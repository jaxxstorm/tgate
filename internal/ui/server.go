//internal/ui/server.go
package ui

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
	
	"github.com/jaxxstorm/tgate/internal/model"
)

// LogProvider interface for getting request logs and stats
type LogProvider interface {
	GetRequestLogs() []model.RequestLog
	GetStats() (ttl, opn int, rt1, rt5, p50, p90 float64)
}

// Server serves the web dashboard UI
type Server struct {
	logProvider LogProvider
	uiFS        fs.FS
}

// NewServer creates a new UI server with the given log provider and embedded filesystem
func NewServer(logProvider LogProvider, uiFS fs.FS) *Server {
	// Create a sub-filesystem for the ui directory if needed
	if uiFS != nil {
		if subFS, err := fs.Sub(uiFS, "ui"); err == nil {
			uiFS = subFS
		}
	}

	return &Server{
		logProvider: logProvider,
		uiFS:        uiFS,
	}
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// API endpoints
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.handleAPI(w, r)
		return
	}

	// Static files
	s.handleStatic(w, r)
}

// handleAPI handles API requests for the web dashboard
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.URL.Path {
	case "/api/requests":
		if s.logProvider == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "log provider not available"})
			return
		}
		requests := s.logProvider.GetRequestLogs()
		json.NewEncoder(w).Encode(requests)
	case "/api/stats":
		if s.logProvider == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "stats provider not available"})
			return
		}
		ttl, opn, rt1, rt5, p50, p90 := s.logProvider.GetStats()
		stats := map[string]interface{}{
			"total_connections":    ttl,
			"open_connections":     opn,
			"avg_response_time_1m": rt1,
			"avg_response_time_5m": rt5,
			"p50_response_time":    p50,
			"p90_response_time":    p90,
		}
		json.NewEncoder(w).Encode(stats)
	case "/api/health":
		// Health check endpoint
		health := map[string]interface{}{
			"status": "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"log_provider": s.logProvider != nil,
		}
		if s.logProvider != nil {
			requests := s.logProvider.GetRequestLogs()
			health["request_count"] = len(requests)
		}
		json.NewEncoder(w).Encode(health)
	default:
		http.NotFound(w, r)
	}
}

// handleStatic serves static files from the embedded filesystem
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.uiFS == nil {
		http.NotFound(w, r)
		return
	}

	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	// Remove leading slash for filesystem lookup
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	// Try to serve the file
	file, err := s.uiFS.Open(path)
	if err != nil {
		// If file not found, serve index.html for SPA routing
		if path != "index.html" {
			file, err = s.uiFS.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
	}
	defer file.Close()

	// Get file info for content type
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Set appropriate content type
	switch {
	case strings.HasSuffix(path, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(path, ".json"):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}

	// Serve the file
	http.ServeContent(w, r, info.Name(), info.ModTime(), file.(io.ReadSeeker))
}

// ServerInfo holds information about the UI server for cleanup
type ServerInfo struct {
	Server        *http.Server
	TailscalePort uint16
	LocalPort     int
	URL           string
}