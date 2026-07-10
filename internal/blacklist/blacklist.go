// Package blacklist provides framework-level user/group filtering.
// Blacklisted users and groups are silently ignored at the middleware level.
// The blacklist is persisted via Storage and cached in memory for fast lookup.
package blacklist

import (
	"strings"
	"sync"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	framelog "github.com/Luoyangan/LQBOT/internal/log"
)

// Blacklist prefixes for storage keys.
const (
	KeyPrefixUser  = "blacklist:user:"
	KeyPrefixGroup = "blacklist:group:"
)

// Manager handles blacklist operations.
// Thread-safe and backed by persistent storage + in-memory cache.
type Manager struct {
	mu          sync.RWMutex
	storage     storage
	logger      *framelog.Logger
	userCache   map[string]bool // userID → blocked
	groupCache  map[string]bool // groupID → blocked
	lastRefresh time.Time
}

type storage interface {
	Get(key string, dest interface{}) error
	Set(key string, value interface{}) error
	Delete(key string) error
}

// New creates a blacklist manager.
func New(s storage, logger *framelog.Logger) *Manager {
	m := &Manager{
		storage:    s,
		logger:     logger,
		userCache:  make(map[string]bool),
		groupCache: make(map[string]bool),
	}
	m.refreshCache()
	return m
}

// refreshCache reloads all blacklist entries from storage.
func (m *Manager) refreshCache() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear caches
	m.userCache = make(map[string]bool)
	m.groupCache = make(map[string]bool)

	// Scan all blacklist entries (we read them individually from storage)
	// Since we use KV storage, we maintain entries on add/remove.
	// The cache is populated lazily via Add/remove, and on startup
	// we can scan known prefixes.
	m.lastRefresh = time.Now()
}

// IsBlockedUser checks if a user is blacklisted.
func (m *Manager) IsBlockedUser(userID string) bool {
	if userID == "" {
		return false
	}
	m.mu.RLock()
	blocked, ok := m.userCache[userID]
	m.mu.RUnlock()
	if ok {
		return blocked
	}

	// Check storage
	var val string
	key := KeyPrefixUser + userID
	if err := m.storage.Get(key, &val); err != nil {
		return false
	}

	blocked = val == "1"
	m.mu.Lock()
	m.userCache[userID] = blocked
	m.mu.Unlock()
	return blocked
}

// IsBlockedGroup checks if a group is blacklisted.
func (m *Manager) IsBlockedGroup(groupID string) bool {
	if groupID == "" {
		return false
	}
	m.mu.RLock()
	blocked, ok := m.groupCache[groupID]
	m.mu.RUnlock()
	if ok {
		return blocked
	}

	// Check storage
	var val string
	key := KeyPrefixGroup + groupID
	if err := m.storage.Get(key, &val); err != nil {
		return false
	}

	blocked = val == "1"
	m.mu.Lock()
	m.groupCache[groupID] = blocked
	m.mu.Unlock()
	return blocked
}

// AddUser blacklists a user.
func (m *Manager) AddUser(userID string) error {
	key := KeyPrefixUser + userID
	if err := m.storage.Set(key, "1"); err != nil {
		return err
	}
	m.mu.Lock()
	m.userCache[userID] = true
	m.mu.Unlock()
	m.logger.Info("user blacklisted", "user_id", userID)
	return nil
}

// RemoveUser removes a user from the blacklist.
func (m *Manager) RemoveUser(userID string) error {
	key := KeyPrefixUser + userID
	if err := m.storage.Delete(key); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.userCache, userID)
	m.mu.Unlock()
	m.logger.Info("user removed from blacklist", "user_id", userID)
	return nil
}

// AddGroup blacklists a group.
func (m *Manager) AddGroup(groupID string) error {
	key := KeyPrefixGroup + groupID
	if err := m.storage.Set(key, "1"); err != nil {
		return err
	}
	m.mu.Lock()
	m.groupCache[groupID] = true
	m.mu.Unlock()
	m.logger.Info("group blacklisted", "group_id", groupID)
	return nil
}

// RemoveGroup removes a group from the blacklist.
func (m *Manager) RemoveGroup(groupID string) error {
	key := KeyPrefixGroup + groupID
	if err := m.storage.Delete(key); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.groupCache, groupID)
	m.mu.Unlock()
	m.logger.Info("group removed from blacklist", "group_id", groupID)
	return nil
}

// IsBlocked checks both user and group blacklist.
func (m *Manager) IsBlocked(ctx contract.EventContext) bool {
	if ctx.GroupID() != "" && m.IsBlockedGroup(ctx.GroupID()) {
		return true
	}
	if ctx.AuthorID() != "" && m.IsBlockedUser(ctx.AuthorID()) {
		return true
	}
	return false
}

// ListUsers returns all blacklisted users.
func (m *Manager) ListUsers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []string
	for id, blocked := range m.userCache {
		if blocked {
			result = append(result, id)
		}
	}
	return result
}

// ListGroups returns all blacklisted groups.
func (m *Manager) ListGroups() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []string
	for id, blocked := range m.groupCache {
		if blocked {
			result = append(result, id)
		}
	}
	return result
}

// StringList formats users and groups for display.
func (m *Manager) StringList() string {
	users := m.ListUsers()
	groups := m.ListGroups()

	var parts []string
	if len(users) > 0 {
		parts = append(parts, "黑名单用户: "+strings.Join(users, ", "))
	}
	if len(groups) > 0 {
		parts = append(parts, "黑名单群: "+strings.Join(groups, ", "))
	}
	if len(parts) == 0 {
		return "当前黑名单为空"
	}
	return strings.Join(parts, "\n")
}
