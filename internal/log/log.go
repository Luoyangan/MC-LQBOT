// Package log provides the framework logger implementation.
// It wraps zerolog and satisfies the contract.Logger interface.
// Console output uses zerolog.ConsoleWriter for colored, human-readable logs.
package log

import (
	"os"
	"strings"
	"sync"

	"github.com/Luoyangan/LQBOT/internal/types"
	"github.com/rs/zerolog"
)

// LogDBWriter is the interface for writing log entries to a database.
// Implementations should handle their own connection pooling and error handling
// (log DB writes should never block or crash the application).
type LogDBWriter interface {
	// SaveLog writes a log entry to the database.
	// level: log level string (info, warn, error, debug, trace)
	// message: log message text
	// fields: optional key-value context
	// source: module/component name that produced the log
	SaveLog(level, message string, fields map[string]interface{}, source string) error

	// SaveLogWithContext writes a log entry with structured event context.
	// The lc parameter carries optional event metadata (EventType, ChannelID, GuildID, GroupID, AuthorID)
	// that gets stored as separate indexed columns for efficient querying.
	// Implementations that don't support structured context should ignore the lc parameter
	// and delegate to SaveLog.
	SaveLogWithContext(level, message string, fields map[string]interface{}, source string, lc types.LogEventContext) error
}

// LogEventContext is the structured event metadata for DB log entries.
// Defined in types package for sharing across packages.

// levelWeights maps log level strings to numeric values for comparison.
// Higher number = more severe. Used to filter DB writes by minimum level.
var levelWeights = map[string]int{
	"trace": 0,
	"debug": 1,
	"info":  2,
	"warn":  3,
	"error": 4,
}

// Logger implements contract.Logger using zerolog.
// It supports both console output and optional database writing.
// Logger implements contract.Logger using zerolog.
type Logger struct {
	zerolog.Logger
	mu        sync.RWMutex
	dbWriter  LogDBWriter
	source    string
	dbLevel   int      // minimum severity for DB writes (0=all, 4=error only); -1 = disabled
	dbExclude []string // messages containing any of these substrings are NOT written to DB
	level     string   // current log level string
}

// New creates a new Logger with the given log level and optional color config.
// Output goes to stderr with ANSI color support via zerolog.ConsoleWriter.
func New(level types.LogLevel, _ string) *Logger {
	return NewWithConfig(level, false)
}

// NewWithConfig creates a Logger with explicit color control.
// Set noColor to true to disable ANSI colors (e.g. when redirecting to file).
func NewWithConfig(level types.LogLevel, noColor bool) *Logger {
	output := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    noColor,
	}

	zl := zerolog.New(output).
		With().
		Timestamp().
		Logger()

	// Set log level
	switch level {
	case types.LogLevelTrace:
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case types.LogLevelDebug:
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case types.LogLevelInfo:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case types.LogLevelWarn:
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case types.LogLevelError:
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	return &Logger{Logger: zl, level: string(level)}
}

// SetLevel changes the log level at runtime.
// Returns the previous level string.
func (l *Logger) SetLevel(level types.LogLevel) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.level
	l.level = string(level)

	switch level {
	case types.LogLevelTrace:
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case types.LogLevelDebug:
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case types.LogLevelInfo:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case types.LogLevelWarn:
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case types.LogLevelError:
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
	return prev
}

// Level returns the current log level string.
func (l *Logger) Level() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level
}

// SetDBWriter attaches a database writer to the logger.
// Parameters:
//   - w: the writer, or nil to detach
//   - source: module/component name passed to SaveLog
//   - dbLevel: minimum severity for DB writes ("", "trace", "debug", "info", "warn", "error")
//     Empty string or unknown level defaults to "info".
func (l *Logger) SetDBWriter(w LogDBWriter, source string, dbLevel types.LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dbWriter = w
	l.source = source

	// Parse DB log level; empty/unset defaults to info
	if dbLevel == "" {
		dbLevel = types.LogLevelInfo
	}
	if w, ok := levelWeights[string(dbLevel)]; ok {
		l.dbLevel = w
	} else {
		l.dbLevel = 2 // default: info
	}
}

// SetDBExclude sets keywords that exclude a log message from being written to DB.
// If the log message contains any of the keywords (case-sensitive substring match),
// it will be skipped. Pass nil or empty slice to clear.
func (l *Logger) SetDBExclude(patterns []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dbExclude = patterns
}

// writeToDB writes a log entry to the attached database writer (if any).
// Filters by:
//  1. DB log level — messages below the threshold are dropped
//  2. Exclude keywords — if the message contains any configured keyword, it is skipped
// Errors from the DB writer are silently ignored to avoid cascading failures.
func (l *Logger) writeToDB(level, msg string, keysAndValues []interface{}) {
	l.writeEventToDB(level, msg, keysAndValues, types.LogEventContext{})
}

// writeEventToDB writes a log entry with structured event context.
// See writeToDB for filter behavior; the LogEventContext is passed through to SaveLogWithContext.
func (l *Logger) writeEventToDB(level, msg string, keysAndValues []interface{}, lc types.LogEventContext) {
	l.mu.RLock()
	w := l.dbWriter
	src := l.source
	minLevel := l.dbLevel
	exclude := l.dbExclude
	l.mu.RUnlock()
	if w == nil {
		return
	}

	// Filter 1: skip messages below the configured DB severity threshold
	msgLevel, ok := levelWeights[level]
	if !ok || msgLevel < minLevel {
		return
	}

	// Filter 2: skip messages matching any exclude keyword
	for _, pattern := range exclude {
		if strings.Contains(msg, pattern) {
			return
		}
	}

	fields := make(map[string]interface{})
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			continue
		}
		fields[key] = keysAndValues[i+1]
	}

	// Best-effort DB write; never block or crash on DB errors
	_ = w.SaveLogWithContext(level, msg, fields, src, lc)
}

// LogEvent logs a message with structured QQ event context to both console and database.
// Unlike Info/Debug/etc., this stores EventType/ChannelID/GuildID/GroupID/AuthorID
// as separate indexed columns for efficient querying.
func (l *Logger) LogEvent(level, msg string, lc types.LogEventContext, keysAndValues ...interface{}) {
	// Console output with all context fields
	event := l.Logger.Info()
	event.Str("event_type", lc.EventType).
		Str("channel_id", lc.ChannelID).
		Str("guild_id", lc.GuildID).
		Str("group_id", lc.GroupID).
		Str("author_id", lc.AuthorID).
		Str("author_name", lc.AuthorName).
		Str("member_role", lc.MemberRole).
		Str("message_id", lc.MessageID)
	addFields(event, keysAndValues)
	event.Msg(msg)

	// Database write with structured context
	l.writeEventToDB(level, msg, keysAndValues, lc)
}

// Debug logs a debug message with optional key-value pairs.
func (l *Logger) Debug(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Debug()
	addFields(event, keysAndValues)
	event.Msg(msg)
	l.writeToDB("debug", msg, keysAndValues)
}

// Info logs an info message with optional key-value pairs.
func (l *Logger) Info(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Info()
	addFields(event, keysAndValues)
	event.Msg(msg)
	l.writeToDB("info", msg, keysAndValues)
}

// Warn logs a warning message with optional key-value pairs.
func (l *Logger) Warn(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Warn()
	addFields(event, keysAndValues)
	event.Msg(msg)
	l.writeToDB("warn", msg, keysAndValues)
}

// Error logs an error message with optional key-value pairs.
func (l *Logger) Error(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Error()
	addFields(event, keysAndValues)
	event.Msg(msg)
	l.writeToDB("error", msg, keysAndValues)
}

// addFields adds key-value pairs to a zerolog event.
func addFields(event *zerolog.Event, keysAndValues []interface{}) {
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			continue
		}
		event.Any(key, keysAndValues[i+1])
	}
}

// With creates a child logger with additional fields.
func (l *Logger) With(fields map[string]interface{}) *Logger {
	child := l.Logger.With().Fields(fields).Logger()
	return &Logger{Logger: child}
}
