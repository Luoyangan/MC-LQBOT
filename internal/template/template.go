// Package template provides a simple message template engine with variable substitution.
// Templates use {variable} syntax for placeholders.
//
// Built-in variables:
//
//	{bot.name}     — Bot name (default: LQBOT)
//	{user.name}    — User display name (from context)
//	{user.id}      — User ID (from context)
//	{time.now}     — Current time (format: "15:04:05")
//	{time.date}    — Current date (format: "2006-01-02")
//	{time.dt}      — Current date + time
//	{scene}        — Message scene: "guild" / "group" / "c2c"
//
// Usage:
//
//	tmpl := template.New()
//	msg := tmpl.Render("你好 {user.name}，现在是 {time.now}", ctx)
package template

import (
	"strings"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// Engine handles message template rendering.
type Engine struct {
	botName string
}

// New creates a template engine with the given bot name.
func New(botName string) *Engine {
	return &Engine{botName: botName}
}

// Render replaces {variable} placeholders with values from the context.
// Unknown variables are left as-is.
func (e *Engine) Render(tmpl string, ctx contract.EventContext) string {
	// Precompute replacements
	scene := "guild"
	switch ctx.Scene() {
	case contract.SceneGroup:
		scene = "group"
	case contract.SceneC2C:
		scene = "c2c"
	}

	now := time.Now()
	repl := map[string]string{
		"bot.name":  e.botName,
		"user.name": ctx.AuthorID(), // fallback to ID if no username available
		"user.id":   ctx.AuthorID(),
		"time.now":  now.Format("15:04:05"),
		"time.date": now.Format("2006-01-02"),
		"time.dt":   now.Format("2006-01-02 15:04:05"),
		"scene":     scene,
	}

	// Apply replacements
	result := tmpl
	for key, val := range repl {
		result = strings.ReplaceAll(result, "{"+key+"}", val)
	}

	return result
}

// RenderSimple replaces variables without requiring a context.
// Only built-in time variables are available.
func (e *Engine) RenderSimple(tmpl string) string {
	now := time.Now()
	repl := map[string]string{
		"bot.name":  e.botName,
		"time.now":  now.Format("15:04:05"),
		"time.date": now.Format("2006-01-02"),
		"time.dt":   now.Format("2006-01-02 15:04:05"),
	}

	result := tmpl
	for key, val := range repl {
		result = strings.ReplaceAll(result, "{"+key+"}", val)
	}
	return result
}
