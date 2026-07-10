package storage

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChannelRecord tracks channel/guild activity.
type ChannelRecord struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	ChannelID    string    `gorm:"size:128;uniqueIndex;not null"` // QQ channel ID
	GuildID      string    `gorm:"size:128;index"`                 // Parent guild ID
	FirstSeenAt  time.Time `gorm:"not null"`
	LastSeenAt   time.Time `gorm:"not null;index"`
	MessageCount int64     `gorm:"default:1;not null"`
}

// RecordChannel upserts a channel record, incrementing the message count.
func (s *Storage) RecordChannel(channelID, guildID string) error {
	now := time.Now()
	entry := ChannelRecord{
		ChannelID:    channelID,
		GuildID:      guildID,
		FirstSeenAt:  now,
		LastSeenAt:   now,
		MessageCount: 1,
	}

	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_seen_at":  now,
			"guild_id":      guildID,
			"message_count": gorm.Expr("message_count + 1"),
		}),
	}).Create(&entry).Error

	if err != nil {
		return fmt.Errorf("record channel: %w", err)
	}
	return nil
}

// QueryChannels retrieves channel records with optional limit/offset, ordered by last_seen_at DESC.
func (s *Storage) QueryChannels(limit, offset int) ([]ChannelRecord, error) {
	query := s.db.Model(&ChannelRecord{}).Order("last_seen_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var entries []ChannelRecord
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	return entries, nil
}

// CountChannels returns the total number of unique channels.
func (s *Storage) CountChannels() (int64, error) {
	var count int64
	if err := s.db.Model(&ChannelRecord{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count channels: %w", err)
	}
	return count, nil
}

// IsNewChannelToday checks if a channel was first seen today.
func (s *Storage) IsNewChannelToday(channelID string) (bool, error) {
	todayStart := time.Now().Truncate(24 * time.Hour)
	var record ChannelRecord
	err := s.db.Where("channel_id = ? AND first_seen_at >= ?", channelID, todayStart).First(&record).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check new channel: %w", err)
	}
	return true, nil
}

// migrateChannelTable ensures the ChannelRecord table exists.
func migrateChannelTable(db *gorm.DB) error {
	return db.AutoMigrate(&ChannelRecord{})
}
