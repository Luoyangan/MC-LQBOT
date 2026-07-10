package handler

import (
	"errors"
	"testing"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// --- Tests for CommandRouter ---

func TestNewCommandRouter(t *testing.T) {
	r := NewCommandRouter()
	if r == nil {
		t.Fatal("NewCommandRouter() returned nil")
	}
	if r.commands == nil {
		t.Fatal("commands map not initialized")
	}
	if r.aliases == nil {
		t.Fatal("aliases map not initialized")
	}
}

func TestRegisterAndResolve(t *testing.T) {
	r := NewCommandRouter()

	var called bool
	cmd := contract.Command{
		Name:    "ping",
		Aliases: []string{"p"},
		Handler: func(ctx contract.CommandContext) error {
			called = true
			return nil
		},
	}

	r.Register(cmd)

	// Resolve by name
	matched, args := r.Resolve("ping")
	if matched == nil {
		t.Fatal("expected command to be resolved by name")
	}
	if matched.Name != "ping" {
		t.Errorf("expected 'ping', got %s", matched.Name)
	}
	if len(args) != 0 {
		t.Errorf("expected 0 args, got %d", len(args))
	}

	// Resolve with / prefix
	matched, args = r.Resolve("/ping")
	if matched == nil {
		t.Fatal("expected command to be resolved with / prefix")
	}
	if matched.Name != "ping" {
		t.Errorf("expected 'ping', got %s", matched.Name)
	}

	// Resolve by alias
	matched, _ = r.Resolve("p")
	if matched == nil {
		t.Fatal("expected command to be resolved by alias")
	}
	if matched.Name != "ping" {
		t.Errorf("expected 'ping' via alias, got %s", matched.Name)
	}

	// Verify handler works
	if matched.Handler != nil {
		_ = matched.Handler(nil) // just check it's callable
		if !called {
			t.Error("handler should have been called")
		}
	}
}

func TestResolveWithArgs(t *testing.T) {
	r := NewCommandRouter()
	r.Register(contract.Command{
		Name: "echo",
		Handler: func(ctx contract.CommandContext) error {
			return nil
		},
	})

	matched, args := r.Resolve("echo hello world")
	if matched == nil {
		t.Fatal("expected command to be resolved")
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[0] != "hello" {
		t.Errorf("expected arg[0]='hello', got '%s'", args[0])
	}
	if args[1] != "world" {
		t.Errorf("expected arg[1]='world', got '%s'", args[1])
	}
}

func TestResolveWithQuotedArgs(t *testing.T) {
	r := NewCommandRouter()
	r.Register(contract.Command{
		Name: "say",
		Handler: func(ctx contract.CommandContext) error {
			return nil
		},
	})

	matched, args := r.Resolve(`say "hello world" foo`)
	if matched == nil {
		t.Fatal("expected command to be resolved")
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[0] != "hello world" {
		t.Errorf("expected arg[0]='hello world', got '%s'", args[0])
	}
	if args[1] != "foo" {
		t.Errorf("expected arg[1]='foo', got '%s'", args[1])
	}
}

func TestResolveNonexistent(t *testing.T) {
	r := NewCommandRouter()
	r.Register(contract.Command{Name: "ping", Handler: func(ctx contract.CommandContext) error { return nil }})

	matched, _ := r.Resolve("nonexistent")
	if matched != nil {
		t.Error("expected nil for nonexistent command")
	}

	matched, _ = r.Resolve("")
	if matched != nil {
		t.Error("expected nil for empty content")
	}

	matched, _ = r.Resolve("   ")
	if matched != nil {
		t.Error("expected nil for whitespace-only content")
	}
}

func TestResolveWithSlashPrefix(t *testing.T) {
	r := NewCommandRouter()

	r.Register(contract.Command{
		Name: "help",
		Handler: func(ctx contract.CommandContext) error {
			return nil
		},
	})

	// With /
	matched, _ := r.Resolve("/help")
	if matched == nil {
		t.Fatal("expected /help to resolve")
	}

	// Without /
	matched, _ = r.Resolve("help")
	if matched == nil {
		t.Fatal("expected help to resolve")
	}

	// With / and args
	matched, args := r.Resolve("/help me please")
	if matched == nil {
		t.Fatal("expected /help me please to resolve")
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
}

func TestUnregister(t *testing.T) {
	r := NewCommandRouter()
	r.Register(contract.Command{
		Name:    "test",
		Aliases: []string{"t"},
		Handler: func(ctx contract.CommandContext) error { return nil },
	})

	// Verify registered
	if matched, _ := r.Resolve("test"); matched == nil {
		t.Fatal("command should be registered")
	}
	if matched, _ := r.Resolve("t"); matched == nil {
		t.Fatal("alias should work")
	}

	r.Unregister("test")

	// Verify unregistered
	if matched, _ := r.Resolve("test"); matched != nil {
		t.Error("command should be unregistered")
	}
	if matched, _ := r.Resolve("t"); matched != nil {
		t.Error("alias should be removed after unregister")
	}
}

func TestUnregisterNonexistent(t *testing.T) {
	r := NewCommandRouter()
	r.Register(contract.Command{Name: "keep", Handler: func(ctx contract.CommandContext) error { return nil }})
	r.Unregister("nonexistent")

	if matched, _ := r.Resolve("keep"); matched == nil {
		t.Error("existing command should still work after unregistering nonexistent")
	}
}

func TestAliasConflict(t *testing.T) {
	r := NewCommandRouter()
	r.Register(contract.Command{
		Name:    "first",
		Aliases: []string{"common"},
		Handler: func(ctx contract.CommandContext) error { return nil },
	})
	r.Register(contract.Command{
		Name:    "second",
		Aliases: []string{"common"}, // same alias, overrides previous
		Handler: func(ctx contract.CommandContext) error { return nil },
	})

	matched, _ := r.Resolve("common")
	if matched == nil {
		t.Fatal("alias should resolve")
	}
	// The second registration overwrites the alias mapping
	if matched.Name != "second" {
		t.Errorf("expected alias to point to 'second', got '%s'", matched.Name)
	}
}

// --- Tests for splitArgs ---

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"hello", []string{"hello"}},
		{"hello world", []string{"hello", "world"}},
		{`"hello world" foo`, []string{"hello world", "foo"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{`"quoted"`, []string{"quoted"}},
		{`multiple   spaces`, []string{"multiple", "spaces"}},
	}

	for _, tt := range tests {
		got := splitArgs(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitArgs(%q) = %v, want %v (len mismatch)", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitArgs(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestSplitArgsUnclosedQuote(t *testing.T) {
	// Unclosed quotes toggle quote mode; remaining content becomes one arg
	args := splitArgs(`echo "hello world`)
	if len(args) != 2 {
		t.Fatalf("expected 2 args with unclosed quote, got %d: %v", len(args), args)
	}
	if args[0] != "echo" {
		t.Errorf("expected arg[0]='echo', got '%s'", args[0])
	}
	if args[1] != "hello world" {
		t.Errorf("expected arg[1]='hello world', got '%s'", args[1])
	}
}

// --- Tests for PreprocessMessage ---

func TestIsMentionedNoMention(t *testing.T) {
	content, mentioned := IsMentioned("hello world", "123", nil)
	if content != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", content)
	}
	if mentioned {
		t.Error("should not be mentioned")
	}
}

func TestIsMentionedWithAtTag(t *testing.T) {
	content, mentioned := IsMentioned("<@!123> hello", "123", []string{"123"})
	if content != "hello" {
		t.Errorf("expected 'hello', got '%s'", content)
	}
	if !mentioned {
		t.Error("should be mentioned")
	}
}

func TestIsMentionedWithAtTagOnly(t *testing.T) {
	content, mentioned := IsMentioned("<@!123>", "123", []string{"123"})
	if content != "" {
		t.Errorf("expected empty string, got '%s'", content)
	}
	if !mentioned {
		t.Error("should be mentioned")
	}
}

func TestIsMentionedWithPlainAt(t *testing.T) {
	content, mentioned := IsMentioned("@bot hello", "", nil)
	if content != "hello" {
		t.Errorf("expected 'hello', got '%s'", content)
	}
	if !mentioned {
		t.Error("should be mentioned")
	}
}

func TestIsMentionedWithPlainAtOnly(t *testing.T) {
	content, mentioned := IsMentioned("@bot", "", nil)
	if content != "" {
		t.Errorf("expected empty string, got '%s'", content)
	}
	if !mentioned {
		t.Error("should be mentioned")
	}
}

func TestIsMentionedDifferentBotID(t *testing.T) {
	// Only matching botID should trigger mention
	content, mentioned := IsMentioned("<@!456> hello", "123", []string{"456"})
	if content != "hello" {
		t.Errorf("expected 'hello', got '%s'", content)
	}
	if mentioned {
		t.Error("should not mention bot with different ID")
	}
}

func TestIsMentionedTrimsWhitespace(t *testing.T) {
	content, mentioned := IsMentioned("  hello world  ", "", nil)
	if content != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", content)
	}
	if mentioned {
		t.Error("should not be mentioned")
	}
}

func TestIsMentionedEmpty(t *testing.T) {
	content, mentioned := IsMentioned("", "123", nil)
	if content != "" {
		t.Errorf("expected empty, got '%s'", content)
	}
	if mentioned {
		t.Error("should not be mentioned")
	}
}

// --- Integration: Router + Handler ---

func TestRouterIntegration(t *testing.T) {
	r := NewCommandRouter()

	// Register commands
	r.Register(contract.Command{
		Name:        "ping",
		Aliases:     []string{"p"},
		Description: "ping test",
		Handler: func(ctx contract.CommandContext) error {
			return ctx.Reply("Pong!")
		},
	})

	r.Register(contract.Command{
		Name: "echo",
		Handler: func(ctx contract.CommandContext) error {
			if ctx.ArgCount() == 0 {
				return errors.New("no args")
			}
			return ctx.Reply(ctx.Arg(0))
		},
	})

	// Test ping
	cmd, args := r.Resolve("/ping")
	if cmd == nil || cmd.Name != "ping" {
		t.Error("expected ping command")
	}
	if len(args) != 0 {
		t.Errorf("expected 0 args for ping, got %d", len(args))
	}

	// Test alias p
	cmd, _ = r.Resolve("p")
	if cmd == nil || cmd.Name != "ping" {
		t.Error("expected ping via alias p")
	}

	// Test echo with args
	cmd, args = r.Resolve("echo hello world")
	if cmd == nil || cmd.Name != "echo" {
		t.Error("expected echo command")
	}
	if len(args) != 2 || args[0] != "hello" || args[1] != "world" {
		t.Errorf("expected [hello world], got %v", args)
	}

	// Test no match
	cmd, _ = r.Resolve("unknown")
	if cmd != nil {
		t.Error("expected nil for unknown command")
	}
}
