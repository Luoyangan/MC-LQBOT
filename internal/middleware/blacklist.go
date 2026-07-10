package middleware

import (
	"github.com/Luoyangan/LQBOT/internal/blacklist"
	"github.com/Luoyangan/LQBOT/internal/contract"
	"github.com/Luoyangan/LQBOT/internal/log"
)

// BlacklistMiddleware filters messages from blacklisted users/groups.
type BlacklistMiddleware struct {
	manager *blacklist.Manager
	logger  *log.Logger
}

// NewBlacklistMiddleware creates a middleware that drops events from blacklisted sources.
func NewBlacklistMiddleware(mgr *blacklist.Manager, logger *log.Logger) *BlacklistMiddleware {
	return &BlacklistMiddleware{manager: mgr, logger: logger}
}

func (m *BlacklistMiddleware) Name() string { return "blacklist" }
func (m *BlacklistMiddleware) Order() int   { return -1500 } // Before logging (-1000)

func (m *BlacklistMiddleware) Handle(ctx contract.EventContext, next func() error) error {
	if m.manager.IsBlocked(ctx) {
		m.logger.Debug("blocked blacklisted source",
			"author_id", ctx.AuthorID(),
			"group_id", ctx.GroupID(),
			"content", ctx.Content(),
		)
		return nil // silently drop
	}
	return next()
}
