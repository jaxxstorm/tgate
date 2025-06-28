package logging

import (
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config holds logging configuration
type Config struct {
	Verbose    bool
	JSON       bool
	LogFile    string
	TUIWriter  io.Writer // Optional TUI writer for log redirection
}

// SetupLogger creates and configures a zap logger based on the provided configuration
func SetupLogger(config Config) (*zap.Logger, error) {
	var zapConfig zap.Config

	if config.JSON {
		zapConfig = zap.NewProductionConfig()
	} else {
		zapConfig = zap.NewDevelopmentConfig()
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	// Set log level
	if config.Verbose {
		zapConfig.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	} else {
		zapConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	// Configure output paths
	if config.LogFile != "" {
		zapConfig.OutputPaths = []string{config.LogFile, "stdout"}
	}

	logger, err := zapConfig.Build()
	if err != nil {
		return nil, err
	}

	// If we have a TUI writer, redirect logs to it
	if config.TUIWriter != nil {
		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(zapConfig.EncoderConfig),
			zapcore.AddSync(config.TUIWriter),
			zapConfig.Level,
		)
		logger = zap.New(core)
	}

	return logger, nil
}

// SetupLoggerWithTUI creates a logger that sends output to both console and TUI
func SetupLoggerWithTUI(config Config, program *tea.Program) (*zap.Logger, error) {
	tuiWriter := NewTUIWriter(program)
	config.TUIWriter = tuiWriter
	return SetupLogger(config)
}

// TUIWriter implements io.Writer to capture zap logs for the TUI
type TUIWriter struct {
	program *tea.Program
}

// NewTUIWriter creates a new TUI writer
func NewTUIWriter(program *tea.Program) *TUIWriter {
	return &TUIWriter{program: program}
}

// Write implements io.Writer interface
func (w *TUIWriter) Write(p []byte) (n int, err error) {
	// Parse log level and message from zap output
	line := string(p)
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) >= 3 {
		level := strings.TrimSpace(parts[1])
		message := strings.TrimSpace(parts[2])
		if len(parts) > 3 {
			message += " " + strings.TrimSpace(parts[3])
		}

		// Create log message for TUI
		logMsg := LogMessage{
			Level:   level,
			Message: message,
			Time:    time.Now(),
		}

		w.program.Send(logMsg)
	}
	return len(p), nil
}

// LogMessage represents a log message for the TUI
type LogMessage struct {
	Level   string
	Message string
	Time    time.Time
}

// MultiWriter combines multiple writers for logging to multiple destinations
type MultiWriter struct {
	writers []io.Writer
}

// NewMultiWriter creates a new multi-writer that writes to all provided writers
func NewMultiWriter(writers ...io.Writer) *MultiWriter {
	return &MultiWriter{writers: writers}
}

// Write implements io.Writer interface
func (mw *MultiWriter) Write(p []byte) (n int, err error) {
	for _, writer := range mw.writers {
		if _, err := writer.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// AddWriter adds a new writer to the multi-writer
func (mw *MultiWriter) AddWriter(writer io.Writer) {
	mw.writers = append(mw.writers, writer)
}

// GetSugaredLogger creates a sugared logger from a regular logger
func GetSugaredLogger(logger *zap.Logger) *zap.SugaredLogger {
	return logger.Sugar()
}