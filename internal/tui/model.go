// internal/tui/model.go
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	
	"github.com/jaxxstorm/tgate/internal/model"
)

// StatsProvider interface for getting statistics
type StatsProvider interface {
	GetStats() (ttl, opn int, rt1, rt5, p50, p90 float64)
	GetWebUIURL() string
}

// Model represents the TUI application state
type Model struct {
	statsPane   viewport.Model
	headersPane viewport.Model
	appLogs     viewport.Model
	width       int
	height      int
	appLogLines []string
	lastRequest *model.RequestLog
	ready       bool
	server      StatsProvider
}

// Message types for TUI updates
type LogMsg struct {
	Level   string
	Message string
	Time    time.Time
}

// RequestMsg is the correct message type for request updates
type RequestMsg struct {
	Log model.RequestLog
}

type tickMsg struct{}

// NewModel creates a new TUI model
func NewModel(server StatsProvider) Model {
	return Model{
		statsPane:   viewport.New(0, 0),
		headersPane: viewport.New(0, 0),
		appLogs:     viewport.New(0, 0),
		appLogLines: []string{},
		server:      server,
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// Update handles messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			// Calculate pane sizes with better logic
			// Reserve space for titles, borders, and footer
			reservedHeight := 8 // titles + borders + footer
			availableHeight := msg.Height - reservedHeight
			
			// Ensure minimum height
			if availableHeight < 20 {
				availableHeight = 20
			}
			
			// Split: 70% for top section, 30% for bottom
			topSectionHeight := (availableHeight * 7) / 10
			bottomSectionHeight := availableHeight - topSectionHeight
			
			// Ensure minimums
			if topSectionHeight < 8 {
				topSectionHeight = 8
			}
			if bottomSectionHeight < 10 {
				bottomSectionHeight = 10
			}
			
			// Each top pane gets half the width minus padding
			topPaneWidth := (msg.Width - 6) / 2 // Leave room for borders and spacing
			if topPaneWidth < 30 {
				topPaneWidth = 30
			}

			m.statsPane = viewport.New(topPaneWidth, topSectionHeight)
			m.headersPane = viewport.New(topPaneWidth, topSectionHeight)
			m.appLogs = viewport.New(msg.Width-6, bottomSectionHeight)
			m.width = msg.Width
			m.height = msg.Height
			m.ready = true

			// Set initial content
			m.appLogs.SetContent(strings.Join(m.appLogLines, "\n"))
			m.updateStatsPane()
			m.updateHeadersPane()
		} else {
			// Update existing viewports with same logic
			reservedHeight := 8
			availableHeight := msg.Height - reservedHeight
			
			if availableHeight < 20 {
				availableHeight = 20
			}
			
			topSectionHeight := (availableHeight * 7) / 10
			bottomSectionHeight := availableHeight - topSectionHeight
			
			if topSectionHeight < 8 {
				topSectionHeight = 8
			}
			if bottomSectionHeight < 10 {
				bottomSectionHeight = 10
			}
			
			topPaneWidth := (msg.Width - 6) / 2
			if topPaneWidth < 30 {
				topPaneWidth = 30
			}

			m.statsPane.Width = topPaneWidth
			m.statsPane.Height = topSectionHeight
			m.headersPane.Width = topPaneWidth
			m.headersPane.Height = topSectionHeight
			m.appLogs.Width = msg.Width - 6
			m.appLogs.Height = bottomSectionHeight
			m.width = msg.Width
			m.height = msg.Height
			
			// Refresh content after resize
			m.updateStatsPane()
			m.updateHeadersPane()
			m.appLogs.SetContent(strings.Join(m.appLogLines, "\n"))
		}

	case tickMsg:
		// Update stats periodically
		if m.ready {
			m.updateStatsPane()
		}
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg{}
		})

	case LogMsg:
		// Add to app logs
		timestamp := msg.Time.Format("15:04:05")
		levelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		if msg.Level == "ERROR" || msg.Level == "FATAL" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("196"))
		} else if msg.Level == "WARN" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("208"))
		} else if msg.Level == "INFO" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("34"))
		} else if msg.Level == "DEBUG" {
			levelStyle = levelStyle.Foreground(lipgloss.Color("75"))
		}

		logLine := fmt.Sprintf("%s %s %s",
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(timestamp),
			levelStyle.Render(fmt.Sprintf("%-5s", msg.Level)),
			msg.Message)

		m.appLogLines = append(m.appLogLines, logLine)

		// Keep only last 1000 lines
		if len(m.appLogLines) > 1000 {
			m.appLogLines = m.appLogLines[1:]
		}

		if m.ready {
			m.appLogs.SetContent(strings.Join(m.appLogLines, "\n"))
			m.appLogs.GotoBottom()
		}

	case RequestMsg:
		// Update request data
		m.lastRequest = &msg.Log
		if m.ready {
			m.updateHeadersPane()
			m.updateStatsPane()
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.ready {
				m.appLogs.LineUp(1)
			}
		case "down", "j":
			if m.ready {
				m.appLogs.LineDown(1)
			}
		case "pgup":
			if m.ready {
				m.appLogs.HalfViewUp()
			}
		case "pgdown":
			if m.ready {
				m.appLogs.HalfViewDown()
			}
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

// updateStatsPane updates the statistics pane content
func (m *Model) updateStatsPane() {
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

	// Stats table
	b.WriteString(fmt.Sprintf("%-12s %5s %5s %6s %6s %6s %6s\n",
		"Connections", "ttl", "opn", "rt1", "rt5", "p50", "p90"))
	b.WriteString(strings.Repeat("─", 55) + "\n")

	// Stats values
	b.WriteString(fmt.Sprintf("%-12s %5d %5d %6.1f %6.1f %6.1f %6.1f\n",
		"", ttl, opn, rt1, rt5, p50, p90))

	b.WriteString("\n")

	// Legend
	b.WriteString("Legend:\n")
	b.WriteString("  ttl: Total requests\n")
	b.WriteString("  opn: Open connections\n")
	b.WriteString("  rt1: Avg response time 1m (ms)\n")
	b.WriteString("  rt5: Avg response time 5m (ms)\n")
	b.WriteString("  p50: 50th percentile (ms)\n")
	b.WriteString("  p90: 90th percentile (ms)\n")

	m.statsPane.SetContent(b.String())
}

// updateHeadersPane updates the headers pane content
func (m *Model) updateHeadersPane() {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Latest Request"))
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

	b.WriteString(fmt.Sprintf("From: %s\n",
		m.lastRequest.RemoteAddr))
		
	b.WriteString(fmt.Sprintf("Time: %s\n\n",
		m.lastRequest.Timestamp.Format("15:04:05")))

	// Request Headers (show as many as fit in the available space)
	if len(m.lastRequest.Headers) > 0 {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Request Headers:"))
		b.WriteString("\n")

		// Priority headers to show first
		priorityHeaders := []string{"User-Agent", "Content-Type", "Authorization", "Accept", "Host", "Accept-Encoding"}
		shown := make(map[string]bool)
		
		// Show priority headers first
		for _, key := range priorityHeaders {
			if value, exists := m.lastRequest.Headers[key]; exists {
				b.WriteString(fmt.Sprintf("  %s: %s\n",
					lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Render(key),
					truncateString(value, 60))) // Increased truncate length
				shown[key] = true
			}
		}
		
		// Show all remaining headers (not just up to 5)
		var otherHeaders []string
		for k := range m.lastRequest.Headers {
			if !shown[k] {
				otherHeaders = append(otherHeaders, k)
			}
		}
		sort.Strings(otherHeaders)
		
		// Calculate how many more headers we can show based on available space
		// Rough estimate: we have about (pane_height - current_lines) lines left
		currentLines := 7 + len(shown) // rough count of lines used so far
		availableLines := m.headersPane.Height - currentLines - 3 // leave some buffer
		
		maxAdditionalHeaders := availableLines
		if maxAdditionalHeaders < 0 {
			maxAdditionalHeaders = 0
		}
		
		for i, k := range otherHeaders {
			if i >= maxAdditionalHeaders {
				// Show count of remaining headers if we hit the limit
				remaining := len(otherHeaders) - i
				if remaining > 0 {
					b.WriteString(fmt.Sprintf("  %s\n", 
						lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(
							fmt.Sprintf("... and %d more headers", remaining))))
				}
				break
			}
			b.WriteString(fmt.Sprintf("  %s: %s\n",
				lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Render(k),
				truncateString(m.lastRequest.Headers[k], 60)))
		}
		b.WriteString("\n")
	}

	// Request Body (show more based on available space)
	if m.lastRequest.Body != "" {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Request Body:"))
		b.WriteString("\n")
		
		// Calculate remaining space for body
		currentLines := strings.Count(b.String(), "\n")
		availableLines := m.headersPane.Height - currentLines - 2 // leave some buffer
		
		// Estimate how much body we can show (rough: 80 chars per line)
		maxBodyChars := availableLines * 80
		if maxBodyChars < 200 {
			maxBodyChars = 200 // minimum
		}
		
		if len(m.lastRequest.Body) > maxBodyChars {
			b.WriteString(fmt.Sprintf("[%d bytes - showing first %d chars]\n", len(m.lastRequest.Body), maxBodyChars))
			bodyPreview := m.lastRequest.Body[:maxBodyChars]
			// Try to break at a reasonable point (newline or space)
			if lastNewline := strings.LastIndex(bodyPreview, "\n"); lastNewline > maxBodyChars-100 {
				bodyPreview = bodyPreview[:lastNewline]
			} else if lastSpace := strings.LastIndex(bodyPreview, " "); lastSpace > maxBodyChars-50 {
				bodyPreview = bodyPreview[:lastSpace]
			}
			b.WriteString(bodyPreview)
			b.WriteString("\n...")
		} else {
			b.WriteString(m.lastRequest.Body)
		}
		b.WriteString("\n")
	}

	m.headersPane.SetContent(b.String())
}

// truncateString truncates a string to the specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// View renders the TUI
func (m Model) View() string {
	if !m.ready {
		return "Initializing TUI..."
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1)

	// Create the top panes (stats and headers)
	leftTopPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("📊 Statistics"),
		borderStyle.
			Width(m.statsPane.Width).
			Height(m.statsPane.Height).
			Render(m.statsPane.View()),
	)

	rightTopPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("🌐 Request Details"),
		borderStyle.
			Width(m.headersPane.Width).
			Height(m.headersPane.Height).
			Render(m.headersPane.View()),
	)

	// Join top panes horizontally
	topSection := lipgloss.JoinHorizontal(lipgloss.Top, leftTopPane, rightTopPane)

	// Create bottom pane (logs)
	bottomPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("📋 Application Logs"),
		borderStyle.
			Width(m.appLogs.Width).
			Height(m.appLogs.Height).
			Render(m.appLogs.View()),
	)

	// Join sections vertically
	main := lipgloss.JoinVertical(lipgloss.Top, topSection, bottomPane)

	// Add footer
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("Press 'q' or Ctrl+C to quit • ↑/↓ or j/k to scroll logs • PgUp/PgDn for faster scrolling")

	// Final view
	finalView := lipgloss.JoinVertical(lipgloss.Top, main, footer)

	return finalView
}

// LogWriter implements io.Writer to capture zap logs for the TUI
type LogWriter struct {
	program *tea.Program
}

// NewLogWriter creates a new log writer for TUI integration
func NewLogWriter(program *tea.Program) *LogWriter {
	return &LogWriter{program: program}
}

// Write implements io.Writer
func (w *LogWriter) Write(p []byte) (n int, err error) {
	// Parse log level and message from zap output
	line := string(p)
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) >= 3 {
		level := strings.TrimSpace(parts[1])
		message := strings.TrimSpace(parts[2])
		if len(parts) > 3 {
			message += " " + strings.TrimSpace(parts[3])
		}

		w.program.Send(LogMsg{
			Level:   level,
			Message: message,
			Time:    time.Now(),
		})
	}
	return len(p), nil
}

// CreateRequestMsg creates a request message for the TUI
func CreateRequestMsg(log model.RequestLog) tea.Msg {
	return RequestMsg{Log: log}
}