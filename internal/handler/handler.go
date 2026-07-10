// Package handler preprocesses messages before dispatching to commands/listeners.
// It handles @bot stripping, command parsing, and argument extraction.
package handler

import (
	"strings"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// CommandRouter routes incoming messages to matching commands.
type CommandRouter struct {
	commands map[string]contract.Command // name 鈫?command
	aliases  map[string]string           // alias 鈫?name
}

// NewCommandRouter creates a new CommandRouter.
func NewCommandRouter() *CommandRouter {
	return &CommandRouter{
		commands: make(map[string]contract.Command),
		aliases:  make(map[string]string),
	}
}

// Register adds a command to the router.
func (r *CommandRouter) Register(cmd contract.Command) {
	r.commands[cmd.Name] = cmd
	for _, alias := range cmd.Aliases {
		r.aliases[alias] = cmd.Name
	}
}

// Commands returns all registered commands.
func (r *CommandRouter) Commands() []contract.Command {
	var result []contract.Command
	for _, cmd := range r.commands {
		result = append(result, cmd)
	}
	return result
}

// Unregister removes a command by name.
func (r *CommandRouter) Unregister(name string) {
	cmd, ok := r.commands[name]
	if !ok {
		return
	}
	for _, alias := range cmd.Aliases {
		delete(r.aliases, alias)
	}
	delete(r.commands, name)
}

// Resolve attempts to find and execute a command from the given content.
// Returns the matched command and its arguments, or nil if no command matches.
func (r *CommandRouter) Resolve(content string) (*contract.Command, []string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}

	// Strip leading "/" if present
	trimmed := strings.TrimPrefix(content, "/")

	parts := splitArgs(trimmed)
	if len(parts) == 0 {
		return nil, nil
	}

	name := parts[0]
	args := parts[1:]

	// Try exact match
	if cmd, ok := r.commands[name]; ok {
		return &cmd, args
	}

	// Try alias match
	if fullName, ok := r.aliases[name]; ok {
		if cmd, ok := r.commands[fullName]; ok {
			return &cmd, args
		}
	}

	return nil, nil
}

// splitArgs splits a string into arguments, respecting quoted strings.
func splitArgs(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false

	for _, ch := range input {
		switch {
		case ch == '"':
			inQuote = !inQuote
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// IsMentioned checks if botID is in the mentions list (from dto.Message.Mentions).
// Returns the cleaned content (with @mention tags removed) and whether bot was mentioned.
func IsMentioned(rawContent string, botID string, mentionedIDs []string) (cleanContent string, wasMentioned bool) {
	// Check if botID is in the mentions list
	if botID != "" {
		for _, id := range mentionedIDs {
			if id == botID {
				wasMentioned = true
				break
			}
		}
	}

	// Remove all <@!userID> mention tags from content
	cleanContent = stripMentionTags(rawContent)

	// Also handle plain @username format (fallback)
	if !wasMentioned && strings.HasPrefix(strings.TrimSpace(rawContent), "@") {
		wasMentioned = true
		// Remove the leading @mention
		parts := strings.SplitN(strings.TrimSpace(rawContent), " ", 2)
		if len(parts) > 1 {
			cleanContent = parts[1]
		} else {
			cleanContent = ""
		}
	}

	return strings.TrimSpace(cleanContent), wasMentioned
}

// stripMentionTags removes all mention tags from the content.
// Supports all QQ API v2 formats:
//   - <@userID> (current format without !)
//   - <@!userID> (deprecated format)
//   - <qqbot-at-user id="userID" /> (new rich format)
func stripMentionTags(content string) string {
	var result strings.Builder
	for content != "" {
		// Try to find any of the three formats
		atBang := strings.Index(content, "<@!")
		atPlain := strings.Index(content, "<@")
		qqbot := strings.Index(content, "<qqbot-at-user")

		// Pick the earliest match
		start := -1
		for _, s := range []int{atBang, atPlain, qqbot} {
			if s != -1 && (start == -1 || s < start) {
				start = s
			}
		}

		if start == -1 {
			result.WriteString(content)
			break
		}

		result.WriteString(content[:start])
		rest := content[start:]
		end := strings.Index(rest, ">")
		if end == -1 {
			break
		}
		content = rest[end+1:]
	}
	return result.String()
}
