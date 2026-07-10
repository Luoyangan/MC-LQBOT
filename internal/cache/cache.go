// Package cache provides a TTL-based in-memory cache for the Storage layer.
// This reduces SQLite reads for frequently accessed keys like permissions,
// blacklists, and plugin configurations.
package cache

import (
	"sync"
	"time"
)

// Entry holds a cached value with expiration.
type Entry struct {
	value    string
	deadline time.Time
}

// Cache is a TTL-based in-memory cache.
// It is safe for concurrent use.
type Cache struct {
	mu       sync.RWMutex
	entries  map[string]*Entry
	ttl      time.Duration
	closeCh  chan struct{}
}

// New creates a new Cache with the given default TTL.
// A cleanup goroutine runs every 5 minutes to remove expired entries.
// Call Close() to stop the cleanup goroutine.
func New(ttl time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]*Entry),
		ttl:     ttl,
		closeCh: make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Get retrieves a cached value by key.
// Returns the value and true if found and not expired.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return "", false
	}

	if time.Now().After(entry.deadline) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return "", false
	}

	return entry.value, true
}

// Set stores a value with the default TTL.
func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &Entry{
		value:    value,
		deadline: time.Now().Add(c.ttl),
	}
}

// SetWithTTL stores a value with a custom TTL.
func (c *Cache) SetWithTTL(key, value string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &Entry{
		value:    value,
		deadline: time.Now().Add(ttl),
	}
}

// Delete removes a cached entry.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Clear removes all cached entries.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*Entry)
}

// cleanupLoop periodically removes expired entries.
func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			now := time.Now()
			for key, entry := range c.entries {
				if now.After(entry.deadline) {
					delete(c.entries, key)
				}
			}
			c.mu.Unlock()
		case <-c.closeCh:
			return
		}
	}
}

// Close stops the cleanup goroutine.
func (c *Cache) Close() {
	close(c.closeCh)
}
