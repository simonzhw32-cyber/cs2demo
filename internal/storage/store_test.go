package storage

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDemoContentExtractsDemoFromZipWithoutCentralDirectory(t *testing.T) {
	want := []byte("HL2DEMO\x00demo payload")
	var compressed bytes.Buffer
	zw, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("new flate writer: %v", err)
	}
	if _, err := zw.Write(want); err != nil {
		t.Fatalf("write compressed payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close compressed payload: %v", err)
	}

	name := []byte("match.dem")
	var zipLike bytes.Buffer
	header := make([]byte, 30)
	binary.LittleEndian.PutUint32(header[0:4], 0x04034b50)
	binary.LittleEndian.PutUint16(header[4:6], 20)
	binary.LittleEndian.PutUint16(header[8:10], 8)
	binary.LittleEndian.PutUint32(header[18:22], uint32(compressed.Len()))
	binary.LittleEndian.PutUint32(header[22:26], uint32(len(want)))
	binary.LittleEndian.PutUint16(header[26:28], uint16(len(name)))
	zipLike.Write(header)
	zipLike.Write(name)
	zipLike.Write(compressed.Bytes())

	var got bytes.Buffer
	if err := saveDemoContent("match.zip", &zipLike, &got); err != nil {
		t.Fatalf("save demo content: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("extracted payload mismatch: got %q, want %q", got.Bytes(), want)
	}
}

func TestSaveDemoContentPadsRecoverableZipTail(t *testing.T) {
	wantPrefix := []byte("HL2DEMO\x00demo payload")
	var compressed bytes.Buffer
	zw, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("new flate writer: %v", err)
	}
	if _, err := zw.Write(wantPrefix); err != nil {
		t.Fatalf("write compressed payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close compressed payload: %v", err)
	}

	name := []byte("match.dem")
	var zipLike bytes.Buffer
	header := make([]byte, 30)
	binary.LittleEndian.PutUint32(header[0:4], 0x04034b50)
	binary.LittleEndian.PutUint16(header[4:6], 20)
	binary.LittleEndian.PutUint16(header[8:10], 8)
	truncated := compressed.Bytes()[:compressed.Len()-1]
	binary.LittleEndian.PutUint32(header[18:22], uint32(compressed.Len()))
	binary.LittleEndian.PutUint32(header[22:26], uint32(len(wantPrefix)+4))
	binary.LittleEndian.PutUint16(header[26:28], uint16(len(name)))
	zipLike.Write(header)
	zipLike.Write(name)
	zipLike.Write(truncated)

	var got bytes.Buffer
	if err := saveDemoContent("match.zip", &zipLike, &got); err != nil {
		t.Fatalf("save demo content: %v", err)
	}
	if !bytes.Equal(got.Bytes()[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("extracted payload prefix mismatch: got %q, want %q", got.Bytes()[:len(wantPrefix)], wantPrefix)
	}
	if tail := got.Bytes()[len(wantPrefix):]; !bytes.Equal(tail, make([]byte, 4)) {
		t.Fatalf("expected zero-padded tail, got %q", tail)
	}
}

func TestSaveDemoContentCopiesPlainDemo(t *testing.T) {
	want := []byte("HL2DEMO\x00plain payload")
	var got bytes.Buffer
	if err := saveDemoContent("match.dem", bytes.NewReader(want), &got); err != nil {
		t.Fatalf("save demo content: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("copied payload mismatch: got %q, want %q", got.Bytes(), want)
	}
}

func TestDetectArchiveKindRecognizesRAR(t *testing.T) {
	rar4 := []byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x00}
	if got := detectArchiveKind("hltv.rar", bufio.NewReader(bytes.NewReader(rar4))); got != archiveRar {
		t.Fatalf("detectArchiveKind(.rar) = %v, want archiveRar", got)
	}
	rar5 := []byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x01, 0x00}
	if got := detectArchiveKind("upload.bin", bufio.NewReader(bytes.NewReader(rar5))); got != archiveRar {
		t.Fatalf("detectArchiveKind(rar signature) = %v, want archiveRar", got)
	}
}

func TestSaveUploadEntriesExtractsAllDemosFromZip(t *testing.T) {
	dir := t.TempDir()
	store := &Store{dataDir: dir}
	zipLike := localZipDemoEntry(t, "one.dem", []byte("HL2DEMO\x00one"))
	zipLike.Write(localZipDemoEntry(t, "two.dem", []byte("HL2DEMO\x00two")).Bytes())

	next := 0
	entries, err := store.SaveUploadEntries(func() string {
		next++
		return []string{"id-one", "id-two"}[next-1]
	}, "matches.zip", bytes.NewReader(zipLike.Bytes()))
	if err != nil {
		t.Fatalf("SaveUploadEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Filename != "one.dem" || entries[1].Filename != "two.dem" {
		t.Fatalf("unexpected filenames: %+v", entries)
	}
	gotOne, err := os.ReadFile(filepath.Join(dir, "demos", "id-one.dem"))
	if err != nil {
		t.Fatalf("read first demo: %v", err)
	}
	gotTwo, err := os.ReadFile(filepath.Join(dir, "demos", "id-two.dem"))
	if err != nil {
		t.Fatalf("read second demo: %v", err)
	}
	if string(gotOne) != "HL2DEMO\x00one" || string(gotTwo) != "HL2DEMO\x00two" {
		t.Fatalf("unexpected extracted payloads: %q / %q", gotOne, gotTwo)
	}
}

func localZipDemoEntry(t *testing.T, name string, payload []byte) *bytes.Buffer {
	t.Helper()
	var compressed bytes.Buffer
	zw, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("new flate writer: %v", err)
	}
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("write compressed payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close compressed payload: %v", err)
	}

	var out bytes.Buffer
	header := make([]byte, 30)
	binary.LittleEndian.PutUint32(header[0:4], 0x04034b50)
	binary.LittleEndian.PutUint16(header[4:6], 20)
	binary.LittleEndian.PutUint16(header[8:10], 8)
	binary.LittleEndian.PutUint32(header[18:22], uint32(compressed.Len()))
	binary.LittleEndian.PutUint32(header[22:26], uint32(len(payload)))
	binary.LittleEndian.PutUint16(header[26:28], uint16(len(name)))
	out.Write(header)
	out.WriteString(name)
	if _, err := io.Copy(&out, &compressed); err != nil {
		t.Fatalf("copy compressed payload: %v", err)
	}
	return &out
}
