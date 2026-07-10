// Package middleware implements the middleware chain for message processing.
package middleware

import (
	"sort"
	"sync"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// Chain manages an ordered list of middlewares.
type Chain struct {
	middlewares []contract.Middleware
}

// New creates an empty middleware chain.
func New() *Chain {
	return &Chain{}
}

// Add registers a middleware to the chain.
// The middleware is inserted in order of its Order() value.
func (c *Chain) Add(m contract.Middleware) {
	c.middlewares = append(c.middlewares, m)
	sort.Slice(c.middlewares, func(i, j int) bool {
		return c.middlewares[i].Order() < c.middlewares[j].Order()
	})
}

// Remove removes a middleware by name.
func (c *Chain) Remove(name string) {
	for i, m := range c.middlewares {
		if m.Name() == name {
			c.middlewares = append(c.middlewares[:i], c.middlewares[i+1:]...)
			return
		}
	}
}

// Execute runs all middlewares in order, passing the event through the chain.
// Each middleware calls next() to pass control to the next middleware.
// The final handler is called after all middlewares have completed.
func (c *Chain) Execute(ctx contract.EventContext, final func() error) error {
	if len(c.middlewares) == 0 {
		return final()
	}

	// Build the middleware chain from inside out
	handler := final
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		next := handler
		handler = func() error {
			return m.Handle(ctx, next)
		}
	}

	return handler()
}

// Len returns the number of middlewares in the chain.
func (c *Chain) Len() int {
	return len(c.middlewares)
}

// List returns all registered middlewares (sorted by order).
func (c *Chain) List() []contract.Middleware {
	result := make([]contract.Middleware, len(c.middlewares))
	copy(result, c.middlewares)
	return result
}

// --- Built-in Middlewares ---

// LoggingMiddleware logs all incoming events.
type LoggingMiddleware struct {
	logger contract.Logger
}

// NewLoggingMiddleware creates a middleware that logs events.
func NewLoggingMiddleware(logger contract.Logger) *LoggingMiddleware {
	return &LoggingMiddleware{logger: logger}
}

func (m *LoggingMiddleware) Name() string { return "logging" }
func (m *LoggingMiddleware) Order() int   { return -1000 }

func (m *LoggingMiddleware) Handle(ctx contract.EventContext, next func() error) error {
	m.logger.Debug("event received",
		"channel_id", ctx.ChannelID(),
		"author_id", ctx.AuthorID(),
		"content", ctx.Content(),
	)
	return next()
}

// RateLimitMiddleware limits the rate of message processing using token buckets.
// Each channel/group/user has its own bucket. Rate and capacity are configurable.
type RateLimitMiddleware struct {
	logger  contract.Logger
	buckets *tokenBucketStore
}

// NewRateLimitMiddleware creates a middleware for rate limiting.
// Default: 5 tokens/sec, max burst 10.
func NewRateLimitMiddleware(logger contract.Logger) *RateLimitMiddleware {
	return &RateLimitMiddleware{
		logger:  logger,
		buckets: newTokenBucketStore(5, 10),
	}
}

// NewRateLimitMiddlewareWith creates a rate limiter with custom settings.
func NewRateLimitMiddlewareWith(rate, burst int, logger contract.Logger) *RateLimitMiddleware {
	if rate < 1 {
		rate = 1
	}
	if burst < rate {
		burst = rate
	}
	return &RateLimitMiddleware{
		logger:  logger,
		buckets: newTokenBucketStore(rate, burst),
	}
}

func (m *RateLimitMiddleware) Name() string { return "rate_limit" }
func (m *RateLimitMiddleware) Order() int   { return -500 }

// resolveKey returns a scene-aware rate limit key:
//   - C2C: "user:<author_id>"
//   - Group: "group:<group_id>"
//   - Guild/other: "channel:<channel_id>"
func (m *RateLimitMiddleware) resolveKey(ctx contract.EventContext) string {
	switch ctx.Scene() {
	case contract.SceneC2C:
		return "user:" + ctx.AuthorID()
	case contract.SceneGroup:
		return "group:" + ctx.GroupID()
	default:
		return "channel:" + ctx.ChannelID()
	}
}

func (m *RateLimitMiddleware) Handle(ctx contract.EventContext, next func() error) error {
	key := m.resolveKey(ctx)
	if !m.buckets.allow(key) {
		if m.logger != nil {
			m.logger.Warn("rate limit exceeded, dropping event",
				"key", key,
				"scene", int(ctx.Scene()),
				"channel_id", ctx.ChannelID(),
				"author_id", ctx.AuthorID(),
			)
		}
		return nil
	}
	return next()
}

// Close stops the background cleanup goroutine.
func (m *RateLimitMiddleware) Close() {
	m.buckets.close()
}

// --- Token Bucket Implementation ---

// tokenBucket represents a single rate limit bucket.
type tokenBucket struct {
	tokens     float64   // current token count
	maxTokens  float64   // maximum burst size
	rate       float64   // tokens per second
	lastRefill time.Time // last refill timestamp
}

// tokenBucketStore manages a collection of token buckets keyed by scene prefix + ID.
// Uses lazy refill: tokens are calculated on demand in allow() rather than
// using a background timer, which eliminates unnecessary lock contention.
type tokenBucketStore struct {
	mu      sync.RWMutex
	buckets map[string]*tokenBucket
	rate    float64
	burst   float64
	closeCh chan struct{}
}

// newTokenBucketStore creates a store and starts a background cleanup goroutine
// that removes buckets idle for more than 30 minutes.
func newTokenBucketStore(rate, burst int) *tokenBucketStore {
	s := &tokenBucketStore{
		buckets: make(map[string]*tokenBucket),
		rate:    float64(rate),
		burst:   float64(burst),
		closeCh: make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// cleanupLoop periodically removes buckets that haven't been accessed
// in 30+ minutes, preventing memory leaks from idle channels/users.
func (s *tokenBucketStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for key, b := range s.buckets {
				if now.Sub(b.lastRefill) > 30*time.Minute {
					delete(s.buckets, key)
				}
			}
			s.mu.Unlock()
		case <-s.closeCh:
			return
		}
	}
}

// allow checks if a request identified by key can proceed.
// Uses lazy refill: tokens are calculated since the last refill time,
// so no background refill goroutine is needed.
func (s *tokenBucketStore) allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, exists := s.buckets[key]
	if !exists {
		b = &tokenBucket{
			tokens:     s.burst,
			maxTokens:  s.burst,
			rate:       s.rate,
			lastRefill: time.Now(),
		}
		s.buckets[key] = b
	}

	// Lazy refill: calculate elapsed tokens since last check
	now := time.Now()
	elapsed := now.Sub(b.lastRefill)
	b.tokens += s.rate * elapsed.Seconds()
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// close stops the background cleanup goroutine.
func (s *tokenBucketStore) close() {
	close(s.closeCh)
}
