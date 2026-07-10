package storage

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserRecord tracks user activity across group chat and C2C contexts.
type UserRecord struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	UserID       string    `gorm:"size:128;uniqueIndex;not null"` // QQ user open ID
	Username     string    `gorm:"size:256"`                      // Last known username/nick
	LastMessage  string    `gorm:"type:text"`                     // Last message content (truncated)
	Scene        string    `gorm:"size:32;index"`                 // Last seen scene: "group" / "c2c"
	FirstSeenAt  time.Time `gorm:"not null"`
	LastSeenAt   time.Time `gorm:"not null;index"`
	MessageCount int64     `gorm:"default:1;not null"`
}

// RecordUser upserts a user record.
// If the user already exists, updates LastSeenAt, Username, LastMessage, Scene,
// and increments MessageCount.
func (s *Storage) RecordUser(userID, username, lastMessage, scene string) error {
	now := time.Now()
	entry := UserRecord{
		UserID:      userID,
		Username:    username,
		LastMessage: truncate(lastMessage, 500),
		Scene:       scene,
		FirstSeenAt: now,
		LastSeenAt:  now,
		MessageCount: 1,
	}

	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_seen_at":  now,
			"username":      username,
			"last_message":  truncate(lastMessage, 500),
			"scene":         scene,
			"message_count": gorm.Expr("message_count + 1"),
		}),
	}).Create(&entry).Error

	if err != nil {
		return fmt.Errorf("record user: %w", err)
	}
	return nil
}

// QueryUsers retrieves user records with optional limit/offset, ordered by last_seen_at DESC.
func (s *Storage) QueryUsers(limit, offset int) ([]UserRecord, error) {
	query := s.db.Model(&UserRecord{}).Order("last_seen_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var entries []UserRecord
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	return entries, nil
}

// CountUsers returns the total number of unique users.
func (s *Storage) CountUsers() (int64, error) {
	var count int64
	if err := s.db.Model(&UserRecord{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

// IsNewUserToday checks if a user was first seen today.
func (s *Storage) IsNewUserToday(userID string) (bool, error) {
	todayStart := time.Now().Truncate(24 * time.Hour)
	var record UserRecord
	err := s.db.Where("user_id = ? AND first_seen_at >= ?", userID, todayStart).First(&record).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check new user: %w", err)
	}
	return true, nil
}

// migrateUserTable ensures the UserRecord table exists.
func migrateUserTable(db *gorm.DB) error {
	return db.AutoMigrate(&UserRecord{})
}
