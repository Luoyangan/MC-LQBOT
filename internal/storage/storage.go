// Package storage implements the Storage interface using GORM.
package storage

import (
	"fmt"
	"sync"

	"github.com/Luoyangan/LQBOT/internal/types"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Storage implements contract.Storage using GORM.
type Storage struct {
	mu   sync.RWMutex
	db   *gorm.DB
	dsn  string
	driver types.StorageDriver
	data map[string]string // Simple KV cache
}

// New creates a new Storage instance. Supports "sqlite" and "mysql" drivers.
func New(cfg types.StorageConfig) (*Storage, error) {
	var dialector gorm.Dialector
	switch cfg.Driver {
	case types.StorageMySQL:
		dialector = mysql.Open(cfg.DSN)
	default:
		dialector = sqlite.Open(cfg.DSN)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Auto-migrate tables
	if err := db.AutoMigrate(&KVEntry{}); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := migrateLogTable(db); err != nil {
		return nil, fmt.Errorf("migrate log table: %w", err)
	}
	if err := migrateUserTable(db); err != nil {
		return nil, fmt.Errorf("migrate user table: %w", err)
	}
	if err := migrateGroupTable(db); err != nil {
		return nil, fmt.Errorf("migrate group table: %w", err)
	}
	if err := migrateChannelTable(db); err != nil {
		return nil, fmt.Errorf("migrate channel table: %w", err)
	}
	if err := migrateChannelUserTable(db); err != nil {
		return nil, fmt.Errorf("migrate channel user table: %w", err)
	}
	if err := migrateDailyTable(db); err != nil {
		return nil, fmt.Errorf("migrate daily table: %w", err)
	}

	return &Storage{
		db:     db,
		dsn:    cfg.DSN,
		driver: cfg.Driver,
		data:   make(map[string]string),
	}, nil
}

// KVEntry represents a key-value pair in the database.
type KVEntry struct {
	Key   string `gorm:"primaryKey;size:512"`
	Value string `gorm:"type:text;not null"`
}

// Get retrieves a value by key and unmarshals it into dest.
func (s *Storage) Get(key string, dest interface{}) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var entry KVEntry
	if err := s.db.First(&entry, "key = ?", key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("key not found: %s", key)
		}
		return fmt.Errorf("read key %s: %w", key, err)
	}

	// For now, if dest is a *string, assign directly
	if s, ok := dest.(*string); ok {
		*s = entry.Value
		return nil
	}

	return fmt.Errorf("unsupported destination type %T", dest)
}

// Set stores a key-value pair.
func (s *Storage) Set(key string, value interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	valStr := fmt.Sprintf("%v", value)
	entry := KVEntry{Key: key, Value: valStr}

	if err := s.db.Save(&entry).Error; err != nil {
		return fmt.Errorf("write key %s: %w", key, err)
	}
	return nil
}

// Delete removes a key-value pair.
func (s *Storage) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.db.Delete(&KVEntry{}, "key = ?", key).Error; err != nil {
		return fmt.Errorf("delete key %s: %w", key, err)
	}
	return nil
}

// Close closes the database connection.
func (s *Storage) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("get underlying db: %w", err)
	}
	return sqlDB.Close()
}

// DSN returns the database DSN/path string.
func (s *Storage) DSN() string { return s.dsn }

// Driver returns the storage driver type ("sqlite" or "mysql").
func (s *Storage) Driver() string { return string(s.driver) }

// DB returns the underlying GORM database for advanced queries.
func (s *Storage) DB() *gorm.DB { return s.db }

// TableRowCount returns the row count for the given model table.
func (s *Storage) TableRowCount(model interface{}) (int64, error) {
	var count int64
	if err := s.db.Model(model).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count table: %w", err)
	}
	return count, nil
}

// truncate returns s truncated to at most max runes.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
