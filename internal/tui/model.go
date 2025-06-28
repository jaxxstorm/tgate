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

// LogMessage is an alias for LogMsg to match the logging package
type LogMessage = LogMsg

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
			// Calculate pane sizes with proper margins
			// Reserve space for titles (1 line each) + borders (2 lines each) + footer (1 line)
			reservedHeight := 8 // 3 titles + 6 border lines + 1 footer
			availableHeight := msg.Height - reservedHeight
			
			// Split remaining height: 60% for top section, 40% for bottom
			topSectionHeight := (availableHeight * 6) / 10
			bottomSectionHeight := availableHeight - topSectionHeight
			
			// Each top pane gets half the width
			topPaneWidth := (msg.Width - 2) / 2 // -2 for side margins
			
			// Create viewports with safe minimums
			if topSectionHeight < 5 {
				topSectionHeight = 5
			}
			if bottomSectionHeight < 5 {
				bottomSectionHeight = 5
			}
			if topPaneWidth < 20 {
				topPaneWidth = 20
			}

			m.statsPane = viewport.New(topPaneWidth-2, topSectionHeight)
			m.headersPane = viewport.New(topPaneWidth-2, topSectionHeight)
			m.appLogs = viewport.New(msg.Width-4, bottomSectionHeight)
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
			topSectionHeight := (availableHeight * 6) / 10
			bottomSectionHeight := availableHeight - topSectionHeight
			topPaneWidth := (msg.Width - 2) / 2
			
			// Safe minimums
			if topSectionHeight < 5 {
				topSectionHeight = 5
			}
			if bottomSectionHeight < 5 {
				bottomSectionHeight = 5
			}
			if topPaneWidth < 20 {
				topPaneWidth = 20
			}

			m.statsPane.Width = topPaneWidth - 2
			m.statsPane.Height = topSectionHeight
			m.headersPane.Width = topPaneWidth - 2
			m.headersPane.Height = topSectionHeight
			m.appLogs.Width = msg.Width - 4
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
		if msg.Level == "ERROR" {
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
			levelStyle.Render(msg.Level),
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

// updateHeadersPane updates the headers pane content
func (m *Model) updateHeadersPane() {
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
		BorderForeground(lipgloss.Color("240"))

	// Create the top panes (stats and headers) with exact sizing
	leftTopPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("📊 Statistics"),
		borderStyle.
			Width(m.statsPane.Width+1).
			Height(m.statsPane.Height+1).
			Render(m.statsPane.View()),
	)

	rightTopPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("🌐 Request Headers"),
		borderStyle.
			Width(m.headersPane.Width+1).
			Height(m.headersPane.Height+1).
			Render(m.headersPane.View()),
	)

	// Join top panes horizontally with no gap
	topSection := lipgloss.JoinHorizontal(lipgloss.Top, leftTopPane, rightTopPane)

	// Create bottom pane (logs) with exact sizing
	bottomPane := lipgloss.JoinVertical(lipgloss.Top,
		titleStyle.Render("📋 Application Logs"),
		borderStyle.
			Width(m.appLogs.Width+1).
			Height(m.appLogs.Height+1).
			Render(m.appLogs.View()),
	)

	// Join sections vertically with no gap
	main := lipgloss.JoinVertical(lipgloss.Top, topSection, bottomPane)

	// Add footer
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("Press 'q' or Ctrl+C to quit")

	// Ensure the final output fits within terminal bounds
	finalView := lipgloss.JoinVertical(lipgloss.Top, main, footer)
	
	// Truncate if necessary to prevent overflow
	if m.height > 0 {
		lines := strings.Split(finalView, "\n")
		if len(lines) > m.height {
			lines = lines[:m.height-1]
			finalView = strings.Join(lines, "\n")
		}
	}

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