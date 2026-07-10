package bus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// --- Mock Types ---

type mockEventContext struct {
	content string
}

func (m *mockEventContext) Content() string                    { return m.content }
func (m *mockEventContext) RawContent() string                 { return m.content }
func (m *mockEventContext) ChannelID() string                  { return "test-channel" }
func (m *mockEventContext) AuthorID() string                   { return "test-user" }
func (m *mockEventContext) MessageID() string                  { return "test-msg" }
func (m *mockEventContext) IsMentioned() bool                  { return false }
func (m *mockEventContext) GuildID() string                    { return "" }
func (m *mockEventContext) GroupID() string                    { return "" }
func (m *mockEventContext) Mentions() []string                 { return nil }
func (m *mockEventContext) Attachments() []contract.Attachment { return nil }
func (m *mockEventContext) Scene() contract.MessageScene       { return contract.SceneGuild }
func (m *mockEventContext) Reply(string) error                 { return nil }
func (m *mockEventContext) ReplyMarkdown(content string) error { return nil }
func (m *mockEventContext) ReplyImage(url string) error        { return nil }
func (m *mockEventContext) ReplyWithButtons(content string, buttons []contract.MessageButton) error {
	return nil
}
func (m *mockEventContext) ReplyWithButtonRows(content string, rows [][]contract.MessageButton) error {
	return nil
}
func (m *mockEventContext) Role() string                                          { return "" }
func (m *mockEventContext) ReplyArk(ark *contract.MessageArk) error                { return nil }
func (m *mockEventContext) ReplyMarkdownTemplate(templateID string, params []contract.MarkdownParam) error {
	return nil
}

// --- Tests ---

func TestNew(t *testing.T) {
	eb := New()
	if eb == nil {
		t.Fatal("New() returned nil")
	}
	if eb.listeners == nil {
		t.Fatal("listeners map not initialized")
	}
}

func TestSubscribeAndPublish(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var called int32
	listener := contract.Listener{
		Event: "message.create",
		Handler: func(ctx contract.EventContext) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	}

	eb.Subscribe(listener)
	eb.Publish(ctx, "message.create", &mockEventContext{content: "hello"})

	if called != 1 {
		t.Errorf("expected handler to be called 1 time, got %d", called)
	}
}

func TestPublishNoListeners(t *testing.T) {
	eb := New()
	ctx := context.Background()

	// Should not panic
	eb.Publish(ctx, "nonexistent", &mockEventContext{content: "test"})
}

func TestPublishMultipleListeners(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var callCount int32
	handler := func(ctx contract.EventContext) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	}

	// Register multiple listeners for the same event
	eb.Subscribe(contract.Listener{Event: "message.create", Handler: handler})
	eb.Subscribe(contract.Listener{Event: "message.create", Handler: handler})
	eb.Subscribe(contract.Listener{Event: "message.create", Handler: handler})

	eb.Publish(ctx, "message.create", &mockEventContext{})

	if callCount != 3 {
		t.Errorf("expected 3 handler calls, got %d", callCount)
	}
}

func TestListenerOrder(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var order []int
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Order: 10,
		Handler: func(ctx contract.EventContext) error {
			order = append(order, 10)
			return nil
		},
	})
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Order: -5,
		Handler: func(ctx contract.EventContext) error {
			order = append(order, -5)
			return nil
		},
	})
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Order: 0,
		Handler: func(ctx contract.EventContext) error {
			order = append(order, 0)
			return nil
		},
	})

	eb.Publish(ctx, "test.event", &mockEventContext{})

	expected := []int{-5, 0, 10}
	if len(order) != len(expected) {
		t.Fatalf("expected order %v, got %v", expected, order)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("position %d: expected %d, got %d", i, v, order[i])
		}
	}
}

func TestHandlerErrorContinues(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var secondCalled bool
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Handler: func(ctx contract.EventContext) error {
			return errors.New("handler error")
		},
	})
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Handler: func(ctx contract.EventContext) error {
			secondCalled = true
			return nil
		},
	})

	eb.Publish(ctx, "test.event", &mockEventContext{})

	if !secondCalled {
		t.Error("expected second handler to still be called after first handler error")
	}
}

func TestUnsubscribe(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var called int32
	eb.Subscribe(contract.Listener{
		Event: "message.create",
		Handler: func(ctx contract.EventContext) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	})

	removed := eb.Unsubscribe("message.create")
	if removed != 1 {
		t.Errorf("expected 1 listener removed, got %d", removed)
	}

	eb.Publish(ctx, "message.create", &mockEventContext{})
	if called != 0 {
		t.Error("expected handler not to be called after unsubscribe")
	}
}

func TestUnsubscribeNonexistent(t *testing.T) {
	eb := New()

	removed := eb.Unsubscribe("nonexistent")
	if removed != 0 {
		t.Errorf("expected 0, got %d", removed)
	}
}

func TestClose(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var called int32
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Handler: func(ctx contract.EventContext) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	})

	eb.Close()

	// After close, Publish should still work (not panic) but listeners were cleared
	eb.Publish(ctx, "test.event", &mockEventContext{})
	if called != 0 {
		t.Error("expected handler not to be called after close")
	}
}

func TestPublishRespectsContextCancellation(t *testing.T) {
	eb := New()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	var called int32
	eb.Subscribe(contract.Listener{
		Event: "test.event",
		Handler: func(ctx contract.EventContext) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	})

	eb.Publish(ctx, "test.event", &mockEventContext{})
	if called != 0 {
		t.Error("expected handler not to be called with cancelled context")
	}
}

func TestPublishToDifferentEvents(t *testing.T) {
	eb := New()
	ctx := context.Background()

	var msgCount, guildCount int32

	eb.Subscribe(contract.Listener{
		Event: "message.create",
		Handler: func(ctx contract.EventContext) error {
			atomic.AddInt32(&msgCount, 1)
			return nil
		},
	})
	eb.Subscribe(contract.Listener{
		Event: "guild.create",
		Handler: func(ctx contract.EventContext) error {
			atomic.AddInt32(&guildCount, 1)
			return nil
		},
	})

	eb.Publish(ctx, "message.create", &mockEventContext{})
	eb.Publish(ctx, "guild.create", &mockEventContext{})

	if msgCount != 1 {
		t.Errorf("expected msgCount=1, got %d", msgCount)
	}
	if guildCount != 1 {
		t.Errorf("expected guildCount=1, got %d", guildCount)
	}
}

func TestSortListeners(t *testing.T) {
	listeners := []contract.Listener{
		{Event: "test", Order: 10},
		{Event: "test", Order: 0},
		{Event: "test", Order: -5},
		{Event: "test", Order: 5},
	}

	sortListeners(listeners)

	expected := []int{-5, 0, 5, 10}
	for i, l := range listeners {
		if l.Order != expected[i] {
			t.Errorf("position %d: expected order %d, got %d", i, expected[i], l.Order)
		}
	}
}
