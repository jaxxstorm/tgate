// internal/logging/formatters.go
package logging

import (
	"fmt"
	"time"

	"go.uber.org/zap/zapcore"
)

// RequestLogFormatter formats HTTP request logs
type RequestLogFormatter struct {
	includeBody bool
	maxBodySize int
}

// NewRequestLogFormatter creates a new request log formatter
func NewRequestLogFormatter(includeBody bool, maxBodySize int) *RequestLogFormatter {
	if maxBodySize <= 0 {
		maxBodySize = 1000 // Default max body size
	}
	return &RequestLogFormatter{
		includeBody: includeBody,
		maxBodySize: maxBodySize,
	}
}

// FormatRequest formats an HTTP request for logging
func (f *RequestLogFormatter) FormatRequest(method, url, remoteAddr, userAgent string, contentLength int64, body string) string {
	timestamp := time.Now().Format("15:04:05")
	
	logMsg := fmt.Sprintf("[%s] %s %s from %s", timestamp, method, url, remoteAddr)
	
	if userAgent != "" {
		logMsg += fmt.Sprintf(" (UA: %s)", userAgent)
	}
	
	if contentLength > 0 {
		logMsg += fmt.Sprintf(" [%d bytes]", contentLength)
	}
	
	if f.includeBody && body != "" {
		if len(body) > f.maxBodySize {
			logMsg += fmt.Sprintf("\nBody: %s... [truncated at %d chars]", body[:f.maxBodySize], f.maxBodySize)
		} else {
			logMsg += fmt.Sprintf("\nBody: %s", body)
		}
	}
	
	return logMsg
}

// FormatResponse formats an HTTP response for logging
func (f *RequestLogFormatter) FormatResponse(statusCode int, size int64, duration time.Duration) string {
	statusIcon := "✓"
	if statusCode >= 400 {
		statusIcon = "✗"
	} else if statusCode >= 300 {
		statusIcon = "⚠"
	}
	
	return fmt.Sprintf("%s %d • %s • %d bytes",
		statusIcon,
		statusCode,
		duration.Round(time.Millisecond),
		size)
}

// ConsoleEncoderConfig returns a console encoder configuration
func ConsoleEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

// JSONEncoderConfig returns a JSON encoder configuration
func JSONEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

// TimeEncoder encodes time in a human-readable format
func TimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("15:04:05.000"))
}

// CustomLevelEncoder encodes log levels with custom formatting
func CustomLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch level {
	case zapcore.DebugLevel:
		enc.AppendString("DBG")
	case zapcore.InfoLevel:
		enc.AppendString("INF")
	case zapcore.WarnLevel:
		enc.AppendString("WRN")
	case zapcore.ErrorLevel:
		enc.AppendString("ERR")
	case zapcore.FatalLevel:
		enc.AppendString("FTL")
	default:
		enc.AppendString("UNK")
	}
}

// ColorLevelEncoder encodes log levels with colors for console output
func ColorLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch level {
	case zapcore.DebugLevel:
		enc.AppendString("\033[36mDEBUG\033[0m") // Cyan
	case zapcore.InfoLevel:
		enc.AppendString("\033[32mINFO\033[0m")  // Green
	case zapcore.WarnLevel:
		enc.AppendString("\033[33mWARN\033[0m")  // Yellow
	case zapcore.ErrorLevel:
		enc.AppendString("\033[31mERROR\033[0m") // Red
	case zapcore.FatalLevel:
		enc.AppendString("\033[35mFATAL\033[0m") // Magenta
	default:
		enc.AppendString("UNKNOWN")
	}
}