package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/cs2demo/platform/internal/analyzer"
	"github.com/cs2demo/platform/internal/domain"
	"github.com/cs2demo/platform/internal/orchestrator"
	"github.com/cs2demo/platform/internal/parser"
	"github.com/cs2demo/platform/internal/prokb"
	"github.com/cs2demo/platform/internal/storage"
)

type Server struct {
	Store            *storage.Store
	Orch             *orchestrator.Orchestrator
	Parser           *parser.Parser
	KB               prokb.KB
	AnalyzerProvider string
	MaxUploadBytes   int64
	WebDir           string
}

func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.MaxMultipartMemory = 32 << 20

	r.Use(func(c *gin.Context) {
		if c.Request.Method == http.MethodPost && s.MaxUploadBytes > 0 {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.MaxUploadBytes)
		}
		c.Next()
	})

	r.GET("/healthz", func(c *gin.Context) {
		provider := s.AnalyzerProvider
		if provider == "" {
			provider = "unknown"
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "analyzer_provider": provider})
	})

	r.POST("/demos/inspect", s.handleInspectUpload)
	r.POST("/demos", s.handleUpload)
	r.GET("/demos", s.handleList)
	r.GET("/demos/:id", s.handleGet)
	r.GET("/demos/:id/report", s.handleReport)
	r.GET("/demos/:id/stats", s.handleStats)
	r.GET("/trends", s.handleTrends)
	r.GET("/players", s.handlePlayers)

	if s.WebDir != "" {
		r.GET("/", func(c *gin.Context) { c.File(filepath.Join(s.WebDir, "index.html")) })
		r.Static("/static", s.WebDir)
	}
	return r
}

func (s *Server) handleUpload(c *gin.Context) {
	target := strings.TrimSpace(c.PostForm("player"))
	if target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing player field (target username)"})
		return
	}

	id := uuid.NewString()
	uploads := []storage.UploadEntry{}
	uploadID := strings.TrimSpace(c.PostForm("upload_id"))
	uploadIDs := c.PostFormArray("upload_ids")
	if uploadID != "" && len(uploadIDs) == 0 {
		uploadIDs = []string{uploadID}
	}
	if len(uploadIDs) > 0 {
		filenames := c.PostFormArray("filenames")
		for i, uploadID := range uploadIDs {
			uploadID = strings.TrimSpace(uploadID)
			if _, err := uuid.Parse(uploadID); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload_id"})
				return
			}
			path, err := s.Store.UploadPath(uploadID)
			if errors.Is(err, storage.ErrNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "upload_id not found; choose the demo file again"})
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "load upload: " + err.Error()})
				return
			}
			filename := ""
			if i < len(filenames) {
				filename = strings.TrimSpace(filenames[i])
			}
			if filename == "" {
				filename = c.PostForm("filename")
			}
			if filename == "" {
				filename = uploadID + ".dem"
			}
			uploads = append(uploads, storage.UploadEntry{UploadID: uploadID, Filename: filename, Path: path})
		}
	} else {
		file, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing file field or upload_id: " + err.Error()})
			return
		}
		src, err := file.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "open upload: " + err.Error()})
			return
		}
		defer src.Close()
		nextUploadID := 0
		entries, err := s.Store.SaveUploadEntries(func() string {
			nextUploadID++
			if nextUploadID == 1 {
				return id
			}
			return uuid.NewString()
		}, file.Filename, src)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save upload: " + err.Error()})
			return
		}
		for _, entry := range entries {
			if entry.Path != "" {
				uploads = append(uploads, entry)
			}
		}
	}
	if len(uploads) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "archive contains no usable .dem files"})
		return
	}

	demoIDs := make([]string, 0, len(uploads))
	for i, up := range uploads {
		demoID := id
		if i > 0 || len(uploads) > 1 {
			demoID = uuid.NewString()
		}
		demo := domain.Demo{
			ID:         demoID,
			Filename:   up.Filename,
			FilePath:   up.Path,
			TargetUser: target,
			Status:     domain.StatusQueued,
		}
		if err := s.Store.CreateDemo(c.Request.Context(), demo); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create demo: " + err.Error()})
			return
		}

		s.Orch.Enqueue(orchestrator.Job{DemoID: demoID, FilePath: up.Path, TargetUser: target})
		demoIDs = append(demoIDs, demoID)
	}

	c.JSON(http.StatusAccepted, gin.H{
		"demo_id":  demoIDs[0],
		"demo_ids": demoIDs,
		"status":   domain.StatusQueued,
		"poll_url": "/demos/" + demoIDs[0],
	})
}

func (s *Server) handleInspectUpload(c *gin.Context) {
	if s.Parser == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "parser not configured"})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file field: " + err.Error()})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open upload: " + err.Error()})
		return
	}
	defer src.Close()

	entries, err := s.Store.SaveUploadEntries(uuid.NewString, file.Filename, src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save upload: " + err.Error()})
		return
	}
	type inspectedEntry struct {
		UploadID string   `json:"upload_id"`
		Filename string   `json:"filename"`
		Players  []string `json:"players"`
		Error    string   `json:"error,omitempty"`
	}
	out := make([]inspectedEntry, 0, len(entries))
	seenPlayers := map[string]bool{}
	players := []string{}
	first := storage.UploadEntry{}
	for _, entry := range entries {
		ie := inspectedEntry{UploadID: entry.UploadID, Filename: entry.Filename, Error: entry.Error}
		if entry.Path != "" && entry.Error == "" {
			if first.Path == "" {
				first = entry
			}
			entryPlayers, err := s.Parser.ListPlayers(entry.Path)
			ie.Players = entryPlayers
			if err != nil {
				ie.Error = err.Error()
			}
		}
		out = append(out, ie)
		for _, p := range ie.Players {
			if !seenPlayers[p] {
				seenPlayers[p] = true
				players = append(players, p)
			}
		}
	}
	if first.Path == "" && len(entries) > 0 {
		first = entries[0]
	}
	c.JSON(http.StatusOK, gin.H{
		"upload_id": first.UploadID,
		"filename":  first.Filename,
		"players":   players,
		"entries":   out,
	})
}

func (s *Server) handleGet(c *gin.Context) {
	d, err := s.Store.GetDemo(c.Request.Context(), c.Param("id"))
	if errors.Is(err, storage.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "demo not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *Server) handleList(c *gin.Context) {
	list, err := s.Store.ListDemos(c.Request.Context(), 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"demos": list})
}

func (s *Server) handleReport(c *gin.Context) {
	id := c.Param("id")
	d, err := s.Store.GetDemo(c.Request.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "demo not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if d.Status != domain.StatusDone {
		c.JSON(http.StatusAccepted, gin.H{"status": d.Status, "error": d.Error})
		return
	}
	r, ok, err := s.Store.GetReport(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusAccepted, gin.H{"status": d.Status, "note": "report still generating"})
		return
	}
	c.JSON(http.StatusOK, r)
}

func (s *Server) handleStats(c *gin.Context) {
	id := c.Param("id")
	st, ok, err := s.Store.GetStats(c.Request.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "demo not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusAccepted, gin.H{"note": "stats not ready"})
		return
	}
	c.JSON(http.StatusOK, st)
}

func (s *Server) handleTrends(c *gin.Context) {
	player := c.Query("player")
	if player == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing player query param"})
		return
	}
	rows, err := s.Store.ListAllDoneStats(c.Request.Context(), 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if s.KB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge base not configured"})
		return
	}
	trend := analyzer.BuildTrend(player, rows, s.KB)
	if trend.MatchesCount == 0 {
		c.JSON(http.StatusOK, gin.H{
			"player":        player,
			"matches_count": 0,
			"note":          "no completed matches found for this player yet",
		})
		return
	}
	c.JSON(http.StatusOK, trend)
}

func (s *Server) handlePlayers(c *gin.Context) {
	rows, err := s.Store.ListAllDoneStats(c.Request.Context(), 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	seen := map[string]int{}
	for _, r := range rows {
		if r.Stats.Target.Name != "" {
			seen[r.Stats.Target.Name]++
		}
	}
	type entry struct {
		Name    string `json:"name"`
		Matches int    `json:"matches"`
	}
	out := make([]entry, 0, len(seen))
	for name, n := range seen {
		out = append(out, entry{Name: name, Matches: n})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Matches > out[j-1].Matches; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	c.JSON(http.StatusOK, gin.H{"players": out})
}

var _ = domain.StatusDone
