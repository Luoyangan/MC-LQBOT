package middleware

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// noopLogger implements contract.Logger with silent operations.
type noopLogger struct{}

func (l *noopLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *noopLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *noopLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *noopLogger) Error(msg string, keysAndValues ...interface{}) {}

// --- Mock Types ---

type mockEventContext struct {
	channelID string
	content   string
}

func (m *mockEventContext) Content() string                        { return m.content }
func (m *mockEventContext) RawContent() string                     { return m.content }
func (m *mockEventContext) ChannelID() string                      { return m.channelID }
func (m *mockEventContext) AuthorID() string                       { return "test-user" }
func (m *mockEventContext) MessageID() string                      { return "test-msg" }
func (m *mockEventContext) IsMentioned() bool                      { return false }
func (m *mockEventContext) GuildID() string                        { return "" }
func (m *mockEventContext) GroupID() string                        { return "" }
func (m *mockEventContext) Mentions() []string                     { return nil }
func (m *mockEventContext) Attachments() []contract.Attachment     { return nil }
func (m *mockEventContext) Scene() contract.MessageScene           { return contract.SceneGuild }
func (m *mockEventContext) Reply(string) error                     { return nil }
func (m *mockEventContext) ReplyMarkdown(content string) error     { return nil }
func (m *mockEventContext) ReplyImage(url string) error            { return nil }
func (m *mockEventContext) Role() string                                            { return "" }
func (m *mockEventContext) ReplyWithButtons(content string, buttons []contract.MessageButton) error { return nil }
func (m *mockEventContext) ReplyWithButtonRows(content string, rows [][]contract.MessageButton) error { return nil }
func (m *mockEventContext) ReplyArk(ark *contract.MessageArk) error { return nil }
func (m *mockEventContext) ReplyMarkdownTemplate(templateID string, params []contract.MarkdownParam) error { return nil }

// mockMiddleware is a simple middleware for testing.
type mockMiddleware struct {
	name  string
	order int
	hook  func() // called before next()
}

func (m *mockMiddleware) Name() string                       { return m.name }
func (m *mockMiddleware) Order() int                         { return m.order }
func (m *mockMiddleware) Handle(ctx contract.EventContext, next func() error) error {
	if m.hook != nil {
		m.hook()
	}
	return next()
}

// --- Tests ---

func TestNewChain(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Len() != 0 {
		t.Errorf("expected empty chain, got %d items", c.Len())
	}
}

func TestAddAndExecute(t *testing.T) {
	c := New()

	var called bool
	c.Add(&mockMiddleware{
		name:  "test",
		order: 0,
	})

	err := c.Execute(&mockEventContext{}, func() error {
		called = true
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !called {
		t.Error("final handler was not called")
	}
}

func TestMiddlewareOrder(t *testing.T) {
	c := New()

	var order []string
	c.Add(&mockMiddleware{
		name:  "second", order: 10,
		hook: func() { order = append(order, "second") },
	})
	c.Add(&mockMiddleware{
		name:  "first", order: -10,
		hook: func() { order = append(order, "first") },
	})
	c.Add(&mockMiddleware{
		name:  "middle", order: 0,
		hook: func() { order = append(order, "middle") },
	})

	_ = c.Execute(&mockEventContext{}, func() error {
		order = append(order, "final")
		return nil
	})

	expected := []string{"first", "middle", "second", "final"}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("step %d: expected %s, got %s", i, v, order[i])
		}
	}
}

func TestRemoveMiddleware(t *testing.T) {
	c := New()

	c.Add(&mockMiddleware{name: "keep", order: 0})
	c.Add(&mockMiddleware{name: "remove", order: 1})

	if c.Len() != 2 {
		t.Errorf("expected 2 middlewares, got %d", c.Len())
	}

	c.Remove("remove")

	if c.Len() != 1 {
		t.Errorf("expected 1 middleware after removal, got %d", c.Len())
	}

	list := c.List()
	if list[0].Name() != "keep" {
		t.Errorf("expected 'keep', got %s", list[0].Name())
	}
}

func TestRemoveNonexistent(t *testing.T) {
	c := New()
	c.Add(&mockMiddleware{name: "existing", order: 0})
	c.Remove("nonexistent")

	if c.Len() != 1 {
		t.Errorf("expected 1 middleware, got %d", c.Len())
	}
}

func TestExecuteEmptyChain(t *testing.T) {
	c := New()

	var called bool
	err := c.Execute(&mockEventContext{}, func() error {
		called = true
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !called {
		t.Error("final handler was not called with empty chain")
	}
}

func TestMiddlewareCanBlockNext(t *testing.T) {
	c := New()

	c.Add(&mockMiddleware{
		name:  "blocker", order: 0,
		hook: func() {},
	})
	// Override to return without calling next
	c.middlewares[0] = &blockingMiddleware{}

	var finalCalled bool
	_ = c.Execute(&mockEventContext{}, func() error {
		finalCalled = true
		return nil
	})

	if finalCalled {
		t.Error("final handler should not be called when middleware blocks")
	}
}

type blockingMiddleware struct{}

func (b *blockingMiddleware) Name() string                     { return "blocker" }
func (b *blockingMiddleware) Order() int                       { return 0 }
func (b *blockingMiddleware) Handle(ctx contract.EventContext, next func() error) error {
	return nil // intentionally not calling next()
}

func TestMiddlewareErrorPropagation(t *testing.T) {
	c := New()

	expectedErr := errors.New("middleware error")
	c.Add(&mockMiddleware{
		name:  "error", order: 0,
		hook: func() {},
	})
	c.middlewares[0] = &errorMiddleware{err: expectedErr}

	err := c.Execute(&mockEventContext{}, func() error { return nil })
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

type errorMiddleware struct {
	err error
}

func (e *errorMiddleware) Name() string  { return "error" }
func (e *errorMiddleware) Order() int    { return 0 }
func (e *errorMiddleware) Handle(ctx contract.EventContext, next func() error) error {
	return e.err
}

func TestListReturnsCopy(t *testing.T) {
	c := New()
	c.Add(&mockMiddleware{name: "mw1", order: 0})

	list1 := c.List()
	list2 := c.List()

	// Modifying list1 should not affect list2 or the chain
	if len(list1) > 0 {
		list1[0] = &mockMiddleware{name: "modified"}
	}

	if c.middlewares[0].Name() != "mw1" {
		t.Error("chain should not be affected by modifying returned list")
	}
	if list2[0].Name() != "mw1" {
		t.Error("list copy should not be affected")
	}
}

// --- LoggingMiddleware Tests ---

type testLogger struct {
	lastMsg string
}

func (l *testLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *testLogger) Info(msg string, keysAndValues ...interface{}) {
	l.lastMsg = msg
}
func (l *testLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *testLogger) Error(msg string, keysAndValues ...interface{}) {}

func TestLoggingMiddleware(t *testing.T) {
	logger := &testLogger{}
	mw := NewLoggingMiddleware(logger)

	err := mw.Handle(&mockEventContext{channelID: "ch1", content: "hello"}, func() error {
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoggingMiddlewareOrder(t *testing.T) {
	mw := NewLoggingMiddleware(&testLogger{})
	if mw.Order() != -1000 {
		t.Errorf("expected logging order -1000, got %d", mw.Order())
	}
}

// --- RateLimitMiddleware Tests ---

func TestRateLimitMiddlewareAllow(t *testing.T) {
	mw := NewRateLimitMiddleware(&noopLogger{})

	// First request should always be allowed (bucket starts full)
	called := false
	err := mw.Handle(&mockEventContext{channelID: "ch1"}, func() error {
		called = true
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}
}

func TestRateLimitMiddlewareBurst(t *testing.T) {
	// Create rate limiter with 10 burst
	mw := NewRateLimitMiddlewareWith(10, 10, &noopLogger{})

	// All 10 requests should be allowed (burst)
	for i := 0; i < 10; i++ {
		allowed := false
		_ = mw.Handle(&mockEventContext{channelID: "burst-ch"}, func() error {
			allowed = true
			return nil
		})
		if !allowed {
			t.Errorf("request %d should have been allowed (burst)", i+1)
		}
	}
}

func TestRateLimitMiddlewareExceedsBurst(t *testing.T) {
	// Create rate limiter with 5 burst
	mw := NewRateLimitMiddlewareWith(5, 5, &noopLogger{})

	// Consume all 5 tokens
	for i := 0; i < 5; i++ {
		_ = mw.Handle(&mockEventContext{channelID: "limit-ch"}, func() error { return nil })
	}

	// Next request should be blocked
	called := false
	_ = mw.Handle(&mockEventContext{channelID: "limit-ch"}, func() error {
		called = true
		return nil
	})

	if called {
		t.Error("handler should not be called when rate limited")
	}
}

func TestRateLimitPerChannel(t *testing.T) {
	mw := NewRateLimitMiddlewareWith(1, 1, &noopLogger{})

	// Consume token for ch1
	_ = mw.Handle(&mockEventContext{channelID: "ch1"}, func() error { return nil })

	// ch2 should still have its own token
	called := false
	_ = mw.Handle(&mockEventContext{channelID: "ch2"}, func() error {
		called = true
		return nil
	})

	if !called {
		t.Error("ch2 should have its own token bucket")
	}

	// ch1 should be blocked
	called2 := false
	_ = mw.Handle(&mockEventContext{channelID: "ch1"}, func() error {
		called2 = true
		return nil
	})

	if called2 {
		t.Error("ch1 should be rate limited")
	}
}

func TestRateLimitMiddlewareWithInvalidParams(t *testing.T) {
	// rate < 1 should be clamped to 1
	mw := NewRateLimitMiddlewareWith(0, 10, &noopLogger{})
	if mw.buckets.rate < 1 {
		t.Errorf("rate should be clamped to >= 1")
	}

	// burst < rate should be clamped to >= rate
	mw2 := NewRateLimitMiddlewareWith(10, 1, &noopLogger{})
	if mw2.buckets.burst < mw2.buckets.rate {
		t.Errorf("burst should be clamped to >= rate")
	}
}

func TestRateLimitRefill(t *testing.T) {
	mw := NewRateLimitMiddlewareWith(5, 5, &noopLogger{})

	// Consume all 5 tokens
	for i := 0; i < 5; i++ {
		_ = mw.Handle(&mockEventContext{channelID: "refill-ch"}, func() error { return nil })
	}

	// Should be limited now
	called := false
	_ = mw.Handle(&mockEventContext{channelID: "refill-ch"}, func() error {
		called = true
		return nil
	})
	if called {
		t.Error("should be limited after consuming all tokens")
	}

	// Wait for lazy refill (~200ms for 1 token at rate=5/s)
	time.Sleep(250 * time.Millisecond)

	// Should be allowed again (1 token refilled)
	called = false
	_ = mw.Handle(&mockEventContext{channelID: "refill-ch"}, func() error {
		called = true
		return nil
	})
	if !called {
		t.Error("should be allowed after refill")
	}
}

func TestConcurrentAccess(t *testing.T) {
	mw := NewRateLimitMiddlewareWith(100, 100, &noopLogger{})

	var counter int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mw.Handle(&mockEventContext{channelID: "concurrent-ch"}, func() error {
				atomic.AddInt32(&counter, 1)
				return nil
			})
		}()
	}
	wg.Wait()

	if counter == 0 {
		t.Error("expected some handlers to be called concurrently")
	}
}
