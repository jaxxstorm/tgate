package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/jaxxstorm/tgate/internal/logging"
)

// TUIOnlyLogger sends all log messages to TUI instead of console
type TUIOnlyLogger struct {
	program   *tea.Program
	formatter *logging.RequestLogFormatter
}

// NewTUIOnlyLogger creates a new TUIOnlyLogger instance
func NewTUIOnlyLogger(program *tea.Program) *TUIOnlyLogger {
	return &TUIOnlyLogger{
		program:   program,
		formatter: logging.NewRequestLogFormatter(false, 500), // TUI-friendly formatting
	}
}

// Standard logging methods with structured message support
func (l *TUIOnlyLogger) Info(msg string, fields ...zap.Field) {
	l.logWithFields("INFO", msg, fields...)
}

func (l *TUIOnlyLogger) Error(msg string, fields ...zap.Field) {
	l.logWithFields("ERROR", msg, fields...)
}

func (l *TUIOnlyLogger) Warn(msg string, fields ...zap.Field) {
	l.logWithFields("WARN", msg, fields...)
}

func (l *TUIOnlyLogger) Debug(msg string, fields ...zap.Field) {
	l.logWithFields("DEBUG", msg, fields...)
}

func (l *TUIOnlyLogger) Fatal(msg string, fields ...zap.Field) {
	l.logWithFields("FATAL", msg, fields...)
}

// Legacy printf-style methods for compatibility
func (l *TUIOnlyLogger) Infof(format string, args ...interface{}) {
	l.program.Send(LogMsg{
		Level:   "INFO",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Errorf(format string, args ...interface{}) {
	l.program.Send(LogMsg{
		Level:   "ERROR",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Warnf(format string, args ...interface{}) {
	l.program.Send(LogMsg{
		Level:   "WARN",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Debugf(format string, args ...interface{}) {
	l.program.Send(LogMsg{
		Level:   "DEBUG",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Fatalf(format string, args ...interface{}) {
	l.program.Send(LogMsg{
		Level:   "FATAL",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

// logWithFields formats structured log fields into a TUI-friendly message
func (l *TUIOnlyLogger) logWithFields(level, msg string, fields ...zap.Field) {
	message := msg

	// Convert zap fields to key=value pairs for TUI display
	if len(fields) > 0 {
		var parts []string
		for _, field := range fields {
			switch field.Type {
			case zapcore.StringType:
				parts = append(parts, fmt.Sprintf("%s=%s", field.Key, field.String))
			case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type:
				parts = append(parts, fmt.Sprintf("%s=%d", field.Key, field.Integer))
			case zapcore.BoolType:
				value := "false"
				if field.Integer == 1 {
					value = "true"
				}
				parts = append(parts, fmt.Sprintf("%s=%s", field.Key, value))
			case zapcore.DurationType:
				parts = append(parts, fmt.Sprintf("%s=%s", field.Key, time.Duration(field.Integer).String()))
			default:
				// For other types, use the field's String method or convert appropriately
				parts = append(parts, fmt.Sprintf("%s=%v", field.Key, field.Interface))
			}
		}
		if len(parts) > 0 {
			message = fmt.Sprintf("%s %s", msg, strings.Join(parts, " "))
		}
	}

	l.program.Send(LogMsg{
		Level:   level,
		Message: message,
		Time:    time.Now(),
	})
}

// CreateTUIZapLogger creates a zap logger that sends output to the TUI
func CreateTUIZapLogger(program *tea.Program) *zap.Logger {
	// Create a TUIOnlyLogger instance
	tuiLogger := NewTUIOnlyLogger(program)
	
	// Create a custom core that routes directly to TUIOnlyLogger
	tuiCore := &tuiZapCore{tuiLogger: tuiLogger}

	// Create logger with custom core
	logger := zap.New(tuiCore)

	return logger
}

// tuiZapWriter implements zapcore.WriteSyncer for sending zap logs to TUI
type tuiZapWriter struct {
	program *tea.Program
}

func (w *tuiZapWriter) Write(p []byte) (n int, err error) {
	// Parse the zap log line and send to TUI
	line := strings.TrimSpace(string(p))

	// Skip empty lines
	if line == "" {
		return len(p), nil
	}

	level := "INFO"
	message := line

	// Try to parse zap console format: TIMESTAMP\tLEVEL\tMESSAGE[\tJSON_FIELDS]
	if strings.Contains(line, "\t") {
		parts := strings.Split(line, "\t")

		if len(parts) >= 3 {
			// Extract level (second part)
			level = strings.ToUpper(strings.TrimSpace(parts[1]))
			// Extract message (third part)
			baseMessage := strings.TrimSpace(parts[2])

			// For TUI, keep it simple - just use the base message and a few key fields
			if len(parts) >= 4 {
				jsonFields := strings.TrimSpace(parts[3])
				// Only extract the most essential fields to keep messages short
				essential := extractEssentialFields(jsonFields)
				if essential != "" {
					message = fmt.Sprintf("%s %s", baseMessage, essential)
				} else {
					message = baseMessage
				}
			} else {
				message = baseMessage
			}
		}
	} else {
		// Handle non-tab formatted logs - these should be properly formatted
		// Since our CreateTUIZapLogger uses ConsoleEncoder, all logs should come through
		// in tab-separated format. If we reach here, something might be wrong with the encoder.

		// Skip very short or empty lines to reduce noise
		if len(strings.TrimSpace(line)) < 3 {
			return len(p), nil
		}

		// Simple level detection for fallback
		upperLine := strings.ToUpper(line)
		if strings.Contains(upperLine, "ERROR") || strings.Contains(upperLine, "FAIL") {
			level = "ERROR"
		} else if strings.Contains(upperLine, "WARN") {
			level = "WARN"
		} else if strings.Contains(upperLine, "DEBUG") {
			level = "DEBUG"
		} else if strings.Contains(upperLine, "FATAL") {
			level = "FATAL"
		} else {
			level = "INFO"
		}

		message = strings.TrimSpace(line)
	}

	// Ensure message isn't too long for TUI display
	if len(message) > 120 {
		message = message[:117] + "..."
	}

	w.program.Send(LogMsg{
		Level:   level,
		Message: message,
		Time:    time.Now(),
	})

	return len(p), nil
}

func (w *tuiZapWriter) Sync() error {
	return nil
}

// CreateTUIOnlyZapLogger creates a zap logger that routes to TUI via TUIOnlyLogger
func CreateTUIOnlyZapLogger(tuiLogger *TUIOnlyLogger) *zap.Logger {
	// Create a custom writer that sends to TUI via the TUIOnlyLogger
	tuiWriter := &tuiOnlyZapWriter{tuiLogger: tuiLogger}

	// Use the standard console encoder config from logging package for consistency
	encoderConfig := logging.ConsoleEncoderConfig()

	// Create logger with custom writer and consistent encoding
	logger := zap.New(
		zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(tuiWriter),
			zapcore.InfoLevel,
		),
	)

	return logger
}

// tuiOnlyZapWriter implements zapcore.WriteSyncer for sending zap logs to TUIOnlyLogger
type tuiOnlyZapWriter struct {
	tuiLogger *TUIOnlyLogger
}

func (w *tuiOnlyZapWriter) Write(p []byte) (n int, err error) {
	// Parse the zap log line and send to TUI via TUIOnlyLogger
	line := strings.TrimSpace(string(p))

	// Skip empty lines
	if line == "" {
		return len(p), nil
	}

	// Simple parsing - just extract level and message
	parts := strings.Split(line, "\t")

	if len(parts) >= 3 {
		level := strings.ToUpper(strings.TrimSpace(parts[1]))
		message := strings.TrimSpace(parts[2])

		// If there are more parts, include them
		if len(parts) > 3 {
			message += " " + strings.Join(parts[3:], " ")
		}

		// Route to appropriate TUIOnlyLogger method based on level
		switch level {
		case "DEBUG":
			w.tuiLogger.Debugf("%s", message)
		case "INFO":
			w.tuiLogger.Infof("%s", message)
		case "WARN":
			w.tuiLogger.Warnf("%s", message)
		case "ERROR":
			w.tuiLogger.Errorf("%s", message)
		case "FATAL":
			w.tuiLogger.Fatalf("%s", message)
		default:
			w.tuiLogger.Infof("%s", message)
		}
	} else {
		// Fallback for malformed log lines
		w.tuiLogger.Infof("%s", line)
	}

	return len(p), nil
}

func (w *tuiOnlyZapWriter) Sync() error {
	return nil
}

// Convenience methods for common logging scenarios using standard logging fields

// LogServerEvent logs a server lifecycle event using standard messages
func (l *TUIOnlyLogger) LogServerEvent(msg string, fields ...zap.Field) {
	l.Info(msg, fields...)
}

// LogTailscaleEvent logs a Tailscale-related event
func (l *TUIOnlyLogger) LogTailscaleEvent(msg string, fields ...zap.Field) {
	l.Info(msg, fields...)
}

// LogProxyEvent logs a proxy server event
func (l *TUIOnlyLogger) LogProxyEvent(msg string, fields ...zap.Field) {
	l.Info(msg, fields...)
}

// LogUIEvent logs a UI server event
func (l *TUIOnlyLogger) LogUIEvent(msg string, fields ...zap.Field) {
	l.Info(msg, fields...)
}

// LogSetupEvent logs a setup or configuration event
func (l *TUIOnlyLogger) LogSetupEvent(msg string, fields ...zap.Field) {
	l.Info(msg, fields...)
}

// LogErrorEvent logs an error event
func (l *TUIOnlyLogger) LogErrorEvent(msg string, fields ...zap.Field) {
	l.Error(msg, fields...)
}

// Common field helpers that use the standard logging fields
func (l *TUIOnlyLogger) ServerStarting() {
	l.Info(logging.MsgServerStarting)
}

func (l *TUIOnlyLogger) ServerStarted() {
	l.Info(logging.MsgServerStarted)
}

func (l *TUIOnlyLogger) ServerStopping() {
	l.Info(logging.MsgServerStopping)
}

func (l *TUIOnlyLogger) ProxyStarting(port int) {
	l.Info(logging.MsgProxyStarting, logging.ProxyPort(port))
}

func (l *TUIOnlyLogger) ProxyStarted(port int) {
	l.Info(logging.MsgProxyStarted, logging.ProxyPort(port))
}

func (l *TUIOnlyLogger) UIStarting(port int) {
	l.Info(logging.MsgUIStarting, logging.UIPort(port))
}

func (l *TUIOnlyLogger) UIStarted(port int) {
	l.Info(logging.MsgUIStarted, logging.UIPort(port))
}

func (l *TUIOnlyLogger) TailscaleServeSetup(enableFunnel bool, useHTTPS bool) {
	l.Info(logging.MsgTailscaleServeSetup,
		logging.FunnelEnabled(enableFunnel),
		logging.HTTPSEnabled(useHTTPS))
}

func (l *TUIOnlyLogger) TailscaleServeSuccess() {
	l.Info(logging.MsgTailscaleServeSuccess)
}

// parseIncomingRequest creates a detailed message from zap JSON fields using logging formatter
func parseIncomingRequest(jsonFields string) string {
	// Parse all available fields
	method := extractJSONField(jsonFields, "method")
	url := extractJSONField(jsonFields, "url")
	remoteAddr := extractJSONField(jsonFields, "remote_addr")
	userAgent := extractJSONField(jsonFields, "user_agent")
	contentLength := extractJSONField(jsonFields, "content_length")
	requestID := extractJSONField(jsonFields, "request_id")

	// Use the logging formatter for consistent formatting
	var clInt int64
	if contentLength != "" {
		fmt.Sscanf(contentLength, "%d", &clInt)
	}

	// Clean up user agent
	cleanUserAgent := strings.Trim(userAgent, `"`)
	if cleanUserAgent == "" || cleanUserAgent == `""` {
		cleanUserAgent = ""
	}

	// Build detailed message with consistent formatting
	var parts []string

	if method != "" && url != "" {
		parts = append(parts, fmt.Sprintf("method=%s url=%s", method, url))
	}

	if remoteAddr != "" {
		parts = append(parts, fmt.Sprintf("remote_addr=%s", remoteAddr))
	}

	if cleanUserAgent != "" {
		parts = append(parts, fmt.Sprintf("user_agent=%q", cleanUserAgent))
	}

	if clInt > 0 {
		parts = append(parts, fmt.Sprintf("content_length=%d", clInt))
	}

	if requestID != "" {
		requestID = strings.Trim(requestID, `"`)
		parts = append(parts, fmt.Sprintf("request_id=%s", requestID))
	}

	if len(parts) > 0 {
		return fmt.Sprintf("%s %s", logging.MsgIncomingRequest, strings.Join(parts, " "))
	}

	return logging.MsgIncomingRequest
}

// parseResponseSent creates a detailed message from zap JSON fields using logging constants
func parseResponseSent(jsonFields string) string {
	// Parse all available fields
	statusCode := extractJSONField(jsonFields, "status_code")
	responseSize := extractJSONField(jsonFields, "response_size")
	duration := extractJSONField(jsonFields, "duration")
	requestID := extractJSONField(jsonFields, "request_id")

	// Build detailed message
	var parts []string

	if statusCode != "" {
		parts = append(parts, fmt.Sprintf("status_code=%s", statusCode))
	}

	if responseSize != "" {
		parts = append(parts, fmt.Sprintf("response_size=%s", responseSize))
	}

	if duration != "" {
		duration = strings.Trim(duration, `"`)
		parts = append(parts, fmt.Sprintf("duration=%s", duration))
	}

	if requestID != "" {
		requestID = strings.Trim(requestID, `"`)
		parts = append(parts, fmt.Sprintf("request_id=%s", requestID))
	}

	if len(parts) > 0 {
		return fmt.Sprintf("%s %s", logging.MsgResponseSent, strings.Join(parts, " "))
	}

	return logging.MsgResponseSent
}

// extractJSONField extracts a field value from a JSON-like string
func extractJSONField(jsonStr, fieldName string) string {
	// Simple extraction for {"field": "value"} format
	re := strings.NewReplacer(
		`"`, `"`,
		` `, ` `,
	)
	cleanStr := re.Replace(jsonStr)

	// Find the field
	startPattern := fmt.Sprintf(`"%s": `, fieldName)
	startIdx := strings.Index(cleanStr, startPattern)
	if startIdx == -1 {
		return ""
	}

	startIdx += len(startPattern)
	remaining := cleanStr[startIdx:]

	// Find the end of the value
	var endIdx int
	if strings.HasPrefix(remaining, `"`) {
		// String value - find closing quote
		endIdx := strings.Index(remaining[1:], `"`)
		if endIdx != -1 {
			return remaining[1 : endIdx+1] // Extract without quotes
		}
	} else {
		// Numeric value - find comma or end
		endIdx = strings.IndexAny(remaining, ",}")
		if endIdx == -1 {
			endIdx = len(remaining)
		}
		return strings.TrimSpace(remaining[:endIdx])
	}

	return ""
}

// parseStructuredFields extracts key=value pairs from JSON fields for general log messages
func parseStructuredFields(jsonFields string) string {
	if jsonFields == "" || jsonFields == "{}" {
		return ""
	}

	var parts []string

	// Common fields to extract and format nicely - prioritize the most important ones
	priorityFields := []string{
		"component", "status", "operation", "error",
		"proxy_port", "ui_port", "tailscale_port", "serve_port",
		"url", "mount_path", "dns_name",
	}

	// Secondary fields
	secondaryFields := []string{
		"funnel_enabled", "https_enabled", "ui_enabled", "tui_enabled",
		"port", "target_port", "server_mode", "bind_address", "local_port",
		"hostname", "auth_key_provided", "tailscale_mode",
	}

	// Extract priority fields first
	for _, fieldName := range priorityFields {
		if value := extractJSONField(jsonFields, fieldName); value != "" {
			value = cleanFieldValue(fieldName, value)
			parts = append(parts, fmt.Sprintf("%s=%s", fieldName, value))
		}
	}

	// Add secondary fields if we have room (limit total message length)
	currentLength := len(strings.Join(parts, " "))
	if currentLength < 80 { // Leave room for secondary fields
		for _, fieldName := range secondaryFields {
			if value := extractJSONField(jsonFields, fieldName); value != "" {
				value = cleanFieldValue(fieldName, value)
				newPart := fmt.Sprintf("%s=%s", fieldName, value)
				if currentLength+len(newPart)+1 < 120 { // Keep total under 120 chars
					parts = append(parts, newPart)
					currentLength += len(newPart) + 1
				} else {
					break // Stop adding fields if getting too long
				}
			}
		}
	}

	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}

	return ""
}

// cleanFieldValue cleans up field values for display
func cleanFieldValue(fieldName, value string) string {
	// Clean up boolean values
	if value == "1" && (strings.Contains(fieldName, "enabled") || strings.Contains(fieldName, "provided")) {
		return "true"
	} else if value == "0" && (strings.Contains(fieldName, "enabled") || strings.Contains(fieldName, "provided")) {
		return "false"
	}

	// Clean up quoted strings
	value = strings.Trim(value, `"`)

	// Truncate very long URLs
	if fieldName == "url" && len(value) > 40 {
		return value[:37] + "..."
	}

	return value
}

// extractEssentialFields extracts only the most important fields for TUI display
func extractEssentialFields(jsonFields string) string {
	if jsonFields == "" || jsonFields == "{}" {
		return ""
	}

	var parts []string

	// Only extract the most essential fields to keep messages concise
	essentialFields := []string{
		"component", "port", "proxy_port", "serve_port", "status", "error",
		"method", "path", "remote_addr", "user_agent", "status_code", "duration", "response_size",
	}

	for _, fieldName := range essentialFields {
		if value := extractJSONField(jsonFields, fieldName); value != "" {
			value = strings.Trim(value, `"`)
			// Keep it very short
			if fieldName == "component" {
				parts = append(parts, value) // Just the component name without key=
			} else {
				parts = append(parts, fmt.Sprintf("%s=%s", fieldName, value))
			}
		}
	}

	// Limit to first 3 fields to keep messages short
	if len(parts) > 3 {
		parts = parts[:3]
	}

	return strings.Join(parts, " ")
}

// CreateSimpleTUIZapLogger creates a zap logger that routes directly to TUIOnlyLogger without parsing
func CreateSimpleTUIZapLogger(tuiLogger *TUIOnlyLogger) *zap.Logger {
	// Create a custom writer that directly routes to TUIOnlyLogger
	tuiWriter := &simpleTUIZapWriter{tuiLogger: tuiLogger}

	// Use a simple text encoder
	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.TimeKey = ""   // Remove timestamp since TUI adds it
	encoderConfig.LevelKey = ""  // Remove level since TUI adds it
	encoderConfig.CallerKey = "" // Remove caller info
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
	encoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")

	// Create logger with custom writer
	logger := zap.New(
		zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(tuiWriter),
			zapcore.InfoLevel,
		),
	)

	return logger
}

// simpleTUIZapWriter directly forwards to TUIOnlyLogger without any parsing
type simpleTUIZapWriter struct {
	tuiLogger *TUIOnlyLogger
}

func (w *simpleTUIZapWriter) Write(p []byte) (n int, err error) {
	// Just send the message directly to TUI without any parsing
	message := strings.TrimSpace(string(p))

	// Skip empty messages
	if message == "" {
		return len(p), nil
	}

	// Just use Info level for everything to keep it simple
	w.tuiLogger.Infof("%s", message)

	return len(p), nil
}

func (w *simpleTUIZapWriter) Sync() error {
	return nil
}

// tuiZapCore implements zapcore.Core for direct TUIOnlyLogger integration
type tuiZapCore struct {
	tuiLogger *TUIOnlyLogger
	level     zapcore.Level
}

func (c *tuiZapCore) Enabled(level zapcore.Level) bool {
	return level >= zapcore.InfoLevel // Always show INFO and above
}

func (c *tuiZapCore) With(fields []zapcore.Field) zapcore.Core {
	// For simplicity, return the same core
	return c
}

func (c *tuiZapCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *tuiZapCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Convert zapcore fields to zap fields
	zapFields := make([]zap.Field, len(fields))
	for i, field := range fields {
		zapFields[i] = zap.Field(field)
	}

	// Route to appropriate TUIOnlyLogger method based on level
	switch entry.Level {
	case zapcore.DebugLevel:
		c.tuiLogger.Debug(entry.Message, zapFields...)
	case zapcore.InfoLevel:
		c.tuiLogger.Info(entry.Message, zapFields...)
	case zapcore.WarnLevel:
		c.tuiLogger.Warn(entry.Message, zapFields...)
	case zapcore.ErrorLevel:
		c.tuiLogger.Error(entry.Message, zapFields...)
	case zapcore.FatalLevel:
		c.tuiLogger.Fatal(entry.Message, zapFields...)
	default:
		c.tuiLogger.Info(entry.Message, zapFields...)
	}

	return nil
}

func (c *tuiZapCore) Sync() error {
	return nil
}
