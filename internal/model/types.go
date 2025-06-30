package model

import (
	"net/http"
	"time"
)

// RequestLog represents a logged HTTP request
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

// ResponseLog represents the response part of a logged request
type ResponseLog struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Size       int64             `json:"size"`
}

// Config holds the main application configuration
type Config struct {
	Port          int
	TailscaleName string
	Funnel        bool
	Verbose       bool
	JSON          bool
	LogFile       string
	AuthKey       string
	ForceTsnet    bool
	SetPath       string
	ServePort     int
	UseHTTPS      bool
	NoTUI         bool
	NoUI          bool
	UIPort        int
	Mock          bool
}

// ServerMode represents the different modes the server can run in
type ServerMode int

const (
	// ModeProxy proxies requests to a local server
	ModeProxy ServerMode = iota
	// ModeMock returns mock responses for testing
	ModeMock
)

// String returns a string representation of the server mode
func (m ServerMode) String() string {
	switch m {
	case ModeProxy:
		return "proxy"
	case ModeMock:
		return "mock"
	default:
		return "unknown"
	}
}

// TailscaleMode represents the different Tailscale integration modes
type TailscaleMode int

const (
	// ModeLocal uses the local Tailscale daemon
	ModeLocal TailscaleMode = iota
	// ModeTsnet creates a separate tsnet device
	ModeTsnet
)

// String returns a string representation of the Tailscale mode
func (t TailscaleMode) String() string {
	switch t {
	case ModeLocal:
		return "local"
	case ModeTsnet:
		return "tsnet"
	default:
		return "unknown"
	}
}

// StatsSnapshot represents a snapshot of statistics
type StatsSnapshot struct {
	TotalConnections  int     `json:"total_connections"`
	OpenConnections   int     `json:"open_connections"`
	AvgResponseTime1m float64 `json:"avg_response_time_1m"`
	AvgResponseTime5m float64 `json:"avg_response_time_5m"`
	P50ResponseTime   float64 `json:"p50_response_time"`
	P90ResponseTime   float64 `json:"p90_response_time"`
}

// UIServerInfo holds information about a running UI server
type UIServerInfo struct {
	Server        *http.Server
	TailscalePort uint16
	LocalPort     int
	URL           string
}
