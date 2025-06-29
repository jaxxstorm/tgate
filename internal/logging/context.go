// internal/logging/context.go
package logging

import (
	"context"

	"go.uber.org/zap"
)

// contextKey is a private type for context keys to avoid collisions
type contextKey string

const (
	loggerKey    contextKey = "logger"
	requestIDKey contextKey = "request_id"
	traceIDKey   contextKey = "trace_id"
)

// WithLogger adds a logger to the context
func WithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext extracts a logger from the context
func FromContext(ctx context.Context) *zap.Logger {
	if logger, ok := ctx.Value(loggerKey).(*zap.Logger); ok {
		return logger
	}
	// Return a no-op logger if none found
	return zap.NewNop()
}

// WithRequestID adds a request ID to the context
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// GetRequestID extracts a request ID from the context
func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDKey).(string); ok {
		return requestID
	}
	return ""
}

// WithTraceID adds a trace ID to the context
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// GetTraceID extracts a trace ID from the context
func GetTraceID(ctx context.Context) string {
	if traceID, ok := ctx.Value(traceIDKey).(string); ok {
		return traceID
	}
	return ""
}

// WithFields adds structured fields to a logger from context
func WithFields(ctx context.Context, fields ...zap.Field) *zap.Logger {
	logger := FromContext(ctx)
	
	// Add request ID if available
	if requestID := GetRequestID(ctx); requestID != "" {
		fields = append(fields, zap.String("request_id", requestID))
	}
	
	// Add trace ID if available
	if traceID := GetTraceID(ctx); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	
	return logger.With(fields...)
}

// LoggerMiddleware is a helper for HTTP middleware that adds logging context
type LoggerMiddleware struct {
	logger *zap.Logger
}

// NewLoggerMiddleware creates a new logger middleware
func NewLoggerMiddleware(logger *zap.Logger) *LoggerMiddleware {
	return &LoggerMiddleware{logger: logger}
}

// WithContext adds the logger to a context for HTTP requests
func (lm *LoggerMiddleware) WithContext(ctx context.Context, requestID string) context.Context {
	ctx = WithLogger(ctx, lm.logger)
	if requestID != "" {
		ctx = WithRequestID(ctx, requestID)
	}
	return ctx
}

// Context-aware logging functions (renamed to avoid conflicts with field helpers)

// DebugCtx logs a debug message with context
func DebugCtx(ctx context.Context, msg string, fields ...zap.Field) {
	WithFields(ctx, fields...).Debug(msg)
}

// InfoCtx logs an info message with context
func InfoCtx(ctx context.Context, msg string, fields ...zap.Field) {
	WithFields(ctx, fields...).Info(msg)
}

// WarnCtx logs a warning message with context
func WarnCtx(ctx context.Context, msg string, fields ...zap.Field) {
	WithFields(ctx, fields...).Warn(msg)
}

// ErrorCtx logs an error message with context
func ErrorCtx(ctx context.Context, msg string, fields ...zap.Field) {
	WithFields(ctx, fields...).Error(msg)
}

// FatalCtx logs a fatal message with context and exits
func FatalCtx(ctx context.Context, msg string, fields ...zap.Field) {
	WithFields(ctx, fields...).Fatal(msg)
}