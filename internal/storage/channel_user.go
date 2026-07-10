package storage

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChannelUserRecord tracks users in guild/channel contexts.
// Channel user IDs are in a different namespace from group/C2C user IDs.
type ChannelUserRecord struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	UserID       string    `gorm:"size:128;not null;uniqueIndex:idx_channel_user"` // User ID within the guild
	GuildID      string    `gorm:"size:128;not null;uniqueIndex:idx_channel_user"` // Parent guild ID
	ChannelID    string    `gorm:"size:128"`                                        // Last seen channel within guild
	Username     string    `gorm:"size:256"`                                        // Last known username
	LastMessage  string    `gorm:"type:text"`                                       // Last message content (truncated)
	FirstSeenAt  time.Time `gorm:"not null"`
	LastSeenAt   time.Time `gorm:"not null;index"`
	MessageCount int64     `gorm:"default:1;not null"`
}

// RecordChannelUser upserts a channel user record.
func (s *Storage) RecordChannelUser(userID, guildID, channelID, username, lastMessage string) error {
	now := time.Now()
	entry := ChannelUserRecord{
		UserID:       userID,
		GuildID:      guildID,
		ChannelID:    channelID,
		Username:     username,
		LastMessage:  truncate(lastMessage, 500),
		FirstSeenAt:  now,
		LastSeenAt:   now,
		MessageCount: 1,
	}

	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "guild_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_seen_at":  now,
			"channel_id":    channelID,
			"username":      username,
			"last_message":  truncate(lastMessage, 500),
			"message_count": gorm.Expr("message_count + 1"),
		}),
	}).Create(&entry).Error

	if err != nil {
		return fmt.Errorf("record channel user: %w", err)
	}
	return nil
}

// QueryChannelUsers retrieves channel user records, ordered by last_seen_at DESC.
func (s *Storage) QueryChannelUsers(guildID string, limit, offset int) ([]ChannelUserRecord, error) {
	query := s.db.Model(&ChannelUserRecord{}).Order("last_seen_at DESC")
	if guildID != "" {
		query = query.Where("guild_id = ?", guildID)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var entries []ChannelUserRecord
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query channel users: %w", err)
	}
	return entries, nil
}

// CountChannelUsers returns the total number of unique channel user records.
func (s *Storage) CountChannelUsers() (int64, error) {
	var count int64
	if err := s.db.Model(&ChannelUserRecord{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count channel users: %w", err)
	}
	return count, nil
}

// migrateChannelUserTable ensures the ChannelUserRecord table exists.
func migrateChannelUserTable(db *gorm.DB) error {
	return db.AutoMigrate(&ChannelUserRecord{})
}
