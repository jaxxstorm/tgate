// internal/proxy/server.go
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/jaxxstorm/tgate/internal/model"
	"github.com/jaxxstorm/tgate/internal/stats"
)

// LoggingResponseWriter wraps http.ResponseWriter to capture response information
type LoggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int64
	headers    map[string]string
}

// WriteHeader captures the status code
func (lrw *LoggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

// Write captures the response size
func (lrw *LoggingResponseWriter) Write(b []byte) (int, error) {
	if lrw.statusCode == 0 {
		lrw.statusCode = 200
	}
	size, err := lrw.ResponseWriter.Write(b)
	lrw.size += int64(size)
	return size, err
}

// Header returns the response headers
func (lrw *LoggingResponseWriter) Header() http.Header {
	return lrw.ResponseWriter.Header()
}

// captureHeaders captures response headers for logging
func (lrw *LoggingResponseWriter) captureHeaders() {
	lrw.headers = make(map[string]string)
	for k, v := range lrw.ResponseWriter.Header() {
		lrw.headers[k] = strings.Join(v, ", ")
	}
}

// Server handles HTTP requests with logging and optional proxying
type Server struct {
	logger      *zap.Logger
	sugarLogger *zap.SugaredLogger
	proxy       *httputil.ReverseProxy
	targetURL   *url.URL
	requestLog  []model.RequestLog
	logMutex    sync.RWMutex
	program     *tea.Program
	useTUI      bool
	mode        model.ServerMode
	stats       *stats.Tracker
	requestID   int64
	webUIURL    string // Store the web UI URL for display
	maxLogsCap  int    // Maximum number of logs to keep
	listeners   []func(model.RequestLog) // Event listeners for new requests
}

// Config holds configuration for the proxy server
type Config struct {
	TargetPort int
	UseTUI     bool
	Mode       model.ServerMode
	Logger     *zap.Logger
	MaxLogs    int // Maximum number of logs to keep (default: 1000)
}

// NewServer creates a new proxy server
func NewServer(config Config) *Server {
	var targetURL *url.URL
	var proxy *httputil.ReverseProxy

	maxLogs := config.MaxLogs
	if maxLogs <= 0 {
		maxLogs = 1000 // Default
	}

	if config.Mode == model.ModeProxy {
		targetURL = &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("localhost:%d", config.TargetPort),
		}

		proxy = httputil.NewSingleHostReverseProxy(targetURL)

		// Customize the director to preserve original headers
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", req.Host)
		}
	}

	return &Server{
		logger:      config.Logger,
		sugarLogger: config.Logger.Sugar(),
		proxy:       proxy,
		targetURL:   targetURL,
		requestLog:  make([]model.RequestLog, 0),
		useTUI:      config.UseTUI,
		mode:        config.Mode,
		stats:       stats.NewTracker(),
		requestID:   0,
		maxLogsCap:  maxLogs,
		listeners:   make([]func(model.RequestLog), 0),
	}
}

// SetProgram sets the TUI program for sending messages
func (s *Server) SetProgram(p *tea.Program) {
	s.program = p
}

// SetWebUIURL stores the web UI URL for display
func (s *Server) SetWebUIURL(url string) {
	s.webUIURL = url
}

// GetWebUIURL returns the stored web UI URL
func (s *Server) GetWebUIURL() string {
	return s.webUIURL
}

// AddListener adds a listener function that will be called for each new request
func (s *Server) AddListener(listener func(model.RequestLog)) {
	if listener != nil {
		s.listeners = append(s.listeners, listener)
	}
}

// ReplaceLogger replaces the current logger with a new one
func (s *Server) ReplaceLogger(logger *zap.Logger) {
	s.logger = logger
	s.sugarLogger = logger.Sugar()
}

// nextRequestID generates a unique request ID
func (s *Server) nextRequestID() string {
	id := atomic.AddInt64(&s.requestID, 1)
	return fmt.Sprintf("req_%d_%d", time.Now().Unix(), id)
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := s.nextRequestID()

	// Track connection stats
	s.stats.IncrementOpen()
	defer s.stats.DecrementOpen()

	// Create logging response writer
	lrw := &LoggingResponseWriter{
		ResponseWriter: w,
		statusCode:     0,
		size:           0,
		headers:        make(map[string]string),
	}

	// Read request body for logging (if not too large)
	var bodyBytes []byte
	var bodyString string
	if r.Body != nil && r.ContentLength < 10*1024*1024 { // Limit to 10MB
		bodyBytes, _ = io.ReadAll(r.Body)
		bodyString = string(bodyBytes)
		r.Body = io.NopCloser(strings.NewReader(bodyString))
	}

	// Capture request headers
	reqHeaders := make(map[string]string)
	for k, v := range r.Header {
		reqHeaders[k] = strings.Join(v, ", ")
	}

	// Log incoming request
	s.sugarLogger.Infow("Incoming request",
		"method", r.Method,
		"url", r.URL.String(),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
		"content_length", r.ContentLength,
		"request_id", requestID,
	)

	if !s.useTUI {
		// Print request details to console (legacy mode)
		s.printRequestDetails(r, reqHeaders, bodyString)
	}

	// Handle request based on mode
	switch s.mode {
	case model.ModeMock:
		s.handleMockRequest(lrw, r, bodyString)
	case model.ModeProxy:
		s.proxy.ServeHTTP(lrw, r)
	}

	// Capture response headers after serving
	lrw.captureHeaders()

	duration := time.Since(start)

	// Add to stats
	s.stats.AddRequest(duration)

	// Create request log entry
	logEntry := model.RequestLog{
		ID:          requestID,
		Timestamp:   start,
		Method:      r.Method,
		URL:         r.URL.String(),
		RemoteAddr:  r.RemoteAddr,
		Headers:     reqHeaders,
		Body:        bodyString,
		UserAgent:   r.UserAgent(),
		ContentType: r.Header.Get("Content-Type"),
		Size:        r.ContentLength,
		StatusCode:  lrw.statusCode, // Convenience field for UI
		Response: model.ResponseLog{
			StatusCode: lrw.statusCode,
			Headers:    lrw.headers,
			Size:       lrw.size,
		},
		Duration: duration,
	}

	// Store log entry and notify listeners
	s.captureRequest(logEntry)

	// Log response
	s.sugarLogger.Infow("Response sent",
		"status_code", lrw.statusCode,
		"response_size", lrw.size,
		"duration", duration,
		"request_id", requestID,
	)

	if !s.useTUI {
		// Print response summary (legacy mode)
		s.printResponseSummary(lrw.statusCode, lrw.size, duration)
	}
}

// captureRequest stores the log entry and notifies listeners
func (s *Server) captureRequest(logEntry model.RequestLog) {
	// Store log entry
	s.logMutex.Lock()
	s.requestLog = append(s.requestLog, logEntry)
	// Keep only last maxLogsCap requests
	if len(s.requestLog) > s.maxLogsCap {
		s.requestLog = s.requestLog[1:]
	}
	s.logMutex.Unlock()

	// Notify listeners - this is the primary way to send to TUI now
	for _, listener := range s.listeners {
		listener(logEntry)
	}
}

// handleMockRequest handles mock responses for testing
func (s *Server) handleMockRequest(w http.ResponseWriter, r *http.Request, body string) {
	// Set response headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-TGate-Mode", "mock")
	w.Header().Set("X-TGate-Timestamp", time.Now().UTC().Format(time.RFC3339))

	// Create a simple response
	response := map[string]interface{}{
		"status":    "received",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"method":    r.Method,
		"path":      r.URL.Path,
		"headers":   len(r.Header),
		"body_size": len(body),
	}

	// Add query parameters if present
	if len(r.URL.RawQuery) > 0 {
		response["query"] = r.URL.RawQuery
	}

	// Add content type if present
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		response["content_type"] = contentType
	}

	// Return 200 OK with JSON response
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// printRequestDetails prints request details to console (legacy mode)
func (s *Server) printRequestDetails(r *http.Request, headers map[string]string, body string) {
	fmt.Printf("\n╭─ %s %s\n", r.Method, r.URL.String())
	fmt.Printf("├─ From: %s\n", r.RemoteAddr)
	fmt.Printf("├─ Time: %s\n", time.Now().Format("15:04:05"))

	if len(headers) > 0 {
		fmt.Printf("├─ Headers:\n")

		// Sort headers for consistent display
		var sortedHeaders []string
		for k := range headers {
			sortedHeaders = append(sortedHeaders, k)
		}
		sort.Strings(sortedHeaders)

		for i, k := range sortedHeaders {
			prefix := "│  "
			if i == len(sortedHeaders)-1 && body == "" {
				prefix = "│  "
			}
			fmt.Printf("%s%s: %s\n", prefix, k, headers[k])
		}
	}

	if body != "" && len(body) < 1000 { // Only show small bodies
		fmt.Printf("├─ Body:\n")
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			prefix := "│  "
			if i == len(lines)-1 {
				prefix = "│  "
			}
			fmt.Printf("%s%s\n", prefix, line)
		}
	} else if body != "" {
		fmt.Printf("├─ Body: [%d bytes - too large to display]\n", len(body))
	}

	fmt.Printf("╰─ Proxying to %s\n", s.getTargetDescription())
}

// getTargetDescription returns a description of the target
func (s *Server) getTargetDescription() string {
	switch s.mode {
	case model.ModeMock:
		return "mock testing mode (no backing server)"
	case model.ModeProxy:
		return s.targetURL.String()
	default:
		return "unknown mode"
	}
}

// printResponseSummary prints response summary to console (legacy mode)
func (s *Server) printResponseSummary(statusCode int, size int64, duration time.Duration) {
	statusIcon := "✓"
	if statusCode >= 400 {
		statusIcon = "✗"
	}

	fmt.Printf("   %s %d • %s • %d bytes\n",
		statusIcon,
		statusCode,
		duration.Round(time.Millisecond),
		size)
}

// GetRequestLogs returns a copy of the request logs (implements model.LogProvider)
func (s *Server) GetRequestLogs() []model.RequestLog {
	s.logMutex.RLock()
	defer s.logMutex.RUnlock()

	// Return a copy
	logs := make([]model.RequestLog, len(s.requestLog))
	copy(logs, s.requestLog)
	return logs
}

// GetStats returns current statistics (implements model.StatsProvider)
func (s *Server) GetStats() (ttl, opn int, rt1, rt5, p50, p90 float64) {
	return s.stats.GetStats()
}

// SendTUIMessage sends a message to the TUI if available (implements model.TUIMessageSender)
func (s *Server) SendTUIMessage(msg interface{}) {
	if s.program != nil {
		s.program.Send(msg)
	}
}