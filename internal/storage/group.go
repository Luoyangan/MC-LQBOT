package storage

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GroupRecord tracks group chat activity.
type GroupRecord struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`
	GroupID      string    `gorm:"size:128;uniqueIndex;not null"` // QQ group open ID
	FirstSeenAt  time.Time `gorm:"not null"`
	LastSeenAt   time.Time `gorm:"not null;index"`
	MessageCount int64     `gorm:"default:1;not null"`
}

// RecordGroup upserts a group record, incrementing the message count.
func (s *Storage) RecordGroup(groupID string) error {
	now := time.Now()
	entry := GroupRecord{
		GroupID:      groupID,
		FirstSeenAt:  now,
		LastSeenAt:   now,
		MessageCount: 1,
	}

	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "group_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_seen_at":  now,
			"message_count": gorm.Expr("message_count + 1"),
		}),
	}).Create(&entry).Error

	if err != nil {
		return fmt.Errorf("record group: %w", err)
	}
	return nil
}

// QueryGroups retrieves group records with optional limit/offset, ordered by last_seen_at DESC.
func (s *Storage) QueryGroups(limit, offset int) ([]GroupRecord, error) {
	query := s.db.Model(&GroupRecord{}).Order("last_seen_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var entries []GroupRecord
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	return entries, nil
}

// CountGroups returns the total number of unique groups.
func (s *Storage) CountGroups() (int64, error) {
	var count int64
	if err := s.db.Model(&GroupRecord{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count groups: %w", err)
	}
	return count, nil
}

// IsNewGroupToday checks if a group was first seen today.
func (s *Storage) IsNewGroupToday(groupID string) (bool, error) {
	todayStart := time.Now().Truncate(24 * time.Hour)
	var record GroupRecord
	err := s.db.Where("group_id = ? AND first_seen_at >= ?", groupID, todayStart).First(&record).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check new group: %w", err)
	}
	return true, nil
}

// migrateGroupTable ensures the GroupRecord table exists.
func migrateGroupTable(db *gorm.DB) error {
	return db.AutoMigrate(&GroupRecord{})
}
