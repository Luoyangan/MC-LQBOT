// Package permission provides role-based access control for commands.
package permission

import (
	"strings"

	"github.com/Luoyangan/LQBOT/internal/contract"
	"github.com/Luoyangan/LQBOT/internal/log"
)

// Permission levels (ordered from most to least restrictive).
const (
	LevelOwner  = "owner"  // 群主
	LevelAdmin  = "admin"  // 群管理员及以上（含群主）
	LevelMember = "member" // 所有群成员
	LevelPublic = "public" // 所有人（包括非群聊场景）
)

// levelRank maps permission level names to numeric rank (lower = more restrictive).
var levelRank = map[string]int{
	LevelOwner:  0,
	LevelAdmin:  1,
	LevelMember: 2,
	LevelPublic: 3,
}

// exactSuffix is appended to a base level name for exact-match mode.
const exactSuffix = "_exact"

// validLevels contains all recognized base level names.
var validLevels = map[string]bool{
	LevelOwner:  true,
	LevelAdmin:  true,
	LevelMember: true,
	LevelPublic: true,
}

// Checker checks command permissions against config mapping.
type Checker struct {
	logger      *log.Logger
	permissions map[string]string // command name → required permission level
}

// NewChecker creates a permission checker.
// permissions maps command names to required permission levels.
// Commands not in the map default to "public" (no restriction).
func NewChecker(permissions map[string]string, logger *log.Logger) *Checker {
	normalized := make(map[string]string, len(permissions))
	for cmd, level := range permissions {
		if !isValidLevel(level) {
			level = LevelPublic
		}
		normalized[cmd] = level
	}
	return &Checker{
		logger:      logger,
		permissions: normalized,
	}
}

// isValidLevel reports whether level is valid. Supports:
// - Single level: "admin", "owner", etc.
// - Exact suffix: "admin_exact", "owner_exact"
// - Comma list:  "owner,member", "admin,owner", etc.
func isValidLevel(level string) bool {
	if strings.Contains(level, ",") {
		for _, item := range strings.Split(level, ",") {
			item = strings.TrimSpace(item)
			if !isValidLevel(item) {
				return false
			}
		}
		return true
	}
	base := strings.TrimSuffix(level, exactSuffix)
	return validLevels[base]
}

// Check verifies that the user has permission to execute the command.
// Returns true if allowed, false if denied.
//
// Permission supports three modes:
//
//	Single string — hierarchical  (e.g. "admin"     → owner + admin)
//	"_exact"      — exact match   (e.g. "admin_exact"  → only admin)
//	Comma list    — role OR       (e.g. "owner,member" → owner or member, not admin)
func (c *Checker) Check(cmd *contract.Command, ctx contract.EventContext) bool {
	required, exists := c.permissions[cmd.Name]
	if !exists {
		for _, alias := range cmd.Aliases {
			if req, ok := c.permissions[alias]; ok {
				required = req
				exists = true
				break
			}
		}
	}
	if !exists {
		required = cmd.Permission
		if required == "" {
			return true
		}
	}

	userRole := ctx.Role()

	// ── Comma-separated list mode ──
	if strings.Contains(required, ",") {
		roles := strings.Split(required, ",")
		for _, r := range roles {
			if strings.TrimSpace(r) == userRole {
				return true
			}
		}
		// Also check if "public" is in the list for non-group contexts
		if _, ok := levelRank[userRole]; !ok {
			for _, r := range roles {
				if strings.TrimSpace(r) == LevelPublic {
					return true
				}
			}
		}
		c.logger.Warn("permission denied (role not in list)",
			"command", cmd.Name,
			"user_role", userRole,
			"required", required,
			"user", ctx.AuthorID(),
		)
		return false
	}

	// ── Exact-match mode (suffixed with _exact) ──
	exact := strings.HasSuffix(required, exactSuffix)
	requiredBase := strings.TrimSuffix(required, exactSuffix)
	requiredRank, ok := levelRank[requiredBase]
	if !ok {
		return true
	}
	if requiredRank >= levelRank[LevelPublic] {
		return true
	}

	userRank, hasRole := levelRank[userRole]
	if !hasRole {
		return requiredRank >= levelRank[LevelPublic]
	}

	if exact {
		if userRank != requiredRank {
			c.logger.Warn("permission denied (exact match)",
				"command", cmd.Name,
				"user_role", userRole,
				"required", required,
				"user", ctx.AuthorID(),
			)
			return false
		}
		return true
	}

	// ── Hierarchical mode (default) ──
	if userRank > requiredRank {
		c.logger.Warn("permission denied",
			"command", cmd.Name,
			"user_role", userRole,
			"required", required,
			"user", ctx.AuthorID(),
		)
		return false
	}

	return true
}
