package storage

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/cs2demo/platform/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db      *sql.DB
	dataDir string
}

type UploadEntry struct {
	UploadID string `json:"upload_id"`
	Filename string `json:"filename"`
	Error    string `json:"error,omitempty"`
	Path     string `json:"-"`
}

func Open(sqlitePath, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sqlitePath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir sqlite dir: %w", err)
	}
	db, err := sql.Open("sqlite", sqlitePath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db, dataDir: dataDir}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS demos (
  id          TEXT PRIMARY KEY,
  filename    TEXT NOT NULL,
  file_path   TEXT NOT NULL,
  target_user TEXT NOT NULL,
  status      TEXT NOT NULL,
  error       TEXT,
  candidates  TEXT,
  stats_json  TEXT,
  report_json TEXT,
  created_at  DATETIME NOT NULL,
  updated_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_demos_status ON demos(status);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE demos ADD COLUMN candidates TEXT`)
	return nil
}

func (s *Store) SaveUpload(id, filename string, src io.Reader) (string, error) {
	dir := filepath.Join(s.dataDir, "demos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".dem")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := saveDemoContent(filename, src, f); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func (s *Store) SaveUploadEntries(newID func() string, filename string, src io.Reader) ([]UploadEntry, error) {
	br := bufio.NewReader(src)
	kind := detectArchiveKind(filename, br)
	if kind == archivePlain {
		id := newID()
		path, err := s.SaveUpload(id, filename, br)
		if err != nil {
			return nil, err
		}
		return []UploadEntry{{UploadID: id, Filename: filename, Path: path}}, nil
	}

	dir := filepath.Join(s.dataDir, "demos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var entries []UploadEntry
	created := []string{}
	ok := false
	defer func() {
		if ok {
			return
		}
		for _, path := range created {
			_ = os.Remove(path)
		}
	}()
	saveEntry := func(name string, writeTo func(io.Writer) error) error {
		id := newID()
		path := filepath.Join(dir, id+".dem")
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := writeTo(f); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(path)
			return err
		}
		created = append(created, path)
		entries = append(entries, UploadEntry{UploadID: id, Filename: name, Path: path})
		return nil
	}
	var err error
	switch kind {
	case archiveZip:
		err = copyDemoEntriesFromZip(br, saveEntry)
	case archiveRar:
		err = s.withTempArchive(filename, br, func(path string) error {
			return copyDemoEntriesFromRar(path, saveEntry, func(name string, err error) {
				entries = append(entries, UploadEntry{Filename: rarEntryDisplayName(name), Error: err.Error()})
			})
		})
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return entries, nil
}

func (s *Store) UploadPath(id string) (string, error) {
	path := filepath.Join(s.dataDir, "demos", id+".dem")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return path, nil
}

func saveDemoContent(filename string, src io.Reader, dst io.Writer) error {
	br := bufio.NewReader(src)
	switch detectArchiveKind(filename, br) {
	case archiveZip:
		return copyFirstDemoFromZip(br, dst)
	case archiveRar:
		tmpDir := os.TempDir()
		tmp, err := os.CreateTemp(tmpDir, "cs2demo-*.rar")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		ok := false
		defer func() {
			_ = tmp.Close()
			if !ok {
				_ = os.Remove(tmpPath)
			}
		}()
		if _, err := io.Copy(tmp, br); err != nil {
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := copyFirstDemoFromRar(tmpPath, dst); err != nil {
			return err
		}
		ok = true
		_ = os.Remove(tmpPath)
		return nil
	}
	_, err := io.Copy(dst, br)
	return err
}

type archiveKind int

const (
	archivePlain archiveKind = iota
	archiveZip
	archiveRar
)

func detectArchiveKind(filename string, br *bufio.Reader) archiveKind {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".zip" {
		return archiveZip
	}
	if ext == ".rar" {
		return archiveRar
	}
	if sig, err := br.Peek(8); err == nil {
		if len(sig) >= 4 && string(sig[:4]) == "PK\x03\x04" {
			return archiveZip
		}
		if isRarSignature(sig) {
			return archiveRar
		}
	}
	return archivePlain
}

func isRarSignature(sig []byte) bool {
	return len(sig) >= 7 &&
		sig[0] == 'R' && sig[1] == 'a' && sig[2] == 'r' && sig[3] == '!' &&
		sig[4] == 0x1a && sig[5] == 0x07 &&
		(sig[6] == 0x00 || (len(sig) >= 8 && sig[6] == 0x01 && sig[7] == 0x00))
}

func (s *Store) withTempArchive(filename string, src io.Reader, fn func(path string) error) error {
	dir := filepath.Join(s.dataDir, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".archive"
	}
	f, err := os.CreateTemp(dir, "upload-*"+ext)
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fn(path)
}

func copyFirstDemoFromRar(path string, dst io.Writer) error {
	errStop := errors.New("stop after first rar demo")
	err := copyDemoEntriesFromRar(path, func(name string, writeTo func(io.Writer) error) error {
		if err := writeTo(dst); err != nil {
			return err
		}
		return errStop
	}, nil)
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

func copyDemoEntriesFromRar(path string, onDemo func(name string, writeTo func(io.Writer) error) error, onError func(name string, err error)) error {
	entries, err := listRarDemoEntries(path)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("rar does not contain a .dem file")
	}
	var extractErrors []string
	extracted := 0
	for _, entry := range entries {
		extractDir, err := os.MkdirTemp(filepath.Dir(path), "rar-extract-*")
		if err != nil {
			return err
		}
		if err := extractRarEntriesToDir(path, []string{entry}, extractDir); err != nil {
			_ = os.RemoveAll(extractDir)
			extractErrors = append(extractErrors, fmt.Sprintf("%s: %v", entry, err))
			if onError != nil {
				onError(entry, err)
			}
			continue
		}
		name := rarEntryDisplayName(entry)
		if err := onDemo(name, func(dst io.Writer) error {
			extracted, err := extractedEntryPath(extractDir, entry)
			if err != nil {
				return err
			}
			f, err := os.Open(extracted)
			if err != nil {
				return fmt.Errorf("open extracted %s: %w", entry, err)
			}
			defer f.Close()
			_, err = io.Copy(dst, f)
			return err
		}); err != nil {
			_ = os.RemoveAll(extractDir)
			return err
		}
		_ = os.RemoveAll(extractDir)
		extracted++
	}
	if extracted == 0 && len(extractErrors) > 0 && onError == nil {
		return fmt.Errorf("extract rar demos: %s", strings.Join(extractErrors, "; "))
	}
	return nil
}

func rarEntryDisplayName(entry string) string {
	name := filepath.Base(filepath.FromSlash(entry))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return entry
	}
	return name
}

func listRarDemoEntries(path string) ([]string, error) {
	cmd := exec.Command("tar", "-tf", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list rar with tar.exe: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(string(out), "\n")
	entries := []string{}
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if strings.EqualFold(filepath.Ext(name), ".dem") {
			entries = append(entries, name)
		}
	}
	return entries, nil
}

func extractRarEntriesToDir(path string, entries []string, dir string) error {
	args := []string{"-xf", path, "-C", dir}
	args = append(args, entries...)
	cmd := exec.Command("tar", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extract rar with tar.exe: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func extractedEntryPath(root, entry string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(entry))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("unsafe rar entry path: %s", entry)
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe rar entry path: %s", entry)
	}
	return path, nil
}

func copyFirstDemoFromZip(src io.Reader, dst io.Writer) error {
	errStop := errors.New("stop after first demo")
	err := copyDemoEntriesFromZip(src, func(name string, writeTo func(io.Writer) error) error {
		if err := writeTo(dst); err != nil {
			return err
		}
		return errStop
	})
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

func copyDemoEntriesFromZip(src io.Reader, onDemo func(name string, writeTo func(io.Writer) error) error) error {
	found := false
	for {
		header := make([]byte, 30)
		if _, err := io.ReadFull(src, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if found {
					return nil
				}
				return errors.New("zip does not contain a .dem file")
			}
			return err
		}
		sig := binary.LittleEndian.Uint32(header[0:4])
		switch sig {
		case 0x04034b50:
		case 0x02014b50, 0x06054b50:
			if found {
				return nil
			}
			return errors.New("zip does not contain a .dem file")
		default:
			return fmt.Errorf("invalid zip local header: 0x%08x", sig)
		}

		flags := binary.LittleEndian.Uint16(header[6:8])
		method := binary.LittleEndian.Uint16(header[8:10])
		compressedSize := binary.LittleEndian.Uint32(header[18:22])
		uncompressedSize := binary.LittleEndian.Uint32(header[22:26])
		nameLen := binary.LittleEndian.Uint16(header[26:28])
		extraLen := binary.LittleEndian.Uint16(header[28:30])

		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(src, nameBytes); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, src, int64(extraLen)); err != nil {
			return err
		}
		name := string(nameBytes)
		isDemo := strings.EqualFold(filepath.Ext(name), ".dem")

		if flags&0x08 != 0 {
			return errors.New("zip entry uses a data descriptor; please upload the extracted .dem file")
		}
		if flags&0x01 != 0 {
			return errors.New("encrypted zip files are not supported")
		}

		limited := io.LimitReader(src, int64(compressedSize))
		if isDemo {
			found = true
			switch method {
			case 0:
				if err := onDemo(name, func(dst io.Writer) error {
					_, err := io.Copy(dst, limited)
					return err
				}); err != nil {
					return err
				}
			case 8:
				if err := onDemo(name, func(dst io.Writer) error {
					fr := flate.NewReader(limited)
					n, err := io.Copy(dst, fr)
					closeErr := fr.Close()
					recovered := false
					if err != nil && errors.Is(err, io.ErrUnexpectedEOF) && uncompressedSize > 0 {
						if padErr := padMissingZipTail(dst, n, int64(uncompressedSize)); padErr == nil {
							err = nil
							recovered = true
						}
					}
					if err != nil {
						return err
					}
					if recovered && errors.Is(closeErr, io.ErrUnexpectedEOF) {
						return nil
					}
					return closeErr
				}); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported zip compression method %d for %s", method, name)
			}
			if _, err := io.Copy(io.Discard, limited); err != nil {
				return err
			}
			continue
		}
		if _, err := io.Copy(io.Discard, limited); err != nil {
			return err
		}
	}
}

func padMissingZipTail(dst io.Writer, written, expected int64) error {
	missing := expected - written
	const maxRecoverableZipTail = 1 << 20
	if missing <= 0 || missing > maxRecoverableZipTail {
		return io.ErrUnexpectedEOF
	}
	_, err := io.CopyN(dst, zeroReader{}, missing)
	return err
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func (s *Store) CreateDemo(ctx context.Context, d domain.Demo) error {
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
INSERT INTO demos(id, filename, file_path, target_user, status, error, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)`,
		d.ID, d.Filename, d.FilePath, d.TargetUser, d.Status, d.Error, d.CreatedAt, d.UpdatedAt)
	return err
}

func (s *Store) UpdateStatus(ctx context.Context, id string, status domain.DemoStatus, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE demos SET status=?, error=?, updated_at=? WHERE id=?`,
		status, errMsg, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateFailureWithCandidates(ctx context.Context, id string, errMsg string, candidates []string) error {
	b, _ := json.Marshal(candidates)
	_, err := s.db.ExecContext(ctx, `
UPDATE demos SET status=?, error=?, candidates=?, updated_at=? WHERE id=?`,
		domain.StatusFailed, errMsg, string(b), time.Now().UTC(), id)
	return err
}

func (s *Store) SaveStats(ctx context.Context, id string, stats domain.MatchStats) error {
	b, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE demos SET stats_json=?, updated_at=? WHERE id=?`,
		string(b), time.Now().UTC(), id)
	return err
}

func (s *Store) SaveReport(ctx context.Context, id string, r domain.AnalysisReport) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE demos SET report_json=?, updated_at=? WHERE id=?`,
		string(b), time.Now().UTC(), id)
	return err
}

func (s *Store) GetDemo(ctx context.Context, id string) (domain.Demo, error) {
	var d domain.Demo
	var errMsg, candRaw sql.NullString
	row := s.db.QueryRowContext(ctx, `
SELECT id, filename, file_path, target_user, status, COALESCE(error,''), COALESCE(candidates,''), created_at, updated_at
FROM demos WHERE id=?`, id)
	err := row.Scan(&d.ID, &d.Filename, &d.FilePath, &d.TargetUser, &d.Status, &errMsg, &candRaw, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Error = errMsg.String
	if candRaw.Valid && candRaw.String != "" {
		_ = json.Unmarshal([]byte(candRaw.String), &d.Candidates)
	}
	return d, nil
}

func (s *Store) GetReport(ctx context.Context, id string) (domain.AnalysisReport, bool, error) {
	var raw sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT report_json FROM demos WHERE id=?`, id)
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AnalysisReport{}, false, ErrNotFound
		}
		return domain.AnalysisReport{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return domain.AnalysisReport{}, false, nil
	}
	var r domain.AnalysisReport
	if err := json.Unmarshal([]byte(raw.String), &r); err != nil {
		return domain.AnalysisReport{}, false, err
	}
	return r, true, nil
}

func (s *Store) GetStats(ctx context.Context, id string) (domain.MatchStats, bool, error) {
	var raw sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT stats_json FROM demos WHERE id=?`, id)
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.MatchStats{}, false, ErrNotFound
		}
		return domain.MatchStats{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return domain.MatchStats{}, false, nil
	}
	var st domain.MatchStats
	if err := json.Unmarshal([]byte(raw.String), &st); err != nil {
		return domain.MatchStats{}, false, err
	}
	return st, true, nil
}

func (s *Store) ListDemos(ctx context.Context, limit int) ([]domain.Demo, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, filename, file_path, target_user, status, COALESCE(error,''), COALESCE(candidates,''), created_at, updated_at
FROM demos ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Demo
	for rows.Next() {
		var d domain.Demo
		var errMsg, candRaw sql.NullString
		if err := rows.Scan(&d.ID, &d.Filename, &d.FilePath, &d.TargetUser, &d.Status, &errMsg, &candRaw, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Error = errMsg.String
		if candRaw.Valid && candRaw.String != "" {
			_ = json.Unmarshal([]byte(candRaw.String), &d.Candidates)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListAllDoneStats 拉取所有 status=done 的 stats_json，给趋势聚合用。
// 限定 limit 最多 200 场，避免慢查询。
func (s *Store) ListAllDoneStats(ctx context.Context, limit int) ([]TrendRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, target_user, stats_json, created_at
FROM demos
WHERE status = ? AND stats_json IS NOT NULL AND stats_json != ''
ORDER BY created_at DESC
LIMIT ?`, domain.StatusDone, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrendRow
	for rows.Next() {
		var r TrendRow
		var raw string
		if err := rows.Scan(&r.DemoID, &r.TargetUser, &raw, &r.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &r.Stats); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type TrendRow struct {
	DemoID     string
	TargetUser string
	Stats      domain.MatchStats
	CreatedAt  time.Time
}
