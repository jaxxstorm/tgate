package logging

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Level represents log levels
type Level int

const (
	// DebugLevel logs are typically voluminous, and are usually disabled in production.
	DebugLevel Level = iota
	// InfoLevel is the default logging priority.
	InfoLevel
	// WarnLevel logs are more important than Info, but don't need individual human review.
	WarnLevel
	// ErrorLevel logs are high-priority. If an application is running smoothly,
	// it shouldn't generate any error-level logs.
	ErrorLevel
	// FatalLevel logs a message, then calls os.Exit(1).
	FatalLevel
)

// String returns a string representation of the log level
func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	case FatalLevel:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// ToZapLevel converts our Level to zapcore.Level
func (l Level) ToZapLevel() zapcore.Level {
	switch l {
	case DebugLevel:
		return zapcore.DebugLevel
	case InfoLevel:
		return zapcore.InfoLevel
	case WarnLevel:
		return zapcore.WarnLevel
	case ErrorLevel:
		return zapcore.ErrorLevel
	case FatalLevel:
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// FromZapLevel converts zapcore.Level to our Level
func FromZapLevel(level zapcore.Level) Level {
	switch level {
	case zapcore.DebugLevel:
		return DebugLevel
	case zapcore.InfoLevel:
		return InfoLevel
	case zapcore.WarnLevel:
		return WarnLevel
	case zapcore.ErrorLevel:
		return ErrorLevel
	case zapcore.FatalLevel:
		return FatalLevel
	default:
		return InfoLevel
	}
}

// SetLogLevel dynamically changes the log level of a logger
func SetLogLevel(logger *zap.Logger, level Level) *zap.Logger {
	atomicLevel := zap.NewAtomicLevelAt(level.ToZapLevel())
	
	// Create a new logger with the updated level
	newCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()),
		zapcore.AddSync(zapcore.Lock(zapcore.AddSync(zapcore.NewMultiWriteSyncer()))),
		atomicLevel,
	)
	
	return zap.New(newCore)
}

// IsEnabled checks if a log level is enabled for the given logger
func IsEnabled(logger *zap.Logger, level Level) bool {
	return logger.Core().Enabled(level.ToZapLevel())
}