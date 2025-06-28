package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

//go:embed ui/*
var uiFiles embed.FS

// Version will be set by goreleaser
var Version = "dev"

var CLI struct {
	Port          int    `kong:"arg,optional,help='Local port to expose'"`
	TailscaleName string `kong:"short='n',default='tgate',help='Tailscale node name (only used with tsnet mode)'"`
	Funnel        bool   `kong:"short='f',help='Enable Tailscale funnel (public internet access)'"`
	Verbose       bool   `kong:"short='v',help='Enable verbose logging'"`
	JSON          bool   `kong:"short='j',help='Output logs in JSON format'"`
	LogFile       string `kong:"help='Log file path (optional)'"`
	AuthKey       string `kong:"help='Tailscale auth key to create separate tsnet device'"`
	ForceTsnet    bool   `kong:"help='Force tsnet mode even if local Tailscale is available'"`
	SetPath       string `kong:"help='Set custom path for serve (default: /)'"`
	ServePort     int    `kong:"help='Tailscale serve port (default: 80 for HTTP, 443 for HTTPS)'"`
	UseHTTPS      bool   `kong:"help='Use HTTPS instead of HTTP for Tailscale serve'"`
	NoTUI         bool   `kong:"help='Disable TUI and use simple console output'"`
	NoUI          bool   `kong:"help='Disable web UI dashboard'"`
	UIPort        int    `kong:"help='Custom port for web UI (default: auto-assigned)'"`
	Version       bool   `kong:"help='Show version information'"`
	Mock          bool   `kong:"short='m',help='Enable mock/testing mode (no backing server required, enables funnel by default)'"`
}

type RequestLog struct {
	ID          string            `json:"id"`
	Timestamp   time.Time         `json:"timestamp"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	RemoteAddr  string            `json:"remote_addr"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body,omitempty"`
	Response    ResponseLog       `json:"response"`
	Duration    time.Duration     `json:"duration"`
	UserAgent   string            `json:"user_agent"`
	ContentType string            `json:"content_type"`
	Size        int64             `json:"size"`
	StatusCode  int               `json:"status_code"` // Convenience field for UI
}

type ResponseLog struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Size       int64             `json:"size"`
}

type LoggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int64
	headers    map[string]string
}

func (lrw *LoggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *LoggingResponseWriter) Write(b []byte) (int, error) {
	if lrw.statusCode == 0 {
		lrw.statusCode = 200
	}
	size, err := lrw.ResponseWriter.Write(b)
	lrw.size += int64(size)
	return size, err
}

func (lrw *LoggingResponseWriter) Header() http.Header {
	return lrw.ResponseWriter.Header()
}

func (lrw *LoggingResponseWriter) captureHeaders() {
	lrw.headers = make(map[string]string)
	for k, v := range lrw.ResponseWriter.Header() {
		lrw.headers[k] = strings.Join(v, ", ")
	}
}

// Statistics tracking
type ConnectionStats struct {
	TotalConnections int
	OpenConnections  int
	Durations        []time.Duration
	ResponseTimes1m  []time.Duration
	ResponseTimes5m  []time.Duration
	mu               sync.RWMutex
}

type UIServerInfo struct {
	Server        *http.Server
	TailscalePort uint16
	LocalPort     int
	URL           string
}

func (cs *ConnectionStats) AddRequest(duration time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.TotalConnections++
	cs.Durations = append(cs.Durations, duration)

	// Keep only last minute for rt1
	cs.ResponseTimes1m = append(cs.ResponseTimes1m, duration)
	if len(cs.ResponseTimes1m) > 60 {
		cs.ResponseTimes1m = cs.ResponseTimes1m[1:]
	}

	// Keep only last 5 minutes for rt5
	cs.ResponseTimes5m = append(cs.ResponseTimes5m, duration)
	if len(cs.ResponseTimes5m) > 300 {
		cs.ResponseTimes5m = cs.ResponseTimes5m[1:]
	}

	// Keep only last 1000 overall durations
	if len(cs.Durations) > 1000 {
		cs.Durations = cs.Durations[1:]
	}
}

func (cs *ConnectionStats) GetStats() (ttl, opn int, rt1, rt5, p50, p90 float64) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	ttl = cs.TotalConnections
	opn = cs.OpenConnections

	// Calculate rt1 (average response time last minute)
	if len(cs.ResponseTimes1m) > 0 {
		var sum time.Duration
		for _, d := range cs.ResponseTimes1m {
			sum += d
		}
		rt1 = float64(sum) / float64(len(cs.ResponseTimes1m)) / float64(time.Millisecond)
	}

	// Calculate rt5 (average response time last 5 minutes)
	if len(cs.ResponseTimes5m) > 0 {
		var sum time.Duration
		for _, d := range cs.ResponseTimes5m {
			sum += d
		}
		rt5 = float64(sum) / float64(len(cs.ResponseTimes5m)) / float64(time.Millisecond)
	}

	// Calculate percentiles from overall durations
	if len(cs.Durations) > 0 {
		sorted := make([]time.Duration, len(cs.Durations))
		copy(sorted, cs.Durations)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i] < sorted[j]
		})

		p50Idx := len(sorted) * 50 / 100
		p90Idx := len(sorted) * 90 / 100

		if p50Idx < len(sorted) {
			p50 = float64(sorted[p50Idx]) / float64(time.Millisecond)
		}
		if p90Idx < len(sorted) {
			p90 = float64(sorted[p90Idx]) / float64(time.Millisecond)
		}
	}

	return
}

// TUI Message types
type logMsg struct {
	level   string
	message string
	time    time.Time
}

type requestMsg struct {
	log RequestLog
}

type TGateServer struct {
	logger      *zap.Logger
	sugarLogger *zap.SugaredLogger
	proxy       *httputil.ReverseProxy
	targetURL   *url.URL
	requestLog  []RequestLog
	uiInfo      *UIServerInfo // Track UI server info for cleanup
	logMutex    sync.RWMutex
	program     *tea.Program
	useTUI      bool
	mockMode    bool
	stats       *ConnectionStats
	requestID   int64
	webUIURL    string // Store the web UI URL for display
}

func NewTGateServer(logger *zap.Logger, targetPort int, useTUI bool, mockMode bool) *TGateServer {
	var targetURL *url.URL
	var proxy *httputil.ReverseProxy

	if !mockMode {
		targetURL = &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("localhost:%d", targetPort),
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

	return &TGateServer{
		logger:      logger,
		sugarLogger: logger.Sugar(),
		proxy:       proxy,
		targetURL:   targetURL,
		requestLog:  make([]RequestLog, 0),
		useTUI:      useTUI,
		mockMode:    mockMode,
		stats:       &ConnectionStats{},
		requestID:   0,
	}
}

func (s *TGateServer) SetProgram(p *tea.Program) {
	s.program = p
}

func (s *TGateServer) SetWebUIURL(url string) {
	s.webUIURL = url
}

func (s *TGateServer) GetWebUIURL() string {
	return s.webUIURL
}

func (s *TGateServer) nextRequestID() string {
	s.logMutex.Lock()
	defer s.logMutex.Unlock()
	s.requestID++
	return fmt.Sprintf("req_%d_%d", time.Now().Unix(), s.requestID)
}

func (s *TGateServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := s.nextRequestID()

	// Increment open connections
	s.stats.mu.Lock()
	s.stats.OpenConnections++
	s.stats.mu.Unlock()

	defer func() {
		// Decrement open connections
		s.stats.mu.Lock()
		s.stats.OpenConnections--
		s.stats.mu.Unlock()
	}()

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

	if s.mockMode {
		// Mock mode: Just return a successful response
		s.handleMockRequest(lrw, r, bodyString)
	} else {
		// Proxy mode: Forward to backing server
		s.proxy.ServeHTTP(lrw, r)
	}

	// Capture response headers after serving
	lrw.captureHeaders()

	duration := time.Since(start)

	// Add to stats
	s.stats.AddRequest(duration)

	// Create request log entry
	logEntry := RequestLog{
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
		Response: ResponseLog{
			StatusCode: lrw.statusCode,
			Headers:    lrw.headers,
			Size:       lrw.size,
		},
		Duration: duration,
	}

	// Store log entry
	s.logMutex.Lock()
	s.requestLog = append(s.requestLog, logEntry)
	// Keep only last 1000 requests
	if len(s.requestLog) > 1000 {
		s.requestLog = s.requestLog[1:]
	}
	s.logMutex.Unlock()

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
	} else if s.program != nil {
		// Send to TUI
		s.program.Send(requestMsg{log: logEntry})
	}
}

func (s *TGateServer) handleMockRequest(w http.ResponseWriter, r *http.Request, body string) {
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

func (s *TGateServer) printRequestDetails(r *http.Request, headers map[string]string, body string) {
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

func (s *TGateServer) getTargetDescription() string {
	if s.mockMode {
		return "mock testing mode (no backing server)"
	}
	return s.targetURL.String()
}

func (s *TGateServer) printResponseSummary(statusCode int, size int64, duration time.Duration) {
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

func (s *TGateServer) GetRequestLogs() []RequestLog {
	s.logMutex.RLock()
	defer s.logMutex.RUnlock()

	// Return a copy
	logs := make([]RequestLog, len(s.requestLog))
	copy(logs, s.requestLog)
	return logs
}

func (s *TGateServer) GetStats() (ttl, opn int, rt1, rt5, p50, p90 float64) {
	return s.stats.GetStats()
}

// UI Server for serving the web dashboard
type UIServer struct {
	tgateServer *TGateServer
	uiFS        fs.FS
}

func NewUIServer(tgateServer *TGateServer) *UIServer {
	// Create a sub-filesystem for the ui directory
	uiFS, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(fmt.Sprintf("Failed to create UI filesystem: %v", err))
	}

	return &UIServer{
		tgateServer: tgateServer,
		uiFS:        uiFS,
	}
}

func (ui *UIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// API endpoints
	if strings.HasPrefix(r.URL.Path, "/api/") {
		ui.handleAPI(w, r)
		return
	}

	// Static files
	ui.handleStatic(w, r)
}

func (ui *UIServer) handleAPI(w http.ResponseWriter, r *http.Request) {
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
		requests := ui.tgateServer.GetRequestLogs()
		// Format timestamps for easier consumption by the UI
		for i := range requests {
			// Keep original timestamp but add formatted version
			requests[i].Timestamp = requests[i].Timestamp
		}
		json.NewEncoder(w).Encode(requests)
	case "/api/stats":
		ttl, opn, rt1, rt5, p50, p90 := ui.tgateServer.GetStats()
		stats := map[string]interface{}{
			"total_connections":    ttl,
			"open_connections":     opn,
			"avg_response_time_1m": rt1,
			"avg_response_time_5m": rt5,
			"p50_response_time":    p50,
			"p90_response_time":    p90,
		}
		json.NewEncoder(w).Encode(stats)
	default:
		http.NotFound(w, r)
	}
}

func (ui *UIServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	// Remove leading slash for filesystem lookup
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	// Try to serve the file
	file, err := ui.uiFS.Open(path)
	if err != nil {
		// If file not found, serve index.html for SPA routing
		if path != "index.html" {
			file, err = ui.uiFS.Open("index.html")
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

// TUI Model
type model struct {
	statsPane   viewport.Model
	headersPane viewport.Model
	appLogs     viewport.Model
	width       int
	height      int
	appLogLines []string
	lastRequest *RequestLog
	ready       bool
	server      *TGateServer
}

func initialModel(server *TGateServer) model {
	return model{
		statsPane:   viewport.New(0, 0),
		headersPane: viewport.New(0, 0),
		appLogs:     viewport.New(0, 0),
		appLogLines: []string{},
		server:      server,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

type tickMsg struct{}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			// Calculate pane sizes
			topPaneWidth := msg.Width / 2
			topPaneHeight := (msg.Height - 4) / 2    // Top half for two panes
			bottomPaneHeight := (msg.Height - 4) / 2 // Bottom half for logs

			m.statsPane = viewport.New(topPaneWidth-2, topPaneHeight-2)
			m.headersPane = viewport.New(topPaneWidth-2, topPaneHeight-2)
			m.appLogs = viewport.New(msg.Width-4, bottomPaneHeight-2)
			m.width = msg.Width
			m.height = msg.Height
			m.ready = true

			// Set initial content
			m.appLogs.SetContent(strings.Join(m.appLogLines, "\n"))
			m.updateStatsPane()
			m.updateHeadersPane()
		} else {
			// Update existing viewports
			topPaneWidth := msg.Width / 2
			topPaneHeight := (msg.Height - 4) / 2
			bottomPaneHeight := (msg.Height - 4) / 2

			m.statsPane.Width = topPaneWidth - 2
			m.statsPane.Height = topPaneHeight - 2
			m.headersPane.Width = topPaneWidth - 2
			m.headersPane.Height = topPaneHeight - 2
			m.appLogs.Width = msg.Width - 4
			m.appLogs.Height = bottomPaneHeight - 2
			m.width = msg.Width
			m.height = msg.Height
		}

	case tickMsg:
		// Update stats periodically
		if m.ready {
			m.updateStatsPane()
		}
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg{}
		})

	case logMsg:
		// Add to app logs
		timestamp := msg.time.Format("15:04:05")
		levelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		if msg.level == "ERROR" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("196"))
		} else if msg.level == "WARN" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("208"))
		} else if msg.level == "INFO" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("34"))
		}

		logLine := fmt.Sprintf("%s %s %s",
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(timestamp),
			levelStyle.Render(msg.level),
			msg.message)

		m.appLogLines = append(m.appLogLines, logLine)

		// Keep only last 1000 lines
		if len(m.appLogLines) > 1000 {
			m.appLogLines = m.appLogLines[1:]
		}

		if m.ready {
			m.appLogs.SetContent(strings.Join(m.appLogLines, "\n"))
			m.appLogs.GotoBottom()
		}

	case requestMsg:
		// Update request data
		m.lastRequest = &msg.log
		if m.ready {
			m.updateHeadersPane()
			m.updateStatsPane()
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
	}

	// Update viewports
	if m.ready {
		m.statsPane, _ = m.statsPane.Update(msg)
		m.headersPane, _ = m.headersPane.Update(msg)
		m.appLogs, _ = m.appLogs.Update(msg)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) updateStatsPane() {
	if m.server == nil {
		return
	}

	ttl, opn, rt1, rt5, p50, p90 := m.server.GetStats()
	webUIURL := m.server.GetWebUIURL()

	var b strings.Builder

	// Header
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Connection Statistics"))
	b.WriteString("\n\n")

	// Web UI Info (if available)
	if webUIURL != "" {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")).Render("🎨 Web Dashboard"))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(webUIURL))
		b.WriteString("\n\n")
	}

	// Compact stats table with shorter column widths
	b.WriteString(fmt.Sprintf("%-12s %5s %5s %6s %6s %6s %6s\n",
		"Connections", "ttl", "opn", "rt1", "rt5", "p50", "p90"))

	// Stats values
	b.WriteString(fmt.Sprintf("%-12s %5d %5d %6.1f %6.1f %6.1f %6.1f\n",
		"", ttl, opn, rt1, rt5, p50, p90))

	b.WriteString("\n")

	// Compact legend
	b.WriteString("Legend:\n")
	b.WriteString("  ttl: Total\n")
	b.WriteString("  opn: Open\n")
	b.WriteString("  rt1: Avg 1m (ms)\n")
	b.WriteString("  rt5: Avg 5m (ms)\n")
	b.WriteString("  p50: 50th %ile (ms)\n")
	b.WriteString("  p90: 90th %ile (ms)\n")

	m.statsPane.SetContent(b.String())
}

func (m *model) updateHeadersPane() {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Latest Request Headers"))
	b.WriteString("\n\n")

	if m.lastRequest == nil {
		b.WriteString("No requests yet...")
		m.headersPane.SetContent(b.String())
		return
	}

	// Request line
	statusColor := lipgloss.Color("34") // green
	if m.lastRequest.Response.StatusCode >= 400 {
		statusColor = lipgloss.Color("196") // red
	} else if m.lastRequest.Response.StatusCode >= 300 {
		statusColor = lipgloss.Color("208") // orange
	}

	b.WriteString(fmt.Sprintf("%s %s\n",
		lipgloss.NewStyle().Bold(true).Render(m.lastRequest.Method),
		m.lastRequest.URL))

	b.WriteString(fmt.Sprintf("Status: %s  Duration: %s\n",
		lipgloss.NewStyle().Foreground(statusColor).Render(fmt.Sprintf("%d", m.lastRequest.Response.StatusCode)),
		m.lastRequest.Duration.Round(time.Millisecond).String()))

	b.WriteString(fmt.Sprintf("From: %s  Time: %s\n\n",
		m.lastRequest.RemoteAddr,
		m.lastRequest.Timestamp.Format("15:04:05")))

	// Request Headers
	if len(m.lastRequest.Headers) > 0 {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Request Headers:"))
		b.WriteString("\n")

		var sortedHeaders []string
		for k := range m.lastRequest.Headers {
			sortedHeaders = append(sortedHeaders, k)
		}
		sort.Strings(sortedHeaders)

		for _, k := range sortedHeaders {
			b.WriteString(fmt.Sprintf("  %s: %s\n",
				lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Render(k),
				m.lastRequest.Headers[k]))
		}
		b.WriteString("\n")
	}

	// Request Body
	if m.lastRequest.Body != "" {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Request Body:"))
		b.WriteString("\n")
		if len(m.lastRequest.Body) > 500 {
			b.WriteString(fmt.Sprintf("[%d bytes - truncated]\n", len(m.lastRequest.Body)))
			b.WriteString(m.lastRequest.Body[:500])
			b.WriteString("\n...")
		} else {
			b.WriteString(m.lastRequest.Body)
		}
		b.WriteString("\n")
	}

	m.headersPane.SetContent(b.String())
}

func (m model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))

	// Create the top panes (stats and headers)
	leftTopPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("📊 Statistics"),
		borderStyle.Width(m.statsPane.Width).Height(m.statsPane.Height).Render(m.statsPane.View()),
	)

	rightTopPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("🌐 Request Headers"),
		borderStyle.Width(m.headersPane.Width).Height(m.headersPane.Height).Render(m.headersPane.View()),
	)

	// Join top panes horizontally
	topSection := lipgloss.JoinHorizontal(lipgloss.Top, leftTopPane, rightTopPane)

	// Create bottom pane (logs)
	bottomPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("📋 Application Logs"),
		borderStyle.Width(m.appLogs.Width).Height(m.appLogs.Height).Render(m.appLogs.View()),
	)

	// Join top and bottom sections vertically
	main := lipgloss.JoinVertical(lipgloss.Top, topSection, bottomPane)

	// Add footer
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("Press 'q' or Ctrl+C to quit")

	return lipgloss.JoinVertical(lipgloss.Top, main, footer)
}

// Custom log writer for TUI
type tuiLogWriter struct {
	program *tea.Program
}

func (w *tuiLogWriter) Write(p []byte) (n int, err error) {
	// Parse log level and message from zap output
	line := string(p)
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) >= 3 {
		level := strings.TrimSpace(parts[1])
		message := strings.TrimSpace(parts[2])
		if len(parts) > 3 {
			message += " " + strings.TrimSpace(parts[3])
		}

		w.program.Send(logMsg{
			level:   level,
			message: message,
			time:    time.Now(),
		})
	}
	return len(p), nil
}

func setupLogger(verbose bool, jsonFormat bool, logFile string, tuiWriter *tuiLogWriter) (*zap.Logger, error) {
	var config zap.Config

	if jsonFormat {
		config = zap.NewProductionConfig()
	} else {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	if verbose {
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	} else {
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	if logFile != "" {
		config.OutputPaths = []string{logFile, "stdout"}
	}

	logger, err := config.Build()
	if err != nil {
		return nil, err
	}

	// If we have a TUI writer, redirect logs to it
	if tuiWriter != nil {
		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(config.EncoderConfig),
			zapcore.AddSync(tuiWriter),
			config.Level,
		)
		logger = zap.New(core)
	}

	return logger, nil
}

func enableHTTPSFeature(ctx context.Context, lc *local.Client, sugar *zap.SugaredLogger) error {
	// Check if HTTPS is already enabled
	status, err := lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	if status.Self.HasCap(tailcfg.CapabilityHTTPS) {
		sugar.Infof("✅ HTTPS capability already enabled")
		return nil
	}

	sugar.Infof("🔍 HTTPS capability not enabled, need to enable it...")
	sugar.Infof("💡 This will enable HTTPS certificate provisioning for your tailnet")
	sugar.Infof("💡 Go to https://login.tailscale.com/admin/dns and enable 'HTTPS Certificates'")
	sugar.Infof("💡 Or wait while we try to enable it automatically...")

	// Try to enable HTTPS capability
	// Note: This might require admin permissions or interactive approval
	// The exact API for this isn't publicly documented, so we'll provide guidance

	return fmt.Errorf("HTTPS capability needs to be enabled in your Tailscale admin console")
}

func checkTailscaleCertificates(ctx context.Context, lc *local.Client, dnsName string, sugar *zap.SugaredLogger) error {
	// Check if HTTPS certificates are available
	status, err := lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	// Check if the node has HTTPS capability
	if !status.Self.HasCap(tailcfg.CapabilityHTTPS) {
		sugar.Warnf("❌ Node does not have HTTPS capability enabled")
		sugar.Infof("💡 To enable HTTPS certificates:")
		sugar.Infof("   1. Go to https://login.tailscale.com/admin/dns")
		sugar.Infof("   2. Enable 'HTTPS Certificates' for your tailnet")
		sugar.Infof("   3. Wait a few minutes for certificate provisioning")
		return fmt.Errorf("HTTPS certificates not enabled for this tailnet")
	}

	sugar.Infof("✅ HTTPS capability is enabled for this tailnet")

	// Check certificate status - this is a bit tricky as the API doesn't directly expose cert status
	// We can try to check if certificates exist by looking at the certificate domains
	if len(status.CertDomains) == 0 {
		sugar.Warnf("⚠️  No certificate domains found")
		sugar.Infof("💡 Certificate provisioning may still be in progress")
		return fmt.Errorf("no certificate domains available")
	}

	// Check if our domain is in the cert domains
	found := false
	for _, domain := range status.CertDomains {
		if strings.Contains(domain, strings.Split(dnsName, ".")[0]) {
			found = true
			break
		}
	}

	if !found {
		sugar.Warnf("⚠️  Certificate not found for domain %s", dnsName)
		sugar.Infof("💡 Available certificate domains: %v", status.CertDomains)
		sugar.Infof("💡 Certificate provisioning may still be in progress")
		return fmt.Errorf("certificate not available for domain %s", dnsName)
	}

	sugar.Infof("✅ Certificate appears to be available for %s", dnsName)
	return nil
}

func cleanupUIServer(ctx context.Context, lc *local.Client, uiInfo *UIServerInfo, sugar *zap.SugaredLogger) error {
	sc, err := lc.GetServeConfig(ctx)
	if err != nil || sc == nil {
		sugar.Debugf("No serve config to clean up for UI: %v", err)
		return nil
	}

	// Apply the updated config
	err = lc.SetServeConfig(ctx, sc)
	if err != nil {
		sugar.Warnf("Failed to cleanup UI serve config: %v", err)
		return err
	} else {
		sugar.Debugf("Cleaned up UI Tailscale serve configuration")
	}

	return nil
}

func findAvailablePort(startPort int) (int, error) {
	for port := startPort; port < startPort+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found starting from %d", startPort)
}

func findAvailableTailscalePort(sc *ipn.ServeConfig, startPort uint16) (uint16, error) {
	for port := startPort; port < startPort+100; port++ {
		if !sc.IsTCPForwardingOnPort(port) {
			// Also check if web handlers exist on this port
			available := true
			if sc.Web != nil {
				for hostPort := range sc.Web {
					if strings.Contains(string(hostPort), fmt.Sprintf(":%d", port)) {
						available = false
						break
					}
				}
			}
			if available {
				return port, nil
			}
		}
	}
	return 0, fmt.Errorf("no available Tailscale port found starting from %d", startPort)
}

func setupTailscaleServe(ctx context.Context, lc *local.Client, proxyPort int, mountPath string, enableFunnel bool, useHTTPS bool, servePort int, sugar *zap.SugaredLogger) error {
	// Get current serve config
	sc, err := lc.GetServeConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get serve config: %w", err)
	}
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	// Get local client status for DNS name
	st, err := lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")

	// Set up HTTP handler for the proxy target (pointing to our local logging proxy)
	h := &ipn.HTTPHandler{
		Proxy: fmt.Sprintf("http://localhost:%d", proxyPort),
	}

	// Clean mount path
	if mountPath == "" {
		mountPath = "/"
	}
	if !strings.HasPrefix(mountPath, "/") {
		mountPath = "/" + mountPath
	}

	// Determine serve port and TLS usage
	var srvPort uint16
	var useTLS bool

	if servePort != 0 {
		srvPort = uint16(servePort)
		useTLS = useHTTPS || servePort == 443
	} else {
		if useHTTPS {
			srvPort = 443
			useTLS = true
		} else {
			srvPort = 80
			useTLS = false
		}
	}

	sugar.Infof("Setting up Tailscale serve on port %d (TLS: %t)", srvPort, useTLS)

	// Check if port is already in use
	if sc.IsTCPForwardingOnPort(srvPort) {
		return fmt.Errorf("port %d is already serving TCP", srvPort)
	}

	// Set web handler
	sc.SetWebHandler(h, dnsName, srvPort, mountPath, useTLS)

	// If using HTTPS/TLS, we need to also set up the TCP handler for TLS termination
	if useTLS {
		sugar.Infof("🔍 Setting up HTTPS TCP handler for TLS termination...")
		if sc.TCP == nil {
			sc.TCP = make(map[uint16]*ipn.TCPPortHandler)
		}
		sc.TCP[srvPort] = &ipn.TCPPortHandler{
			HTTPS: true,
		}

		if err := enableHTTPSFeature(ctx, lc, sugar); err != nil {
			sugar.Warnf("⚠️  HTTPS feature check failed: %v", err)
			sugar.Infof("💡 HTTPS may not work properly without certificates")
			sugar.Infof("💡 Consider using HTTP mode instead: remove --use-https flag")
		}
	}

	// Enable funnel if requested (only works with HTTPS/443)
	if enableFunnel {
		if !useTLS || srvPort != 443 {
			sugar.Warnf("Funnel requires HTTPS on port 443, but serving on port %d with TLS=%t", srvPort, useTLS)
			sugar.Infof("Consider using --use-https or --serve-port=443")
			return fmt.Errorf("funnel requires HTTPS on port 443")
		}

		// Enable HTTPS feature first if needed
		if err := enableHTTPSFeature(ctx, lc, sugar); err != nil {
			sugar.Errorf("❌ Failed to enable HTTPS feature: %v", err)
			sugar.Infof("💡 Please enable HTTPS certificates in your Tailscale admin console:")
			sugar.Infof("   1. Go to https://login.tailscale.com/admin/dns")
			sugar.Infof("   2. Enable 'HTTPS Certificates'")
			sugar.Infof("   3. Wait a few minutes for provisioning")
			sugar.Infof("   4. Try again")
			return fmt.Errorf("HTTPS certificates not enabled: %w", err)
		}

		// Check certificate status before enabling funnel
		sugar.Infof("🔍 Checking HTTPS certificate status for funnel...")
		if err := checkTailscaleCertificates(ctx, lc, dnsName, sugar); err != nil {
			sugar.Errorf("❌ Certificate check failed: %v", err)
			sugar.Infof("💡 You can:")
			sugar.Infof("   • Wait a few minutes if certificates are still provisioning")
			sugar.Infof("   • Try running without --funnel first to test local access")
			sugar.Infof("   • Check https://login.tailscale.com/admin/dns for certificate settings")
			return fmt.Errorf("cannot enable funnel: %w", err)
		}

		sc.SetFunnel(dnsName, srvPort, true)
		sugar.Infof("🌍 Funnel enabled - service will be available on the internet")
	}

	// Apply the serve config
	err = lc.SetServeConfig(ctx, sc)
	if err != nil {
		return fmt.Errorf("failed to set serve config: %w", err)
	}

	// Display URL information with certificate status
	scheme := "http"
	if useTLS {
		scheme = "https"
	}

	portPart := ""
	if (scheme == "http" && srvPort != 80) || (scheme == "https" && srvPort != 443) {
		portPart = fmt.Sprintf(":%d", srvPort)
	}

	url := fmt.Sprintf("%s://%s%s%s", scheme, dnsName, portPart, mountPath)

	if enableFunnel {
		sugar.Infof("🌍 Available on the internet: %s", url)
		sugar.Infof("💡 If you get TLS errors, certificates may still be provisioning")
		sugar.Infof("💡 Try again in 2-3 minutes if the connection fails")
	} else {
		sugar.Infof("🔒 Available within your tailnet: %s", url)
		if useTLS {
			sugar.Infof("💡 If you get TLS errors, try HTTP first or wait for certificate provisioning")
		}
	}

	return nil
}

func setupUIServer(ctx context.Context, lc *local.Client, requestedUIPort int, tgateServer *TGateServer, sugar *zap.SugaredLogger) (*UIServerInfo, error) {
	// Find available local port
	var uiPort int
	var err error
	if requestedUIPort != 0 {
		uiPort = requestedUIPort
	} else {
		uiPort, err = findAvailablePort(9080)
		if err != nil {
			return nil, fmt.Errorf("failed to find available port for UI server: %w", err)
		}
	}

	// Create UI server
	uiServer := NewUIServer(tgateServer)

	// Set up Tailscale serve for UI (HTTP only, no funnel)
	sc, err := lc.GetServeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get serve config: %w", err)
	}
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	// Get DNS name
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")

	// Find available Tailscale port starting from 8080
	tailscalePort, err := findAvailableTailscalePort(sc, 8080)
	if err != nil {
		return nil, fmt.Errorf("failed to find available Tailscale port: %w", err)
	}

	uiHandler := &ipn.HTTPHandler{
		Proxy: fmt.Sprintf("http://localhost:%d", uiPort),
	}

	sc.SetWebHandler(uiHandler, dnsName, tailscalePort, "/ui/", false) // HTTP only, no TLS

	// Apply the serve config
	err = lc.SetServeConfig(ctx, sc)
	if err != nil {
		return nil, fmt.Errorf("failed to set UI serve config: %w", err)
	}

	// Start UI server on local port
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", uiPort),
		Handler: uiServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			sugar.Errorf("UI server error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	uiURL := fmt.Sprintf("http://%s:%d/ui/", dnsName, tailscalePort)
	sugar.Infof("🎨 Web UI available within your tailnet: %s", uiURL)

	uiInfo := &UIServerInfo{
		Server:        httpServer,
		TailscalePort: tailscalePort,
		LocalPort:     uiPort,
		URL:           uiURL,
	}

	return uiInfo, nil
}

func cleanupTailscaleServe(ctx context.Context, lc *local.Client, port int, mountPath string, useHTTPS bool, servePort int, sugar *zap.SugaredLogger) error {
	sc, err := lc.GetServeConfig(ctx)
	if err != nil || sc == nil {
		sugar.Debugf("No serve config to clean up: %v", err)
		return nil // Nothing to clean up
	}

	st, err := lc.Status(ctx)
	if err != nil {
		sugar.Warnf("Failed to get status during cleanup: %v", err)
		return err
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")

	if mountPath == "" {
		mountPath = "/"
	}
	if !strings.HasPrefix(mountPath, "/") {
		mountPath = "/" + mountPath
	}

	// Determine the port we used for serving
	var srvPort uint16
	if servePort != 0 {
		srvPort = uint16(servePort)
	} else {
		if useHTTPS {
			srvPort = 443
		} else {
			srvPort = 80
		}
	}
	// Helper function to safely remove web handlers
	safeRemoveWebHandler := func(dnsName string, port uint16, paths []string, allowFunnel bool) {
		defer func() {
			if r := recover(); r != nil {
				sugar.Warnf("Recovered from panic while removing web handler on port %d: %v", port, r)
			}
		}()

		// Check if the web config exists for this port before trying to remove
		if sc.Web != nil {
			hostPort := ipn.HostPort(dnsName + ":" + fmt.Sprintf("%d", port))
			if hostConfig, exists := sc.Web[hostPort]; exists {
				if _, portExists := hostConfig.Handlers[fmt.Sprintf("%d", port)]; portExists {
					sc.RemoveWebHandler(dnsName, port, paths, allowFunnel)
					sugar.Debugf("Removed web handler for %s:%d%v", dnsName, port, paths)
				}
			}
		}
	}

	// Remove main service handler
	safeRemoveWebHandler(dnsName, srvPort, []string{mountPath}, true)

	// Also cleanup potential UI handlers
	safeRemoveWebHandler(dnsName, 8080, []string{"/ui/"}, false)
	safeRemoveWebHandler(dnsName, 8081, []string{"/ui/"}, false)
	safeRemoveWebHandler(dnsName, 8082, []string{"/ui/"}, false)

	// Apply the updated config
	err = lc.SetServeConfig(ctx, sc)
	if err != nil {
		sugar.Warnf("Failed to cleanup serve config: %v", err)
		return err
	} else {
		sugar.Infof("Cleaned up Tailscale serve configuration")
	}

	return nil
}

func main() {
	kong.Parse(&CLI)

	// Handle version flag
	if CLI.Version {
		fmt.Printf("tgate version %s\n", Version)
		os.Exit(0)
	}

	// Validate arguments
	if CLI.Mock && CLI.Port != 0 {
		fmt.Fprintf(os.Stderr, "Error: Cannot specify both port and --mock flag\n")
		fmt.Fprintf(os.Stderr, "Usage: tgate <port> [flags]     (proxy mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --mock [flags]     (mock/testing mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --version\n")
		os.Exit(1)
	}

	if !CLI.Mock && CLI.Port == 0 {
		fmt.Fprintf(os.Stderr, "Error: port argument is required (or use --mock for testing mode)\n")
		fmt.Fprintf(os.Stderr, "Usage: tgate <port> [flags]     (proxy mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --mock [flags]     (mock/testing mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --version\n")
		os.Exit(1)
	}

	// Auto-enable funnel for mock mode unless explicitly disabled
	if CLI.Mock && !CLI.Funnel {
		CLI.Funnel = true
		CLI.UseHTTPS = true
	}

	// If funnel is enabled, automatically enable HTTPS since funnel requires it
	if CLI.Funnel {
		CLI.UseHTTPS = true
	}

	// Setup basic logger first (before TUI)
	logger, err := setupLogger(CLI.Verbose, CLI.JSON, CLI.LogFile, nil)
	if err != nil {
		fmt.Printf("Failed to setup logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	sugar := logger.Sugar()

	if CLI.Mock {
		sugar.Infof("🎭 Mock mode enabled - automatically enabling funnel for external access")
	}

	if CLI.Funnel {
		sugar.Infof("🌍 Funnel enabled - automatically enabling HTTPS")
	}

	sugar.Infof("Starting tgate server...")
	if CLI.Mock {
		sugar.Infof("Mode: Mock/testing (no backing server)")
	} else {
		sugar.Infof("Local target: localhost:%d", CLI.Port)
	}
	sugar.Infof("Funnel enabled: %t", CLI.Funnel)
	sugar.Infof("HTTPS enabled: %t", CLI.UseHTTPS)
	sugar.Infof("Web UI enabled: %t", !CLI.NoUI)

	// Test local connection only in proxy mode
	if !CLI.Mock {
		testConn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", CLI.Port), 5*time.Second)
		if err != nil {
			sugar.Fatalf("Cannot connect to local server at localhost:%d - %v", CLI.Port, err)
		}
		testConn.Close()
		sugar.Infof("✓ Local server is reachable")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Determine which mode to use
	useLocalTailscale := false
	var localClient *local.Client

	if !CLI.ForceTsnet && CLI.AuthKey == "" {
		// Try to use local Tailscale
		localClient = &local.Client{}
		_, err := localClient.Status(ctx)
		if err == nil {
			useLocalTailscale = true
			sugar.Infof("✓ Using local Tailscale daemon")
		} else {
			sugar.Infof("Local Tailscale not available: %v", err)
			sugar.Infof("Falling back to tsnet mode")
		}
	}

	if CLI.AuthKey != "" {
		sugar.Infof("Auth key provided - using tsnet mode")
	}

	if CLI.ForceTsnet {
		sugar.Infof("Forced tsnet mode")
	}

	var cleanup func() error
	var tgateServer *TGateServer
	var uiCleanup func() error

	if useLocalTailscale {
		// Create our logging proxy server
		tgateServer = NewTGateServer(logger, CLI.Port, !CLI.NoTUI, CLI.Mock)

		// Set up UI server if enabled
		if !CLI.NoUI {
			uiPort := CLI.UIPort
			if uiPort == 0 {
				uiPort, err = findAvailablePort(9080)
				if err != nil {
					sugar.Warnf("Failed to find available port for UI server: %v", err)
					sugar.Infof("UI server disabled")
				}
			}

			if uiPort > 0 {
				sugar.Infof("Starting web UI server on port %d", uiPort)
				uiInfo, err := setupUIServer(ctx, localClient, uiPort, tgateServer, sugar)
				if err != nil {
					sugar.Warnf("Failed to setup UI server: %v", err)
					sugar.Infof("Continuing without web UI")
				} else {
					sugar.Infof("🎨 Web UI dashboard: %s", uiInfo.URL)
					tgateServer.SetWebUIURL(uiInfo.URL) // Store the URL for TUI display
					uiCleanup = func() error {
						// First cleanup the Tailscale serve config for UI
						if err := cleanupUIServer(context.Background(), localClient, uiInfo, sugar); err != nil {
							sugar.Warnf("Failed to cleanup UI Tailscale serve: %v", err)
						}
						// Then shutdown the HTTP server
						shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						return uiInfo.Server.Shutdown(shutdownCtx)
					}
				}
			}
		}

		if CLI.Mock {
			// In mock mode, serve directly without proxy
			// Find an available port for our mock server
			proxyPort, err := findAvailablePort(8080)
			if err != nil {
				sugar.Fatalf("Failed to find available port for mock server: %v", err)
			}

			sugar.Infof("Starting mock testing server on port %d", proxyPort)

			// Start our mock server
			proxyServer := &http.Server{
				Addr:    fmt.Sprintf("localhost:%d", proxyPort),
				Handler: tgateServer,
			}

			go func() {
				if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					sugar.Errorf("Mock server error: %v", err)
				}
			}()

			// Give the server a moment to start
			time.Sleep(100 * time.Millisecond)

			// Use local Tailscale serve (pointing to our mock server)
			sugar.Infof("Setting up Tailscale serve...")

			err = setupTailscaleServe(ctx, localClient, proxyPort, CLI.SetPath, CLI.Funnel, CLI.UseHTTPS, CLI.ServePort, sugar)
			if err != nil {
				sugar.Fatalf("Failed to setup Tailscale serve: %v", err)
			}

			cleanup = func() error {
				// Cleanup Tailscale serve config
				cleanupTailscaleServe(context.Background(), localClient, 0, CLI.SetPath, CLI.UseHTTPS, CLI.ServePort, sugar)
				// Shutdown mock server
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return proxyServer.Shutdown(shutdownCtx)
			}

			sugar.Infof("🚀 tgate mock server configured with Tailscale serve")
			sugar.Infof("🔗 All requests will be logged and acknowledged")
		} else {
			// Find an available port for our local logging proxy
			proxyPort, err := findAvailablePort(CLI.Port + 1000)
			if err != nil {
				sugar.Fatalf("Failed to find available port for logging proxy: %v", err)
			}

			sugar.Infof("Starting local logging proxy on port %d", proxyPort)

			// Start our logging proxy server
			proxyServer := &http.Server{
				Addr:    fmt.Sprintf("localhost:%d", proxyPort),
				Handler: tgateServer,
			}

			go func() {
				if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					sugar.Errorf("Logging proxy server error: %v", err)
				}
			}()

			// Give the proxy server a moment to start
			time.Sleep(100 * time.Millisecond)

			// Use local Tailscale serve (pointing to our logging proxy)
			sugar.Infof("Setting up Tailscale serve...")

			err = setupTailscaleServe(ctx, localClient, proxyPort, CLI.SetPath, CLI.Funnel, CLI.UseHTTPS, CLI.ServePort, sugar)
			if err != nil {
				sugar.Fatalf("Failed to setup Tailscale serve: %v", err)
			}

			cleanup = func() error {
				// Cleanup Tailscale serve config
				cleanupTailscaleServe(context.Background(), localClient, CLI.Port, CLI.SetPath, CLI.UseHTTPS, CLI.ServePort, sugar)
				// Shutdown proxy server
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return proxyServer.Shutdown(shutdownCtx)
			}

			sugar.Infof("🚀 tgate server configured with Tailscale serve + logging proxy")
			sugar.Infof("🔍 All requests will be logged and forwarded to localhost:%d", CLI.Port)
		}
	} else {
		// Use tsnet mode
		tgateServer = NewTGateServer(logger, CLI.Port, !CLI.NoTUI, CLI.Mock)

		httpServer := &http.Server{
			Handler: tgateServer,
		}

		var tsnetServer *tsnet.Server
		if CLI.AuthKey != "" {
			tsnetServer = &tsnet.Server{
				Hostname: CLI.TailscaleName,
				AuthKey:  CLI.AuthKey,
			}
		} else {
			tsnetServer = &tsnet.Server{
				Hostname: CLI.TailscaleName,
			}
		}

		sugar.Infof("Tailscale node name: %s", CLI.TailscaleName)

		ln, err := tsnetServer.Listen("tcp", ":80")
		if err != nil {
			sugar.Fatalf("Failed to listen on Tailscale device: %v", err)
		}

		// Get the device's Tailscale URL
		status, err := tsnetServer.Up(ctx)
		if err != nil {
			sugar.Warnf("Could not get device status: %v", err)
		} else {
			tailscaleURL := fmt.Sprintf("https://%s", status.Self.DNSName)
			sugar.Infof("📡 Tailscale URL: %s", tailscaleURL)
		}

		cleanup = func() error {
			httpServer.Shutdown(context.Background())
			ln.Close()
			tsnetServer.Close()
			return nil
		}

		go func() {
			sugar.Infof("🚀 tgate server started with tsnet")
			if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
				sugar.Errorf("HTTP server error: %v", err)
			}
		}()
	}

	if CLI.NoTUI {
		// Display running information (legacy mode)
		fmt.Printf("\n" + strings.Repeat("─", 60) + "\n")
		if useLocalTailscale {
			fmt.Printf("  tgate is running with Tailscale serve!\n")
			fmt.Printf("  Mode: Local Tailscale daemon\n")
		} else {
			fmt.Printf("  tgate is running with tsnet!\n")
			fmt.Printf("  Mode: tsnet device (%s)\n", CLI.TailscaleName)
		}
		if CLI.Mock {
			fmt.Printf("  Mode: Mock/Public\n")
		} else {
			fmt.Printf("  Target: localhost:%d\n", CLI.Port)
		}
		if !CLI.NoUI {
			fmt.Printf("  Web UI: Available via Tailscale\n")
		}
		fmt.Printf(strings.Repeat("─", 60) + "\n\n")

		// Wait for shutdown signal
		<-ctx.Done()
	} else {
		// Initialize TUI after everything is set up
		sugar.Infof("🎨 Starting TUI interface...")
		sugar.Infof("💡 Press 'q' or Ctrl+C to quit")

		// Log web UI URL if available
		if !CLI.NoUI && tgateServer.GetWebUIURL() != "" {
			sugar.Infof("🌐 Web Dashboard: %s", tgateServer.GetWebUIURL())
		}

		// Create TUI program
		program := tea.NewProgram(initialModel(tgateServer), tea.WithAltScreen())

		// Set up TUI logger
		tuiWriter := &tuiLogWriter{program: program}
		tuiLogger, err := setupLogger(CLI.Verbose, CLI.JSON, CLI.LogFile, tuiWriter)
		if err != nil {
			sugar.Errorf("Failed to setup TUI logger: %v", err)
		} else {
			// Connect the server to the TUI
			tgateServer.SetProgram(program)
			// Update logger to use TUI
			tgateServer.logger = tuiLogger
			tgateServer.sugarLogger = tuiLogger.Sugar()
		}

		// Run TUI in a goroutine and wait for shutdown
		tuiDone := make(chan struct{})
		go func() {
			defer close(tuiDone)
			if _, err := program.Run(); err != nil {
				fmt.Printf("TUI error: %v\n", err)
			}
		}()

		// Wait for shutdown signal or TUI exit
		select {
		case <-ctx.Done():
			program.Quit()
		case <-tuiDone:
			cancel()
		}
	}

	sugar.Infof("Shutting down tgate server...")

	if cleanup != nil {
		if err := cleanup(); err != nil {
			sugar.Errorf("Error during cleanup: %v", err)
		}
	}

	if uiCleanup != nil {
		if err := uiCleanup(); err != nil {
			sugar.Errorf("Error during UI cleanup: %v", err)
		}
	}

	sugar.Infof("tgate server stopped")
}
