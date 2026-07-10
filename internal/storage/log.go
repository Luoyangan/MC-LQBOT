package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Luoyangan/LQBOT/internal/types"
	"gorm.io/gorm"
)

// LogEntry represents a log record stored in the database.
type LogEntry struct {
	ID         uint      `gorm:"primaryKey;autoIncrement"`
	Level      string    `gorm:"size:16;index;not null"`
	Message    string    `gorm:"type:text;not null"`
	Fields     string    `gorm:"type:text"`          // JSON-encoded key-value pairs
	Source     string    `gorm:"size:128;index"`     // Source module/component
	EventType  string    `gorm:"size:64;index"`      // QQ event type, e.g. "MESSAGE_CREATE" (optional)
	ChannelID  string    `gorm:"size:128;index"`     // Source channel ID (optional)
	GuildID    string    `gorm:"size:128;index"`     // Guild/server ID (optional)
	GroupID    string    `gorm:"size:128;index"`     // Group chat ID (optional)
	AuthorID   string    `gorm:"size:128;index"`     // Message sender ID (optional)
	AuthorName string    `gorm:"size:256"`           // Message sender username (optional)
	MemberRole string    `gorm:"size:32"`            // Group member role: owner/admin/member (optional)
	MessageID  string    `gorm:"size:128"`           // QQ message ID (optional)
	CreatedAt  time.Time `gorm:"index;not null"`
}

// SaveLog writes a log entry to the database.
// level: log level (info, warn, error, debug, trace)
// message: log message text
// fields: optional key-value pairs (will be JSON-encoded)
// source: module/component name that produced the log
func (s *Storage) SaveLog(level, message string, fields map[string]interface{}, source string) error {
	return s.saveLog(level, message, fields, source, types.LogEventContext{})
}

// SaveLogWithContext writes a log entry with structured event context.
// In addition to SaveLog fields, it stores EventType, ChannelID, GuildID, GroupID, AuthorID
// as separate indexed database columns for efficient querying.
func (s *Storage) SaveLogWithContext(level, message string, fields map[string]interface{}, source string, lc types.LogEventContext) error {
	return s.saveLog(level, message, fields, source, lc)
}

// saveLog is the internal implementation shared by SaveLog and SaveLogWithContext.
func (s *Storage) saveLog(level, message string, fields map[string]interface{}, source string, lc types.LogEventContext) error {
	fieldsJSON := ""
	if len(fields) > 0 {
		b, err := json.Marshal(fields)
		if err != nil {
			fieldsJSON = fmt.Sprintf(`{"error":"%v"}`, err)
		} else {
			fieldsJSON = string(b)
		}
	}

	entry := LogEntry{
		Level:      level,
		Message:    message,
		Fields:     fieldsJSON,
		Source:     source,
		EventType:  lc.EventType,
		ChannelID:  lc.ChannelID,
		GuildID:    lc.GuildID,
		GroupID:    lc.GroupID,
		AuthorID:   lc.AuthorID,
		AuthorName: lc.AuthorName,
		MemberRole: lc.MemberRole,
		MessageID:  lc.MessageID,
		CreatedAt:  time.Now(),
	}

	if err := s.db.Create(&entry).Error; err != nil {
		return fmt.Errorf("save log entry: %w", err)
	}
	return nil
}

// CleanupLogs deletes log entries older than the specified time.
// Returns the number of deleted rows.
func (s *Storage) CleanupLogs(before time.Time) (int64, error) {
	result := s.db.Where("created_at < ?", before).Delete(&LogEntry{})
	if result.Error != nil {
		return 0, fmt.Errorf("cleanup logs: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// CleanupLogsByRetentionDays deletes log entries older than retainDays days.
// Returns the number of deleted rows.
func (s *Storage) CleanupLogsByRetentionDays(retainDays int) (int64, error) {
	before := time.Now().AddDate(0, 0, -retainDays)
	return s.CleanupLogs(before)
}

// CountLogs returns the total number of log entries in the database.
func (s *Storage) CountLogs() (int64, error) {
	var count int64
	if err := s.db.Model(&LogEntry{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count logs: %w", err)
	}
	return count, nil
}

// QueryLogs retrieves log entries with optional filters.
// Filters: level, source, eventType, channelID, guildID, groupID, authorID, limit, offset.
// Empty string for any filter means "no filter on this field".
func (s *Storage) QueryLogs(level, source, eventType, channelID, guildID, groupID, authorID string, limit, offset int) ([]LogEntry, error) {
	query := s.db.Model(&LogEntry{}).Order("created_at DESC")

	if level != "" {
		query = query.Where("level = ?", level)
	}
	if source != "" {
		query = query.Where("source = ?", source)
	}
	if eventType != "" {
		query = query.Where("event_type = ?", eventType)
	}
	if channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}
	if guildID != "" {
		query = query.Where("guild_id = ?", guildID)
	}
	if groupID != "" {
		query = query.Where("group_id = ?", groupID)
	}
	if authorID != "" {
		query = query.Where("author_id = ?", authorID)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var entries []LogEntry
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query logs: %w", err)
	}
	return entries, nil
}

// QueryLogsSince retrieves log entries with ID greater than the given lastID.
// Returns entries ordered by ID ASC (oldest first within the new set).
// The limit caps how many entries are returned (max 50).
func (s *Storage) QueryLogsSince(lastID uint, limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	query := s.db.Model(&LogEntry{}).Where("id > ?", lastID).Order("id ASC").Limit(limit)
	var entries []LogEntry
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query logs since %d: %w", lastID, err)
	}
	return entries, nil
}

// migrateLogTable ensures the LogEntry table exists.
func migrateLogTable(db *gorm.DB) error {
	return db.AutoMigrate(&LogEntry{})
}
