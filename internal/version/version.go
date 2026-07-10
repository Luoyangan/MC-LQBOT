// Package version is the single source of truth for build metadata.
// Fields can be overridden at build time via -ldflags -X.
//
// Usage in Makefile:
//
//	GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
//	GIT_DATE   := $(shell git log -1 --format=%cd --date=format:'%Y%m%d' 2>/dev/null || echo "unknown")
//	go build -ldflags="-s -w \
//		-X 'github.com/Luoyangan/LQBOT/internal/version.Commit=$(GIT_COMMIT)' \
//		-X 'github.com/Luoyangan/LQBOT/internal/version.Date=$(GIT_DATE)'" -o LQBOT ./cmd/bot
package version

import "fmt"

// Build-time overridden values (set via -ldflags -X).
var (
	App     = "LQBOT"
	Version = "0.5.15"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a human-readable version string.
func String() string {
	return fmt.Sprintf("%s v%s (commit=%s, date=%s)", App, Version, Commit, Date)
}
