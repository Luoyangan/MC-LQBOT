package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// DailyRecord stores per-day aggregate statistics about bot activity.
// One row per date, created or updated when events occur.
type DailyRecord struct {
	ID  uint   `gorm:"primaryKey;autoIncrement"`
	Date string `gorm:"size:10;uniqueIndex;not null"` // "2006-01-02"

	// ── 单聊 (C2C) ──
	C2CActiveUsers   int64 `gorm:"default:0;not null"` // 使用用户数
	C2CNewUsers      int64 `gorm:"default:0;not null"` // 新添加用户数
	C2CRemovedUsers  int64 `gorm:"default:0;not null"` // 新移除用户数
	C2CIncomingMsg   int64 `gorm:"default:0;not null"` // 上行消息量
	C2CIncomingUsers int64 `gorm:"default:0;not null"` // 上行消息人数 (unique)
	C2COutgoingMsg   int64 `gorm:"default:0;not null"` // 下行消息量

	// ── 群聊 (Group) ──
	GroupActiveCount  int64 `gorm:"default:0;not null"` // 使用群数
	GroupNewCount     int64 `gorm:"default:0;not null"` // 新添加群数
	GroupRemovedCount int64 `gorm:"default:0;not null"` // 新移除群数
	GroupIncomingMsg  int64 `gorm:"default:0;not null"` // 群上行消息量
	GroupOutgoingMsg  int64 `gorm:"default:0;not null"` // 群下行消息量

	// ── 频道 (Channel) ──
	ChannelActiveCount  int64 `gorm:"default:0;not null"` // 使用频道数
	ChannelNewCount     int64 `gorm:"default:0;not null"` // 新添加频道数
	ChannelRemovedCount int64 `gorm:"default:0;not null"` // 新移除频道数
	ChannelIncomingMsg  int64 `gorm:"default:0;not null"` // 频道上行消息量
	ChannelOutgoingMsg  int64 `gorm:"default:0;not null"` // 频道下行消息量

	// ── 去重追踪 (JSON 数组，运行时维护) ──
	SeenC2CUsers   string `gorm:"type:text"` // JSON: [user_id1, user_id2]
	SeenGroupUsers string `gorm:"type:text"` // JSON: [user_id1, ...]
	SeenGroups     string `gorm:"type:text"` // JSON: [group_id1, ...]
	SeenChannels   string `gorm:"type:text"` // JSON: [channel_id1, ...]

	// ── 通用 (其他汇总) ──
	TotalCommands     int64 `gorm:"default:0;not null"` // 指令执行次数
	TotalInteractions int64 `gorm:"default:0;not null"` // 按钮/交互点击次数

	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

// todayDate returns today's date string in "2006-01-02" format.
func todayDate() string {
	return time.Now().Format("2006-01-02")
}

// ───────────────────────────────────────
// 单聊 (C2C)
// ───────────────────────────────────────

// RecordC2CIncoming records an incoming C2C message from the given user.
func (s *Storage) RecordC2CIncoming(userID string) error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.C2CIncomingMsg++
		if addToJSONSet(&r.SeenC2CUsers, userID) {
			r.C2CActiveUsers++
			r.C2CIncomingUsers++
		}
	})
}

// RecordC2CNewUser records a new C2C user (first time seen).
func (s *Storage) RecordC2CNewUser() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.C2CNewUsers++
	})
}

// RecordC2COutgoing records an outgoing C2C reply message.
func (s *Storage) RecordC2COutgoing() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.C2COutgoingMsg++
	})
}

// ───────────────────────────────────────
// 群聊 (Group)
// ───────────────────────────────────────

// RecordGroupIncoming records an incoming group message from the given user in the given group.
func (s *Storage) RecordGroupIncoming(userID, groupID string) error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.GroupIncomingMsg++
		if addToJSONSet(&r.SeenGroupUsers, userID) {
			// Unique user in group context
		}
		if addToJSONSet(&r.SeenGroups, groupID) {
			r.GroupActiveCount++
		}
	})
}

// RecordGroupNewGroup records a new group (bot just joined).
func (s *Storage) RecordGroupNewGroup() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.GroupNewCount++
	})
}

// RecordGroupRemoved records a group removal.
func (s *Storage) RecordGroupRemoved() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.GroupRemovedCount++
	})
}

// RecordGroupOutgoing records an outgoing group reply message.
func (s *Storage) RecordGroupOutgoing() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.GroupOutgoingMsg++
	})
}

// ───────────────────────────────────────
// 频道 (Channel)
// ───────────────────────────────────────

// RecordChannelIncoming records an incoming channel/guild message from the given user.
func (s *Storage) RecordChannelIncoming(channelID string) error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.ChannelIncomingMsg++
		if addToJSONSet(&r.SeenChannels, channelID) {
			r.ChannelActiveCount++
		}
	})
}

// RecordChannelNewChannel records a new channel (bot just joined).
func (s *Storage) RecordChannelNewChannel() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.ChannelNewCount++
	})
}

// RecordChannelRemoved records a channel removal.
func (s *Storage) RecordChannelRemoved() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.ChannelRemovedCount++
	})
}

// RecordChannelOutgoing records an outgoing channel reply message.
func (s *Storage) RecordChannelOutgoing() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.ChannelOutgoingMsg++
	})
}

// ───────────────────────────────────────
// 通用计数器
// ───────────────────────────────────────

// RecordDailyCommand increments the command execution counter for today.
func (s *Storage) RecordDailyCommand() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.TotalCommands++
	})
}

// RecordDailyInteraction increments the interaction (button click) counter for today.
func (s *Storage) RecordDailyInteraction() error {
	return s.upsertDaily(func(r *DailyRecord) {
		r.TotalInteractions++
	})
}

// ───────────────────────────────────────
// 内部辅助
// ───────────────────────────────────────

// upsertDaily reads or creates today's DailyRecord, applies the mutation, and saves.
func (s *Storage) upsertDaily(mutate func(*DailyRecord)) error {
	date := todayDate()
	now := time.Now()

	return s.db.Transaction(func(tx *gorm.DB) error {
		var record DailyRecord
		err := tx.Where("date = ?", date).First(&record).Error

		isNew := false
		if err == gorm.ErrRecordNotFound {
			isNew = true
			record = DailyRecord{
				Date:             date,
				SeenC2CUsers:     "[]",
				SeenGroupUsers:   "[]",
				SeenGroups:       "[]",
				SeenChannels:     "[]",
				CreatedAt:        now,
				UpdatedAt:        now,
			}
		} else if err != nil {
			return fmt.Errorf("read daily record: %w", err)
		}

		mutate(&record)
		record.UpdatedAt = now

		if isNew {
			return tx.Create(&record).Error
		}
		return tx.Save(&record).Error
	})
}

// addToJSONSet adds a value to a JSON array set, returning true if it was newly added.
func addToJSONSet(set *string, value string) bool {
	var items []string
	if *set != "" {
		_ = json.Unmarshal([]byte(*set), &items)
	}
	if items == nil {
		items = make([]string, 0)
	}
	// Check if already exists
	for _, item := range items {
		if item == value {
			return false
		}
	}
	items = append(items, value)
	b, _ := json.Marshal(items)
	*set = string(b)
	return true
}

// QueryDailyRecords retrieves daily records within a date range, ordered by date DESC.
func (s *Storage) QueryDailyRecords(startDate, endDate string, limit, offset int) ([]DailyRecord, error) {
	query := s.db.Model(&DailyRecord{}).Order("date DESC")
	if startDate != "" {
		query = query.Where("date >= ?", startDate)
	}
	if endDate != "" {
		query = query.Where("date <= ?", endDate)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var entries []DailyRecord
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("query daily records: %w", err)
	}
	return entries, nil
}

// CountDailyRecords returns the total number of daily records.
func (s *Storage) CountDailyRecords() (int64, error) {
	var count int64
	if err := s.db.Model(&DailyRecord{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count daily records: %w", err)
	}
	return count, nil
}

// migrateDailyTable ensures the DailyRecord table exists.
func migrateDailyTable(db *gorm.DB) error {
	return db.AutoMigrate(&DailyRecord{})
}
