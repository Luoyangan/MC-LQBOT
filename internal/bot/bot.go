// Package bot implements the core Bot that ties all framework components together.
package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Luoyangan/LQBOT/internal/adapter"
	"github.com/Luoyangan/LQBOT/internal/admin"
	fwblacklist "github.com/Luoyangan/LQBOT/internal/blacklist"
	"github.com/Luoyangan/LQBOT/internal/bus"
	"github.com/Luoyangan/LQBOT/internal/cache"
	"github.com/Luoyangan/LQBOT/internal/config"
	"github.com/Luoyangan/LQBOT/internal/contract"
	"github.com/Luoyangan/LQBOT/internal/handler"
	"github.com/Luoyangan/LQBOT/internal/httpsrv"
	framelog "github.com/Luoyangan/LQBOT/internal/log"
	"github.com/Luoyangan/LQBOT/internal/middleware"
	"github.com/Luoyangan/LQBOT/internal/permission"
	"github.com/Luoyangan/LQBOT/internal/scheduler"
	"github.com/Luoyangan/LQBOT/internal/storage"
	fwtmpl "github.com/Luoyangan/LQBOT/internal/template"
	"github.com/Luoyangan/LQBOT/internal/types"
	"github.com/Luoyangan/LQBOT/internal/utils"
	"github.com/Luoyangan/LQBOT/internal/version"
	"github.com/Luoyangan/LQBOT/plugins/online"
	"github.com/Luoyangan/LQBOT/plugins/onlinetime"
	"github.com/Luoyangan/LQBOT/plugins/whitelist"

	// <--new-plugin-import-here
	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/constant"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/dto/keyboard"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"golang.org/x/oauth2"
)

// Bot is the core framework instance.
type Bot struct {
	cfg      *types.Config
	logger   *framelog.Logger
	storage  *storage.Storage
	eventBus *bus.EventBus
	mwChain  *middleware.Chain
	adapter  contract.Adapter
	router   *handler.CommandRouter
	api      *qqAPIImpl

	// Rate limiter (for lifecycle management)
	rateLimiter *middleware.RateLimitMiddleware

	// Scheduler (cron/interval tasks) — nil if no tasks registered
	scheduler *scheduler.Scheduler

	// HTTP server (embedded) — nil if disabled
	httpSrv *httpsrv.Server

	// Admin panel — nil if disabled
	adminPanel *admin.Admin

	// Permission checker
	permChecker *permission.Checker

	// Blacklist manager
	blacklistMgr *fwblacklist.Manager

	// In-memory cache
	cache *cache.Cache

	// Template engine
	templateEngine *fwtmpl.Engine

	// Plugins that use the Plugin interface (Init-based lifecycle)
	plugins []contract.Plugin

	// Runtime stats
	botStartTime time.Time
	cmdCount     int64

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Log cleanup
	logCleanupRunning chan struct{} // closed when cleanup goroutine exits
}

// NewFromConfig creates a Bot instance from a configuration file path.
func NewFromConfig(configPath string) (*Bot, error) {
	configPath = config.Init(configPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return New(cfg)
}

// New creates a Bot instance from a parsed Config.
func New(cfg *types.Config) (*Bot, error) {
	ensureDirs("data")

	// Initialize logger
	logger := framelog.NewWithConfig(cfg.LogLevel, cfg.LogNoColor)

	// Bridge botgo logger to our zerolog logger
	botgo.SetLogger(&botgoLogger{logger})

	// Initialize storage
	store, err := storage.New(cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}

	// Attach database writer to logger so all logs are also saved to DB
	logger.SetDBWriter(store, "bot", cfg.LogLevelDB)
	if len(cfg.LogDBExclude) > 0 {
		logger.SetDBExclude(cfg.LogDBExclude)
	}
	logger.Info("database logging enabled",
		"dsn", cfg.Storage.DSN,
		"db_level", cfg.LogLevelDB,
		"db_exclude", cfg.LogDBExclude,
	)

	// Initialize API client
	api := &qqAPIImpl{
		appID:     cfg.AppID,
		appSecret: cfg.AppSecret,
		sandbox:   cfg.Sandbox,
		logger:    logger,
	}

	// Initialize event bus
	eb := bus.New()

	// Initialize middleware chain
	mwChain := middleware.New()
	mwChain.Add(middleware.NewLoggingMiddleware(logger))
	rl := middleware.NewRateLimitMiddleware(logger)
	mwChain.Add(rl)

	// Initialize command router
	router := handler.NewCommandRouter()

	// Initialize in-memory cache
	fwCache := cache.New(5 * time.Minute)

	// Initialize blacklist manager
	blMgr := fwblacklist.New(store, logger)

	// Initialize template engine
	tmplEngine := fwtmpl.New("LQBOT")

	ctx, cancel := context.WithCancel(context.Background())

	bot := &Bot{
		cfg:               cfg,
		logger:            logger,
		storage:           store,
		eventBus:          eb,
		mwChain:           mwChain,
		router:            router,
		api:               api,
		rateLimiter:       rl,
		scheduler:         scheduler.New(),
		permChecker:       permission.NewChecker(cfg.Permissions, logger),
		blacklistMgr:      blMgr,
		cache:             fwCache,
		templateEngine:    tmplEngine,
		botStartTime:      time.Now(),
		ctx:               ctx,
		cancel:            cancel,
		logCleanupRunning: make(chan struct{}),
	}

	// Add blacklist middleware (early in chain, before logging)
	mwChain.Add(middleware.NewBlacklistMiddleware(blMgr, logger))

	// Initialize embedded HTTP server if enabled
	if cfg.HTTP.Enabled {
		bot.httpSrv = httpsrv.New(cfg.HTTP.Port, cfg.HTTP.CertFile, cfg.HTTP.KeyFile, logger)
		// Initialize admin panel if enabled
		if cfg.HTTP.Admin {
			bot.adminPanel = admin.New(store, logger, bot.httpSrv.ServeMux(), admin.VersionInfo{
				App:     version.App,
				Version: version.Version,
				Commit:  version.Commit,
				Date:    version.Date,
			}, func() error {
				// Restart: graceful shutdown then re-execute
				bot.logger.Warn("restarting via admin panel")
				go bot.Restart()
				return nil
			}, func() {
				// Shutdown: graceful shutdown then exit
				bot.logger.Warn("shutting down via admin panel")
				bot.shutdown()
				os.Exit(0)
			}, bot.scheduler)
		}
	}

	// Register plugins (commands + event listeners)
	bot.RegisterPlugin(&whitelist.WhitelistPlugin{})
	bot.RegisterPlugin(&online.OnlinePlugin{})
	bot.RegisterPlugin(&onlinetime.OnlineTimePlugin{})
	bot.registerPlugins()
	bot.initPluginSystem()

	// Pass registered commands to admin panel
	if bot.adminPanel != nil {
		bot.adminPanel.SetCommands(bot.router.Commands())
	}

	return bot, nil
}

// registerPlugins imports and registers all plugin packages.
// Each plugin's Register() is called with the narrow interfaces it needs.
// Available narrow interfaces: CommandRegister, ListenerRegister, QQAPI,
// Scheduler, HTTPServer (nil when disabled), and json.RawMessage for plugin config.
// RegisterPlugin registers a plugin that implements the Plugin interface.
// Its Init() method will be called during bot startup.
func (b *Bot) RegisterPlugin(p contract.Plugin) {
	b.plugins = append(b.plugins, p)
}

// initPluginSystem calls Init() on all registered Plugin interface plugins.
// This must be called AFTER registerPlugins() so all Register() calls are done first.
func (b *Bot) initPluginSystem() {
	for _, p := range b.plugins {
		pc := &contract.PluginContext{
			Commands:  b.router,
			Listeners: b.eventBus,
			Logger:    b.logger,
			Storage:   b.storage,
			QQAPI:     b.api,
			Scheduler: b.scheduler,
		}

		// Inject plugin config from config.yaml if available
		if b.cfg.Plugins != nil {
			if cfg, ok := b.cfg.Plugins[p.Name()]; ok {
				pc.PluginConfig = cfg
			}
			// Inject shared config from config.plugins.mc
			if mcCfg, ok := b.cfg.Plugins["mc"]; ok {
				pc.SharedConfig = mcCfg
			}
		}

		// Inject HTTP server if enabled
		if b.httpSrv != nil {
			pc.HTTPServer = b.httpSrv
		}

		if err := p.Init(pc); err != nil {
			b.logger.Error("plugin init failed", "plugin", p.Name(), "error", err)
		} else {
			b.logger.Info("plugin initialized", "plugin", p.Name())
		}
	}
}

func (b *Bot) registerPlugins() {
	// TODO: add plugin registrations here
	// Example: myplugin.Register(b.router)
}

// Run starts the bot and blocks until shutdown.
// Returns nil on normal shutdown (SIGINT/SIGTERM), error on startup failure.
func (b *Bot) Run() error {
	b.logger.Info(version.String()+" starting",
		"access_type", b.cfg.AccessType,
	)

	// 1. Initialize adapter
	if err := b.initAdapter(); err != nil {
		return fmt.Errorf("init adapter: %w", err)
	}

	// 2. Start adapter (connect to QQ)
	if err := b.adapter.Start(b.ctx); err != nil {
		return fmt.Errorf("start adapter: %w", err)
	}

	// 3. Initialize API client with OpenAPI
	b.api.initOpenAPI()

	// 4. Start scheduler (cron/interval tasks)
	b.scheduler.Start()

	// 5. Start embedded HTTP server if enabled
	if b.httpSrv != nil {
		if err := b.httpSrv.Start(); err != nil {
			return fmt.Errorf("start http server: %w", err)
		}
	}

	// 6. Start log cleanup goroutine (if enabled)
	b.startLogCleanup()

	// 7. Start event processing loop
	b.wg.Add(1)
	go b.eventLoop()

	// 8. Update admin panel status
	if b.adminPanel != nil {
		b.adminPanel.SetBotStatus(admin.BotStatus{
			Running:   true,
			Adapter:   true,
			Database:  b.storage.Driver(),
			HTTPServe: b.httpSrv != nil,
		})
	}

	b.logger.Info("LQBOT is running. Press Ctrl+C to stop.")

	// 9. Wait for shutdown signal
	sig := utils.WaitForSignal()
	b.logger.Info("received signal, shutting down", "signal", sig)

	// 7. Graceful shutdown
	b.shutdown()
	return nil
}

// initAdapter creates the appropriate adapter based on config.
func (b *Bot) initAdapter() error {
	switch b.cfg.AccessType {
	case types.AccessWebSocket:
		ada, err := adapter.NewWebSocketAdapter(b.cfg.AppID, b.cfg.AppSecret, b.cfg.Sandbox, b.cfg.Intents, b.logger)
		if err != nil {
			return err
		}
		b.adapter = ada

	case types.AccessWebhook:
		b.adapter = adapter.NewWebhookAdapter(b.cfg.AppID, b.cfg.AppSecret, b.cfg.Webhook.Port, b.cfg.Webhook.Path, b.logger)

	default:
		return fmt.Errorf("unsupported access type: %s", b.cfg.AccessType)
	}
	return nil
}

// eventLoop processes events from the adapter.
func (b *Bot) eventLoop() {
	defer b.wg.Done()

	events := b.adapter.Events()
	for {
		select {
		case <-b.ctx.Done():
			return
		case raw, ok := <-events:
			if !ok {
				return
			}
			b.processRawEvent(raw)
		}
	}
}

// rawEvent uses the same format as QQ WSPayload: {"t":"EVENT_TYPE","d":<data>}
type rawEvent struct {
	T string          `json:"t"` // Native QQ API event type, e.g. "MESSAGE_CREATE"
	D json.RawMessage `json:"d"` // Event payload (dto.Message, dto.WSInteractionData, etc.)
}

// processRawEvent handles a raw event from the adapter using native QQ event types.
func (b *Bot) processRawEvent(raw []byte) {
	var evt rawEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		b.logger.Error("failed to parse event", "error", err)
		return
	}

	// Dispatch by native QQ event type
	switch evt.T {
	case types.EventInteractionCreate:
		b.processInteractionEvent(evt.D)

	// Message events — have content, author, message_id
	case types.EventMessageCreate,
		types.EventAtMessageCreate,
		types.EventGroupAtMessageCreate,
		types.EventGroupMessageCreate,
		types.EventC2CMessageCreate,
		types.EventDirectMsgCreate:
		b.processMessageEvent(evt.T, evt.D)

	// Guild/system events — forward to event bus
	case types.EventGuildCreate,
		types.EventGuildUpdate,
		types.EventGuildDelete,
		types.EventChannelCreate,
		types.EventChannelUpdate,
		types.EventChannelDelete,
		types.EventMemberJoin,
		types.EventMemberUpdate,
		types.EventMemberLeave,
		types.EventMessageDelete,
		types.EventPublicMessageDel,
		types.EventDirectMsgDelete,
		types.EventReactionAdd,
		types.EventReactionRemove,
		types.EventFriendAdd,
		types.EventFriendDel,
		types.EventSubscribeMsgStatus,
		types.EventEnterAIO,
		types.EventAuditPass,
		types.EventAuditReject,
		types.EventForumThreadCreate,
		types.EventForumThreadUpdate,
		types.EventForumThreadDelete,
		types.EventForumPostCreate,
		types.EventForumPostDelete,
		types.EventForumReplyCreate,
		types.EventForumReplyDelete,
		types.EventForumAuditResult,
		types.EventAudioStart,
		types.EventAudioFinish,
		types.EventAudioOnMic,
		types.EventAudioOffMic:
		b.processGuildEvent(evt.T, evt.D)

	default:
		b.logger.Debug("unhandled event type", "type", evt.T)
	}

	// Push to admin SSE stream
	if b.adminPanel != nil && evt.T != "" {
		b.adminPanel.PublishEvent("event", evt.T)
	}
}

// processMessageEvent handles message-type events using native QQ event types.
func (b *Bot) processMessageEvent(eventType string, rawData json.RawMessage) {
	var msg dto.Message
	if err := json.Unmarshal(rawData, &msg); err != nil {
		b.logger.Error("failed to parse message data", "error", err)
		return
	}

	// Extract member role from raw JSON for logging and event context
	memberRole := extractMemberRole(rawData)

	// Structured log with event context
	b.logger.LogEvent("info", "received message", types.LogEventContext{
		EventType:  eventType,
		ChannelID:  msg.ChannelID,
		GuildID:    msg.GuildID,
		GroupID:    msg.GroupID,
		AuthorID:   msg.Author.ID,
		AuthorName: msg.Author.Username,
		MemberRole: memberRole,
		MessageID:  msg.ID,
	}, "content", msg.Content)

	// Daily stats — route by scene
	if msg.GuildID != "" {
		// Channel/guild message
		_ = b.storage.RecordChannelIncoming(msg.ChannelID)
		if newCh, _ := b.storage.IsNewChannelToday(msg.ChannelID); newCh {
			_ = b.storage.RecordChannelNewChannel()
		}
	} else if msg.GroupID != "" {
		// Group message
		_ = b.storage.RecordGroupIncoming(msg.Author.ID, msg.GroupID)
		if newGrp, _ := b.storage.IsNewGroupToday(msg.GroupID); newGrp {
			_ = b.storage.RecordGroupNewGroup()
		}
	} else {
		// C2C message
		_ = b.storage.RecordC2CIncoming(msg.Author.ID)
		if newUsr, _ := b.storage.IsNewUserToday(msg.Author.ID); newUsr {
			_ = b.storage.RecordC2CNewUser()
		}
	}

	// Record users, groups, and channels for tracking
	// Separate channel user IDs from group/C2C user IDs (different namespaces)
	if msg.Author.ID != "" {
		if msg.GuildID != "" {
			// Channel/guild context — different ID namespace
			_ = b.storage.RecordChannelUser(msg.Author.ID, msg.GuildID, msg.ChannelID, msg.Author.Username, msg.Content)
		} else {
			// Group chat or C2C — same ID namespace
			scene := "c2c"
			if msg.GroupID != "" {
				scene = "group"
			}
			_ = b.storage.RecordUser(msg.Author.ID, msg.Author.Username, msg.Content, scene)
		}
	}
	if msg.GroupID != "" {
		_ = b.storage.RecordGroup(msg.GroupID)
	}
	if msg.ChannelID != "" {
		_ = b.storage.RecordChannel(msg.ChannelID, msg.GuildID)
	}

	eventCtx := newEventContext(eventType, &msg, rawData, b.api, b.adapter.BotUserID(), func(scene string) {
		switch scene {
		case "c2c":
			_ = b.storage.RecordC2COutgoing()
		case "group":
			_ = b.storage.RecordGroupOutgoing()
		case "channel":
			_ = b.storage.RecordChannelOutgoing()
		}
	})

	// Run through middleware chain with timeout, then dispatch
	timeout := 30 * time.Second
	mwCtx, mwCancel := context.WithTimeout(b.ctx, timeout)
	defer mwCancel()

	done := make(chan error, 1)
	go func() {
		done <- b.mwChain.Execute(eventCtx, func() error {
			// Try command routing first
			cmd, args := b.router.Resolve(eventCtx.Content())
			if cmd != nil {
				return b.executeCommand(cmd, args, eventCtx)
			}

			// Otherwise dispatch to event listeners
			b.eventBus.Publish(b.ctx, eventType, eventCtx)
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			b.logger.Warn("event processing error", "error", err, "event_type", eventType)
		}
	case <-mwCtx.Done():
		b.logger.Warn("event processing timed out", "event_type", eventType, "timeout", timeout)
	}
}

// processGuildEvent handles guild/member events by forwarding to event bus.
func (b *Bot) processGuildEvent(eventType string, rawData json.RawMessage) {
	// Try to extract guild ID from raw data for structured logging
	var guildMeta struct {
		ID      string `json:"id"`
		GuildID string `json:"guild_id"`
	}
	_ = json.Unmarshal(rawData, &guildMeta)
	guildID := guildMeta.ID
	if guildMeta.GuildID != "" {
		guildID = guildMeta.GuildID
	}

	b.logger.LogEvent("info", "received guild event", types.LogEventContext{
		EventType: eventType,
		GuildID:   guildID,
	})

	// Daily stats — track guild lifecycle events
	switch eventType {
	case types.EventGuildCreate:
		_ = b.storage.RecordChannelNewChannel()
		_ = b.storage.RecordGroupNewGroup()
	case types.EventGuildDelete:
		_ = b.storage.RecordChannelRemoved()
		_ = b.storage.RecordGroupRemoved()
	default:
		// member join/leave, message_delete — no daily counter
	}

	// Record channel/guild from guild event
	if guildID != "" {
		_ = b.storage.RecordChannel(guildID, guildID)
	}

	// Create a minimal event context for guild events
	ctx := &guildEventContext{eventType: eventType, api: b.api}
	b.eventBus.Publish(b.ctx, eventType, ctx)
}

// processInteractionEvent handles interaction.create events (button clicks, etc.).
func (b *Bot) processInteractionEvent(rawData json.RawMessage) {
	var data dto.WSInteractionData
	if err := json.Unmarshal(rawData, &data); err != nil {
		b.logger.Error("failed to parse interaction data", "error", err)
		return
	}

	b.logger.LogEvent("info", "received interaction", types.LogEventContext{
		EventType: types.EventInteractionCreate,
		ChannelID: data.ChannelID,
		GuildID:   data.GuildID,
		GroupID:   data.GroupOpenID,
		AuthorID:  data.UserOpenID,
	})

	// Daily stats
	_ = b.storage.RecordDailyInteraction()

	// Record user and group from interaction
	if data.UserOpenID != "" {
		_ = b.storage.RecordUser(data.UserOpenID, "", "", "c2c")
	}
	if data.GroupOpenID != "" {
		_ = b.storage.RecordGroup(data.GroupOpenID)
	}

	ictx := newInteractionContext(&data, b.api)
	b.eventBus.Publish(b.ctx, types.EventInteractionCreate, ictx)
}

// executeCommand runs a matched command with permission check and timing.
func (b *Bot) executeCommand(cmd *contract.Command, args []string, ctx contract.EventContext) error {
	// Debug: log role info before permission check
	b.logger.Debug("permission check",
		"command", cmd.Name,
		"user_role", ctx.Role(),
		"author_id", ctx.AuthorID(),
		"scene", ctx.Scene(),
		"group_id", ctx.GroupID(),
	)

	// Permission check
	if !b.permChecker.Check(cmd, ctx) {
		return nil // silently denied
	}

	// Daily stats
	_ = b.storage.RecordDailyCommand()

	// Push to admin SSE
	if b.adminPanel != nil {
		b.adminPanel.PublishEvent("cmd", "/"+cmd.Name)
	}

	cmdCtx := &commandContextImpl{
		args:         args,
		EventContext: ctx,
	}

	// Command execution with timing and panic recovery
	start := time.Now()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in command handler: %v", r)
				b.logger.Error("command panic recovered",
					"command", cmd.Name,
					"panic", r,
					"author_id", ctx.AuthorID(),
				)
			}
		}()
		err = cmd.Handler(cmdCtx)
	}()
	duration := time.Since(start)

	// Track command count
	_ = atomic.AddInt64(&b.cmdCount, 1)

	// Log slow commands (>1s)
	if duration > 1*time.Second {
		b.logger.Warn("slow command",
			"command", cmd.Name,
			"duration", duration.String(),
			"author_id", ctx.AuthorID(),
			"scene", int(ctx.Scene()),
		)
	} else {
		b.logger.Debug("command executed",
			"command", cmd.Name,
			"duration", duration.String(),
		)
	}

	return err
}

// startLogCleanup starts a goroutine that periodically cleans up old log entries.
// The cleanup interval and retention period are configured in config.yaml (storage.log_cleanup).
// No-op if log cleanup is disabled.
func (b *Bot) startLogCleanup() {
	lc := b.cfg.Storage.LogCleanup
	if !lc.Enabled {
		b.logger.Debug("log cleanup is disabled")
		return
	}

	interval, err := time.ParseDuration(lc.Interval)
	if err != nil {
		b.logger.Warn("invalid log cleanup interval, using default 24h", "error", err)
		interval = 24 * time.Hour
	}

	retainDays := lc.RetainDays
	if retainDays <= 0 {
		retainDays = 30
	}

	b.logger.Info("log cleanup started",
		"interval", interval.String(),
		"retain_days", retainDays,
	)

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer close(b.logCleanupRunning)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Run cleanup immediately on start
		b.doLogCleanup(retainDays)

		for {
			select {
			case <-b.ctx.Done():
				return
			case <-ticker.C:
				b.doLogCleanup(retainDays)
			}
		}
	}()
}

// doLogCleanup performs a single log cleanup cycle.
func (b *Bot) doLogCleanup(retainDays int) {
	deleted, err := b.storage.CleanupLogsByRetentionDays(retainDays)
	if err != nil {
		b.logger.Error("log cleanup failed", "error", err)
		return
	}
	if deleted > 0 {
		b.logger.Debug("log cleanup completed", "deleted", deleted)
	}
}

// shutdown performs graceful shutdown of all components.
func (b *Bot) shutdown() {
	b.logger.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Stop scheduler
	b.scheduler.Stop()

	// 2. Stop HTTP server
	if b.httpSrv != nil {
		if err := b.httpSrv.Stop(shutdownCtx); err != nil {
			b.logger.Error("HTTP server stop error", "error", err)
		}
	}

	// 3. Stop adapter
	if b.adapter != nil {
		if err := b.adapter.Stop(shutdownCtx); err != nil {
			b.logger.Error("adapter stop error", "error", err)
		}
	}

	// 4. Cancel main context (stops event loop + log cleanup)
	b.cancel()

	// 5. Wait for goroutines to finish (event loop, cleanup, etc.) with timeout
	doneCh := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		b.logger.Warn("shutdown: goroutines did not exit within 5s, continuing")
	}

	// 6. Close event bus
	b.eventBus.Close()

	// 7. Close storage
	if b.storage != nil {
		if err := b.storage.Close(); err != nil {
			b.logger.Error("storage close error", "error", err)
		}
	}

	// 6. Close rate limiter (stops cleanup goroutine)
	if b.rateLimiter != nil {
		b.rateLimiter.Close()
	}

	b.logger.Info("shutdown complete")
}

// Restart performs a graceful shutdown then re-executes the current binary.
func (b *Bot) Restart() {
	b.shutdown()

	// Re-execute the current binary (replaces this process)
	exe, err := os.Executable()
	if err != nil {
		b.logger.Error("restart: get executable", "error", err)
		return
	}

	b.logger.Info("restart: re-executing", "exe", exe)
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		b.logger.Error("restart: start process", "error", err)
		return
	}

	// Parent process exits immediately after child starts
	b.logger.Info("restart: child process started, exiting parent")
	os.Exit(0)
}

// ---
// EventContext implementation — aligned with QQ Official API v2 dto.Message fields
// ---

type eventContextImpl struct {
	msg            *dto.Message
	api            contract.QQAPI
	content        string
	rawContent     string
	isMentioned    bool
	scene          contract.MessageScene
	role           string
	recordOutgoing func(scene string) // records outgoing message in daily stats
}

func newEventContext(eventType string, msg *dto.Message, rawData json.RawMessage, api contract.QQAPI, botID string, recordOutgoing func(scene string)) *eventContextImpl {
	// Extract mentioned user IDs
	var mentionIDs []string
	for _, m := range msg.Mentions {
		mentionIDs = append(mentionIDs, m.ID)
	}

	// Determine scene from native event type
	scene := sceneFromEventType(eventType)

	content, isMentioned := handler.IsMentioned(msg.Content, botID, mentionIDs)

	// Extract member_role from raw JSON (dto.User doesn't include this field)
	memberRole := extractMemberRole(rawData)

	return &eventContextImpl{
		msg:            msg,
		api:            api,
		content:        content,
		rawContent:     msg.Content,
		isMentioned:    isMentioned || scene == contract.SceneC2C,
		scene:          scene,
		role:           memberRole,
		recordOutgoing: recordOutgoing,
	}
}

// sceneFromEventType maps a native QQ event type to a MessageScene.
func sceneFromEventType(eventType string) contract.MessageScene {
	switch eventType {
	case types.EventMessageCreate, types.EventAtMessageCreate:
		return contract.SceneGuild
	case types.EventGroupAtMessageCreate, types.EventGroupMessageCreate:
		return contract.SceneGroup
	case types.EventC2CMessageCreate:
		return contract.SceneC2C
	default:
		if eventType == types.EventMessageCreate {
			return contract.SceneGuild
		}
		return contract.SceneGuild
	}
}

func (c *eventContextImpl) Content() string              { return c.content }
func (c *eventContextImpl) RawContent() string           { return c.rawContent }
func (c *eventContextImpl) ChannelID() string            { return c.msg.ChannelID }
func (c *eventContextImpl) AuthorID() string             { return c.msg.Author.ID }
func (c *eventContextImpl) Role() string                 { return c.role }
func (c *eventContextImpl) MessageID() string            { return c.msg.ID }
func (c *eventContextImpl) IsMentioned() bool            { return c.isMentioned }
func (c *eventContextImpl) Scene() contract.MessageScene { return c.scene }

func (c *eventContextImpl) GuildID() string { return c.msg.GuildID }
func (c *eventContextImpl) GroupID() string { return c.msg.GroupID }

func (c *eventContextImpl) Mentions() []string {
	var ids []string
	for _, m := range c.msg.Mentions {
		ids = append(ids, m.ID)
	}
	return ids
}

func (c *eventContextImpl) Attachments() []contract.Attachment {
	var atts []contract.Attachment
	for _, a := range c.msg.Attachments {
		att := contract.Attachment{
			URL:      a.URL,
			FileName: a.FileName,
			MimeType: a.ContentType,
			Width:    a.Width,
			Height:   a.Height,
		}
		atts = append(atts, att)
	}
	return atts
}

func (c *eventContextImpl) Reply(msg string) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		msgToCreate := &dto.MessageToCreate{Content: msg, MsgType: dto.TextMsg, MsgID: c.msg.ID}
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msgToCreate)
			if err == nil && c.recordOutgoing != nil {
				c.recordOutgoing("group")
			}
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msgToCreate)
			if err == nil && c.recordOutgoing != nil {
				c.recordOutgoing("c2c")
			}
			return err
		}
		// Channel message: use passive reply with msg_id to avoid active push restrictions
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msgToCreate)
		if err == nil && c.recordOutgoing != nil {
			c.recordOutgoing("channel")
		}
		return err
	}

	if c.recordOutgoing != nil {
		c.recordOutgoing("channel")
	}
	return c.api.SendMessage(c.msg.ChannelID, msg)
}

// ReplyMarkdown sends a markdown message as a passive reply with msg_id.
func (c *eventContextImpl) ReplyMarkdown(content string) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		msgToCreate := &dto.MessageToCreate{
			Markdown: &dto.Markdown{Content: content},
			MsgType:  dto.MarkdownMsg,
			MsgID:    c.msg.ID,
		}
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msgToCreate)
			if err == nil && c.recordOutgoing != nil {
				c.recordOutgoing("group")
			}
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msgToCreate)
			if err == nil && c.recordOutgoing != nil {
				c.recordOutgoing("c2c")
			}
			return err
		}
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msgToCreate)
		if err == nil && c.recordOutgoing != nil {
			c.recordOutgoing("channel")
		}
		return err
	}
	if c.recordOutgoing != nil {
		c.recordOutgoing("channel")
	}
	return c.api.SendMarkdown(c.msg.ChannelID, content)
}

// ReplyImage sends an image as a passive reply with msg_id.
func (c *eventContextImpl) ReplyImage(url string) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		msgToCreate := &dto.MessageToCreate{
			Image: url,
			MsgID: c.msg.ID,
		}
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msgToCreate)
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msgToCreate)
			return err
		}
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msgToCreate)
		return err
	}
	return c.api.SendImage(c.msg.ChannelID, url)
}

// ReplyWithButtons sends markdown + buttons as a passive reply with msg_id.
// Uses raw keyboard JSON for full action field support (reply, anchor, unsupport_tips).
func (c *eventContextImpl) ReplyWithButtons(content string, buttons []contract.MessageButton) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		rows := [][]contract.MessageButton{buttons}
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			msg := newButtonAPIMessage(content, rows)
			msg.MsgID = c.msg.ID
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msg)
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			msg := newButtonAPIMessage(content, rows)
			msg.MsgID = c.msg.ID
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msg)
			return err
		}
		// Channel: fallback to botgo types (PostMessage requires *dto.MessageToCreate)
		msg := buildButtonMessage(content, buttons)
		msg.MsgID = c.msg.ID
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msg)
		return err
	}
	return c.api.SendMessageWithButtons(c.msg.ChannelID, content, buttons)
}

// ReplyWithButtonRows sends markdown + multi-row buttons as a passive reply.
// Uses raw keyboard JSON for full action field support (reply, anchor, unsupport_tips).
func (c *eventContextImpl) ReplyWithButtonRows(content string, rows [][]contract.MessageButton) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			msg := newButtonAPIMessage(content, rows)
			msg.MsgID = c.msg.ID
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msg)
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			msg := newButtonAPIMessage(content, rows)
			msg.MsgID = c.msg.ID
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msg)
			return err
		}
		// Channel: fallback to botgo types
		msg := buildButtonRowsMessage(content, rows)
		msg.MsgID = c.msg.ID
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msg)
		return err
	}
	return c.api.SendMessageWithButtons(c.msg.ChannelID, content, rows[0])
}

// ReplyArk sends an Ark template message as a passive reply with msg_id.
func (c *eventContextImpl) ReplyArk(ark *contract.MessageArk) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		msg := buildArkMessage(ark)
		msg.MsgID = c.msg.ID
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msg)
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msg)
			return err
		}
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msg)
		return err
	}
	return c.api.SendArkMessage(c.msg.ChannelID, ark)
}

// ReplyMarkdownTemplate sends a templated markdown message as a passive reply with msg_id.
func (c *eventContextImpl) ReplyMarkdownTemplate(templateID string, params []contract.MarkdownParam) error {
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		msg := &dto.MessageToCreate{
			Markdown: buildMarkdownTemplate(templateID, params),
			MsgType:  dto.MarkdownMsg,
			MsgID:    c.msg.ID,
		}
		ctx := context.TODO()

		if c.msg.GroupID != "" {
			_, err := impl.api.PostGroupMessage(ctx, c.msg.GroupID, msg)
			return err
		}
		if c.msg.DirectMessage || c.scene == contract.SceneC2C {
			_, err := impl.api.PostC2CMessage(ctx, c.msg.Author.ID, msg)
			return err
		}
		_, err := impl.api.PostMessage(ctx, c.msg.ChannelID, msg)
		return err
	}
	return c.api.SendMarkdownTemplate(c.msg.ChannelID, templateID, params)
}

// guildEventContext implements contract.EventContext for guild/member events.
type guildEventContext struct {
	eventType string
	api       contract.QQAPI
}

func (g *guildEventContext) Content() string                    { return "" }
func (g *guildEventContext) RawContent() string                 { return "" }
func (g *guildEventContext) ChannelID() string                  { return "" }
func (g *guildEventContext) AuthorID() string                   { return "" }
func (g *guildEventContext) Role() string                       { return "" }
func (g *guildEventContext) MessageID() string                  { return "" }
func (g *guildEventContext) IsMentioned() bool                  { return false }
func (g *guildEventContext) GuildID() string                    { return "" }
func (g *guildEventContext) GroupID() string                    { return "" }
func (g *guildEventContext) Mentions() []string                 { return nil }
func (g *guildEventContext) Attachments() []contract.Attachment { return nil }
func (g *guildEventContext) Scene() contract.MessageScene       { return contract.SceneGuild }
func (g *guildEventContext) Reply(msg string) error             { return nil }
func (g *guildEventContext) ReplyMarkdown(content string) error { return nil }
func (g *guildEventContext) ReplyImage(url string) error        { return nil }
func (g *guildEventContext) ReplyWithButtons(content string, buttons []contract.MessageButton) error {
	return nil
}
func (g *guildEventContext) ReplyWithButtonRows(content string, rows [][]contract.MessageButton) error {
	return nil
}
func (g *guildEventContext) ReplyArk(ark *contract.MessageArk) error { return nil }
func (g *guildEventContext) ReplyMarkdownTemplate(templateID string, params []contract.MarkdownParam) error {
	return nil
}

// interactionContextImpl implements contract.InteractionContext.
type interactionContextImpl struct {
	data *contract.InteractionData
	api  contract.QQAPI
}

func newInteractionContext(d *dto.WSInteractionData, api contract.QQAPI) *interactionContextImpl {
	if d == nil {
		return &interactionContextImpl{api: api}
	}

	idata := &contract.InteractionData{
		ID:          d.ID,
		ChannelID:   d.ChannelID,
		GuildID:     d.GuildID,
		GroupOpenID: d.GroupOpenID,
		UserOpenID:  d.UserOpenID,
		Scene:       d.Scene,
	}
	idata.Type = int(d.Type)

	if d.GroupMemberOpenID != "" {
		idata.UserID = d.GroupMemberOpenID
	} else {
		idata.UserID = d.UserOpenID
	}

	if d.Data != nil && len(d.Data.Resolved) > 0 {
		var resolved dto.Resolved
		if err := json.Unmarshal(d.Data.Resolved, &resolved); err == nil {
			idata.ButtonID = resolved.ButtonID
			idata.ButtonData = resolved.ButtonData
			if resolved.MessageID != "" {
				idata.MessageID = resolved.MessageID
			}
		}
	}

	return &interactionContextImpl{data: idata, api: api}
}

func (c *interactionContextImpl) InteractionData() *contract.InteractionData { return c.data }
func (c *interactionContextImpl) ButtonID() string                           { return c.data.ButtonID }
func (c *interactionContextImpl) ButtonData() string                         { return c.data.ButtonData }
func (c *interactionContextImpl) GroupOpenID() string                        { return c.data.GroupOpenID }
func (c *interactionContextImpl) UserOpenID() string                         { return c.data.UserOpenID }
func (c *interactionContextImpl) UserID() string                             { return c.data.UserID }
func (c *interactionContextImpl) ChannelID() string                          { return c.data.ChannelID }
func (c *interactionContextImpl) MessageID() string                          { return c.data.MessageID }
func (c *interactionContextImpl) Reply(msg string) error {
	// 使用 botgo API 直接发送被动回复，优先使用存储的 msg_id
	if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
		msgToCreate := &dto.MessageToCreate{
			Content: msg,
			MsgType: dto.TextMsg,
		}
		// 尝试查找存储的按钮消息 msg_id 用于被动回复
		hasMsgID := false
		if storedMsgID := contract.GetButtonMsgID(c.data.GroupOpenID); storedMsgID != "" {
			msgToCreate.MsgID = storedMsgID
			// 设置消息序号防去重：同一 msg_id 的每次回复使用递增序号
			msgToCreate.MsgSeq = contract.NextMsgSeq(c.data.GroupOpenID)
			hasMsgID = true
		}
		ctx := context.TODO()
		if c.data.GroupOpenID != "" {
			// 有 msg_id 用被动回复，没有则用交互回调（type 2 markdown）
			if hasMsgID {
				_, err := impl.api.PostGroupMessage(ctx, c.data.GroupOpenID, msgToCreate)
				return err
			}
			// Fallback: 使用交互回调 type 2（至少用户能看到反馈）
			return c.api.ReplyInteraction(c.data.ID, msg)
		}
		if c.data.ChannelID != "" {
			_, err := impl.api.PostMessage(ctx, c.data.ChannelID, msgToCreate)
			return err
		}
		// C2C interaction: use PostC2CMessage
		if c.data.UserOpenID != "" {
			_, err := impl.api.PostC2CMessage(ctx, c.data.UserOpenID, msgToCreate)
			return err
		}
	}
	// Fallback
	if c.data.GroupOpenID != "" {
		return c.api.SendGroupMessage(c.data.GroupOpenID, msg)
	}
	return c.api.SendMessage(c.data.ChannelID, msg)
}

func (c *interactionContextImpl) ReplyMarkdown(content string) error {
	if c.data.GroupOpenID != "" {
		return c.api.SendGroupMarkdown(c.data.GroupOpenID, content)
	}
	return c.api.SendMarkdown(c.data.ChannelID, content)
}

func (c *interactionContextImpl) ReplyImage(url string) error {
	if c.data.GroupOpenID != "" {
		return c.api.SendGroupMessage(c.data.GroupOpenID, "[图片] "+url)
	}
	return c.api.SendImage(c.data.ChannelID, url)
}

func (c *interactionContextImpl) ReplyWithButtons(content string, buttons []contract.MessageButton) error {
	if c.data.GroupOpenID != "" {
		return c.api.SendGroupMessageWithButtons(c.data.GroupOpenID, content, buttons)
	}
	return c.api.SendMessageWithButtons(c.data.ChannelID, content, buttons)
}

func (c *interactionContextImpl) ReplyWithButtonRows(content string, rows [][]contract.MessageButton) error {
	ctx := context.TODO()
	if c.data.GroupOpenID != "" {
		if impl, ok := c.api.(*qqAPIImpl); ok && impl.api != nil {
			msg := newButtonAPIMessage(content, rows)
			if storedMsgID := contract.GetButtonMsgID(c.data.GroupOpenID); storedMsgID != "" {
				msg.MsgID = storedMsgID
				msg.MsgSeq = contract.NextMsgSeq(c.data.GroupOpenID)
			}
			_, err := impl.api.PostGroupMessage(ctx, c.data.GroupOpenID, msg)
			return err
		}
		return c.api.SendGroupMessageWithButtons(c.data.GroupOpenID, content, rows[0])
	}
	return c.api.SendMessageWithButtons(c.data.ChannelID, content, rows[0])
}

func (c *interactionContextImpl) ReplyArk(ark *contract.MessageArk) error {
	if c.data.GroupOpenID != "" {
		return c.api.SendGroupArkMessage(c.data.GroupOpenID, ark)
	}
	return c.api.SendArkMessage(c.data.ChannelID, ark)
}

func (c *interactionContextImpl) ReplyMarkdownTemplate(templateID string, params []contract.MarkdownParam) error {
	if c.data.GroupOpenID != "" {
		return c.api.SendGroupMarkdownTemplate(c.data.GroupOpenID, templateID, params)
	}
	return c.api.SendMarkdownTemplate(c.data.ChannelID, templateID, params)
}
func (c *interactionContextImpl) Callback(content string) error {
	// ReplyInteraction acknowledges the interaction AND sends a reply to the user.
	// This is the QQ API's built-in interaction callback mechanism and is NOT
	// subject to active push limits (the reply is part of the interaction protocol).
	if c.data.ID != "" {
		return c.api.ReplyInteraction(c.data.ID, content)
	}
	return nil
}
func (c *interactionContextImpl) DeferReply() error {
	return c.api.PutInteraction(c.data.ID, `{"type":0}`)
}

// EventContext interface implementation (for EventBus dispatch compatibility).
func (c *interactionContextImpl) Content() string                    { return "" }
func (c *interactionContextImpl) RawContent() string                 { return "" }
func (c *interactionContextImpl) AuthorID() string                   { return c.data.UserID }
func (c *interactionContextImpl) Role() string                       { return "" }
func (c *interactionContextImpl) IsMentioned() bool                  { return false }
func (c *interactionContextImpl) GuildID() string                    { return c.data.GuildID }
func (c *interactionContextImpl) GroupID() string                    { return "" }
func (c *interactionContextImpl) Mentions() []string                 { return nil }
func (c *interactionContextImpl) Attachments() []contract.Attachment { return nil }
func (c *interactionContextImpl) Scene() contract.MessageScene       { return contract.SceneC2C }

// commandContextImpl implements contract.CommandContext.
type commandContextImpl struct {
	args []string
	contract.EventContext
}

func (c *commandContextImpl) Args() []string { return c.args }
func (c *commandContextImpl) Arg(i int) string {
	if i >= 0 && i < len(c.args) {
		return c.args[i]
	}
	return ""
}
func (c *commandContextImpl) ArgCount() int { return len(c.args) }

// ---
// QQAPI implementation using botgo OpenAPI
// ---

type qqAPIImpl struct {
	appID       string
	appSecret   string
	sandbox     bool
	logger      *framelog.Logger
	api         openapi.OpenAPI
	tokenSource oauth2.TokenSource
	mu          sync.Mutex
}

// AppID returns the bot's App ID.
func (a *qqAPIImpl) AppID() string { return a.appID }

func (a *qqAPIImpl) initOpenAPI() {
	if a.appID == "" || a.appID == "your_app_id_here" {
		a.logger.Warn("QQAPI: app_id not configured, API calls will be no-ops")
		return
	}

	// Note: Token endpoint (https://bots.qq.com/app/getAppAccessToken) is the SAME
	// for both production and sandbox environments. Do NOT change TokenDomain for sandbox.

	credentials := &token.QQBotCredentials{
		AppID:     a.appID,
		AppSecret: a.appSecret,
	}
	tokenSource := token.NewQQBotTokenSource(credentials)
	a.tokenSource = tokenSource
	a.mu.Lock()
	if a.sandbox {
		a.api = botgo.NewSandboxOpenAPI(a.appID, tokenSource)
	} else {
		a.api = botgo.NewOpenAPI(a.appID, tokenSource)
	}
	a.mu.Unlock()

	if a.sandbox {
		a.logger.Info("QQAPI initialized (sandbox mode)")
	} else {
		a.logger.Info("QQAPI initialized")
	}
}

func (a *qqAPIImpl) sendToChannel(id string, msg *dto.MessageToCreate) error {
	a.mu.Lock()
	api := a.api
	a.mu.Unlock()
	if api == nil {
		return nil
	}
	_, err := api.PostMessage(context.TODO(), id, msg)
	if err != nil {
		a.logger.Error("send message failed", "error", err, "target_id", id)
	}
	return err
}

func (a *qqAPIImpl) sendToGroup(id string, msg *dto.MessageToCreate) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	_, err := api.PostGroupMessage(context.TODO(), id, msg)
	if err != nil {
		a.logger.Error("send group message failed", "error", err, "target_id", id)
	}
	return err
}

func (a *qqAPIImpl) SendMessage(channelID, content string) error {
	msg := &dto.MessageToCreate{Content: content, MsgType: dto.TextMsg}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) SendMarkdown(channelID, content string) error {
	msg := &dto.MessageToCreate{
		Markdown: &dto.Markdown{Content: content},
		MsgType:  dto.MarkdownMsg,
	}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) ReplyMarkdown(channelID, msgID, content string) error {
	msg := &dto.MessageToCreate{
		Markdown: &dto.Markdown{Content: content},
		MsgType:  dto.MarkdownMsg,
		MsgID:    msgID,
	}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) SendMarkdownTemplate(channelID, templateID string, params []contract.MarkdownParam) error {
	msg := &dto.MessageToCreate{
		Markdown: buildMarkdownTemplate(templateID, params),
		MsgType:  dto.MarkdownMsg,
	}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) SendImage(channelID, imageURL string) error {
	msg := &dto.MessageToCreate{Image: imageURL}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) ReplyImage(channelID, msgID, imageURL string) error {
	msg := &dto.MessageToCreate{Image: imageURL, MsgID: msgID}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) SendMessageWithButtons(channelID string, content string, buttons []contract.MessageButton) error {
	msg := buildButtonMessage(content, buttons)
	return a.sendToChannel(channelID, msg)
}

// buildButtonMessage creates a MessageToCreate with keyboard buttons.
// Per QQ API docs, keyboards are only supported on markdown messages.
func buildButtonMessage(content string, buttons []contract.MessageButton) *dto.MessageToCreate {
	var kbButtons []*keyboard.Button
	for _, btn := range buttons {
		kbButtons = append(kbButtons, buildBotgoButton(&btn))
	}

	return &dto.MessageToCreate{
		MsgType: dto.MarkdownMsg,
		Markdown: &dto.Markdown{
			Content: content,
		},
		Keyboard: &keyboard.MessageKeyboard{
			Content: &keyboard.CustomKeyboard{
				Rows: []*keyboard.Row{{Buttons: kbButtons}},
			},
		},
	}
}

// buildBotgoButton converts a contract.MessageButton to a botgo keyboard.Button,
// mapping the supported action type and permission fields.
func buildBotgoButton(btn *contract.MessageButton) *keyboard.Button {
	data := btn.Data
	if data == "" {
		data = btn.ID
	}

	// Map action type.
	// Priority: URL field > ActionType field > default (command)
	var actionType keyboard.ActionType
	if btn.URL != "" {
		// URL field takes priority → jump button (ActionType=0)
		actionType = keyboard.ActionTypeURL
		data = btn.URL
	} else {
		// Go int zero value 0 is ambiguous (unset vs QQ API URL=0).
		// Use URL field for jump buttons. Treat 0/2 as command, 1 as callback.
		switch btn.ActionType {
		case 0:
			actionType = keyboard.ActionTypeAtBot // default: command
		case 1:
			actionType = keyboard.ActionTypeCallback
		case 2:
			actionType = keyboard.ActionTypeAtBot
		default:
			actionType = keyboard.ActionTypeAtBot
		}
	}

	// Map permission type
	// 注意: Permission 的 Go 零值为 0 (=specific users)，但业务上未设置时应默认 everyone
	permType := keyboard.PermissionTypAll
	switch btn.Permission {
	case 0:
		if len(btn.SpecifyUserIDs) > 0 {
			permType = keyboard.PermissionTypeSpecifyUserIDs
		}
		// 无 SpecifyUserIDs → 保持默认 everyone
	case 1:
		permType = keyboard.PermissionTypManager
	case 2:
		permType = keyboard.PermissionTypAll
	case 3:
		permType = keyboard.PermissionTypSpecifyRoleIDs
	}

	perm := &keyboard.Permission{Type: permType}
	if len(btn.SpecifyUserIDs) > 0 {
		perm.SpecifyUserIDs = btn.SpecifyUserIDs
	}
	if len(btn.SpecifyRoleIDs) > 0 {
		perm.SpecifyRoleIDs = btn.SpecifyRoleIDs
	}

	action := &keyboard.Action{
		Type:       actionType,
		Permission: perm,
		Data:       data,
		Enter:      btn.Enter,
	}

	visitedLabel := btn.Label
	if btn.VisitedLabel != "" {
		visitedLabel = btn.VisitedLabel
	}
	return &keyboard.Button{
		ID: btn.ID,
		RenderData: &keyboard.RenderData{
			Label:        btn.Label,
			VisitedLabel: visitedLabel,
			Style:        btn.Style,
		},
		Action: action,
	}
}

// ── Full action support via raw JSON (includes fields botgo doesn't have) ──

// fullAction mirrors QQ API button action with ALL supported fields.
type fullAction struct {
	Type                 int             `json:"type"`
	Permission           *fullPermission `json:"permission,omitempty"`
	Data                 string          `json:"data,omitempty"`
	Enter                bool            `json:"enter,omitempty"`
	Reply                bool            `json:"reply,omitempty"`
	Anchor               int             `json:"anchor,omitempty"`
	UnsupportTips        string          `json:"unsupport_tips,omitempty"`
	AtBotShowChannelList bool            `json:"at_bot_show_channel_list,omitempty"`
}

type fullPermission struct {
	Type           int      `json:"type"`
	SpecifyUserIDs []string `json:"specify_user_ids,omitempty"`
	SpecifyRoleIDs []string `json:"specify_role_ids,omitempty"`
}

// fullButton mirrors QQ API button with full action support.
type fullButton struct {
	ID         string      `json:"id"`
	RenderData *renderData `json:"render_data"`
	Action     *fullAction `json:"action"`
}

type renderData struct {
	Label        string `json:"label"`
	VisitedLabel string `json:"visited_label"`
	Style        int    `json:"style"`
}

// buildKeyboardJSON builds a complete keyboard JSON with full action field support.
func buildKeyboardJSON(content string, rows [][]contract.MessageButton) []byte {
	var kbRows []map[string]interface{}
	for _, buttons := range rows {
		var btns []*fullButton
		for _, btn := range buttons {
			btns = append(btns, buildFullButton(&btn))
		}
		kbRows = append(kbRows, map[string]interface{}{"buttons": btns})
	}

	keyboard := map[string]interface{}{
		"content": map[string]interface{}{
			"rows": kbRows,
		},
	}
	jsonBytes, _ := json.Marshal(keyboard)
	return jsonBytes
}

// buildFullButton converts a contract.MessageButton to a fullButton with all fields.
func buildFullButton(btn *contract.MessageButton) *fullButton {
	data := btn.Data
	if data == "" {
		data = btn.ID
	}

	// ActionType: URL field > ActionType field > default (command).
	actionType := btn.ActionType
	if btn.URL != "" {
		actionType = 0 // jump
		data = btn.URL
	} else if actionType == 0 {
		actionType = 2 // default: command (@bot)
	}

	perm := &fullPermission{Type: 2} // default: everyone
	switch btn.Permission {
	case 0:
		if len(btn.SpecifyUserIDs) > 0 {
			perm.Type = 0
			perm.SpecifyUserIDs = btn.SpecifyUserIDs
		}
		// 无 SpecifyUserIDs → 保持默认 everyone
	case 1:
		perm.Type = 1
	case 2:
		perm.Type = 2
	case 3:
		perm.Type = 3
		perm.SpecifyRoleIDs = btn.SpecifyRoleIDs
	}

	visitedLabel := btn.Label
	if btn.VisitedLabel != "" {
		visitedLabel = btn.VisitedLabel
	}
	return &fullButton{
		ID: btn.ID,
		RenderData: &renderData{
			Label:        btn.Label,
			VisitedLabel: visitedLabel,
			Style:        btn.Style,
		},
		Action: &fullAction{
			Type:          actionType,
			Permission:    perm,
			Data:          data,
			Enter:         btn.Enter,
			Reply:         btn.Reply,
			Anchor:        btn.Anchor,
			UnsupportTips: btn.UnsupportTips,
		},
	}
}

// buttonAPIMessage implements dto.APIMessage with raw keyboard JSON for full action support.
type buttonAPIMessage struct {
	Content  string          `json:"content,omitempty"`
	MsgType  dto.MessageType `json:"msg_type,omitempty"`
	MsgID    string          `json:"msg_id,omitempty"`
	MsgSeq   uint32          `json:"msg_seq,omitempty"`
	Markdown *dto.Markdown   `json:"markdown,omitempty"`
	Keyboard json.RawMessage `json:"keyboard,omitempty"`
}

func (m *buttonAPIMessage) GetEventID() string        { return "" }
func (m *buttonAPIMessage) GetSendType() dto.SendType { return dto.Text }

// newButtonAPIMessage creates a buttonAPIMessage with full action support.
func newButtonAPIMessage(content string, rows [][]contract.MessageButton) *buttonAPIMessage {
	keyboardJSON := buildKeyboardJSON(content, rows)
	return &buttonAPIMessage{
		MsgType: dto.MarkdownMsg,
		Markdown: &dto.Markdown{
			Content: content,
		},
		Keyboard: keyboardJSON,
	}
}

// buildButtonRowsMessage creates a MessageToCreate with multi-row keyboard buttons.
// Each inner slice represents one row of buttons in the keyboard.
func buildButtonRowsMessage(content string, rows [][]contract.MessageButton) *dto.MessageToCreate {
	var kbRows []*keyboard.Row
	for _, buttons := range rows {
		var kbButtons []*keyboard.Button
		for _, btn := range buttons {
			kbButtons = append(kbButtons, buildBotgoButton(&btn))
		}
		kbRows = append(kbRows, &keyboard.Row{Buttons: kbButtons})
	}

	return &dto.MessageToCreate{
		MsgType: dto.MarkdownMsg,
		Markdown: &dto.Markdown{
			Content: content,
		},
		Keyboard: &keyboard.MessageKeyboard{
			Content: &keyboard.CustomKeyboard{
				Rows: kbRows,
			},
		},
	}
}

func (a *qqAPIImpl) SendGroupMessage(groupID string, content string) error {
	msg := &dto.MessageToCreate{Content: content}
	a.mu.Lock()
	api := a.api
	a.mu.Unlock()
	if api == nil {
		return nil
	}
	_, err := api.PostGroupMessage(context.TODO(), groupID, msg)
	if err != nil {
		a.logger.Error("send group message failed", "error", err, "target_id", groupID)
	}
	return err
}

func (a *qqAPIImpl) SendGroupMessageWithButtons(groupID string, content string, buttons []contract.MessageButton) error {
	msg := buildButtonMessage(content, buttons)
	return a.sendToGroup(groupID, msg)
}

// ── New message type implementations ──

func (a *qqAPIImpl) SendEmbedMessage(channelID string, embed *contract.MessageEmbed) error {
	// Convert contract embed to botgo embed
	bgEmbed := &dto.Embed{
		Title:  embed.Title,
		Prompt: embed.Prompt,
	}
	if embed.Thumbnail != "" {
		bgEmbed.Thumbnail = dto.MessageEmbedThumbnail{URL: embed.Thumbnail}
	}
	for _, f := range embed.Fields {
		bgEmbed.Fields = append(bgEmbed.Fields, &dto.EmbedField{Name: f.Name})
	}
	msg := &dto.MessageToCreate{
		Embed:   bgEmbed,
		MsgType: dto.EmbedMsg,
	}
	return a.sendToChannel(channelID, msg)
}

// buildArkMessage converts a contract.MessageArk to a dto.MessageToCreate.
func buildArkMessage(ark *contract.MessageArk) *dto.MessageToCreate {
	bgArk := &dto.Ark{
		TemplateID: ark.TemplateID,
	}
	for _, kv := range ark.KV {
		bgKV := &dto.ArkKV{Key: kv.Key, Value: kv.Value}
		for _, obj := range kv.Obj {
			bgObj := &dto.ArkObj{}
			for _, okv := range obj.ObjKV {
				bgObj.ObjKV = append(bgObj.ObjKV, &dto.ArkObjKV{Key: okv.Key, Value: okv.Value})
			}
			bgKV.Obj = append(bgKV.Obj, bgObj)
		}
		bgArk.KV = append(bgArk.KV, bgKV)
	}
	return &dto.MessageToCreate{
		Ark:     bgArk,
		MsgType: dto.ArkMsg,
	}
}

// buildMarkdownTemplate converts a templateID + params to a dto.Markdown.
func buildMarkdownTemplate(templateID string, params []contract.MarkdownParam) *dto.Markdown {
	bgParams := make([]*dto.MarkdownParams, len(params))
	for i, p := range params {
		bgParams[i] = &dto.MarkdownParams{Key: p.Key, Values: p.Values}
	}
	return &dto.Markdown{
		CustomTemplateID: templateID,
		Params:           bgParams,
	}
}

func (a *qqAPIImpl) SendArkMessage(channelID string, ark *contract.MessageArk) error {
	return a.sendToChannel(channelID, buildArkMessage(ark))
}

func (a *qqAPIImpl) SendGroupArkMessage(groupID string, ark *contract.MessageArk) error {
	msg := buildArkMessage(ark)
	a.mu.Lock()
	api := a.api
	a.mu.Unlock()
	if api == nil {
		return nil
	}
	_, err := api.PostGroupMessage(context.TODO(), groupID, msg)
	return err
}

func (a *qqAPIImpl) SendC2CArkMessage(userID string, ark *contract.MessageArk) error {
	msg := buildArkMessage(ark)
	a.mu.Lock()
	api := a.api
	a.mu.Unlock()
	if api == nil {
		return nil
	}
	_, err := api.PostC2CMessage(context.TODO(), userID, msg)
	return err
}

func (a *qqAPIImpl) SendRichMedia(channelID string, media *contract.RichMedia) error {
	// If URL is provided but FileInfo is empty, auto-upload first
	if media.FileInfo == "" && media.URL != "" {
		fileInfo, err := a.UploadChannelMedia(channelID, media.FileType, media.URL)
		if err != nil {
			return fmt.Errorf("upload channel media: %w", err)
		}
		media.FileInfo = fileInfo
	}

	msg := &dto.MessageToCreate{
		MsgType: dto.RichMediaMsg,
	}
	if media.FileInfo != "" {
		msg.Media = &dto.MediaInfo{FileInfo: []byte(media.FileInfo)}
	}
	if media.Content != "" {
		msg.Content = media.Content
	}
	return a.sendToChannel(channelID, msg)
}

func (a *qqAPIImpl) SendGroupMarkdown(groupID string, content string) error {
	msg := &dto.MessageToCreate{
		Markdown: &dto.Markdown{Content: content},
		MsgType:  dto.MarkdownMsg,
	}
	return a.sendToGroup(groupID, msg)
}

func (a *qqAPIImpl) SendGroupMarkdownTemplate(groupID, templateID string, params []contract.MarkdownParam) error {
	msg := &dto.MessageToCreate{
		Markdown: buildMarkdownTemplate(templateID, params),
		MsgType:  dto.MarkdownMsg,
	}
	return a.sendToGroup(groupID, msg)
}

func (a *qqAPIImpl) SendGroupRichMedia(groupID string, media *contract.RichMedia) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}

	// Step 1: Upload the file to get file_info
	fileInfo, err := a.UploadGroupMedia(groupID, media.FileType, media.URL)
	if err != nil {
		return fmt.Errorf("upload group media: %w", err)
	}

	// Step 2: Send as a passive reply with msg_id
	// Build the request body manually to avoid botgo's base64 encoding of []byte.
	baseURL := constant.APIDomain
	if a.sandbox {
		baseURL = constant.SandBoxAPIDomain
	}

	body := map[string]interface{}{
		"msg_type": 7,
		"media": map[string]string{
			"file_info": fileInfo,
		},
	}
	if media.MsgID != "" {
		body["msg_id"] = media.MsgID
	}
	if media.Content != "" {
		body["content"] = media.Content
	}

	uploadURL := fmt.Sprintf("%s/v2/groups/%s/messages", baseURL, groupID)
	_, err = api.Transport(context.TODO(), "POST", uploadURL, body)
	if err != nil {
		a.logger.Error("send group rich media failed", "error", err, "target_id", groupID)
	}
	return err
}

// ── C2C message sending ──

func (a *qqAPIImpl) sendToC2C(userID string, msg *dto.MessageToCreate) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	_, err := api.PostC2CMessage(context.TODO(), userID, msg)
	if err != nil {
		a.logger.Error("send c2c message failed", "error", err, "target_id", userID)
	}
	return err
}

func (a *qqAPIImpl) SendC2CMessage(userID, content string) error {
	msg := &dto.MessageToCreate{Content: content, MsgType: dto.TextMsg}
	return a.sendToC2C(userID, msg)
}

func (a *qqAPIImpl) SendC2CMarkdown(userID, content string) error {
	msg := &dto.MessageToCreate{
		Markdown: &dto.Markdown{Content: content},
		MsgType:  dto.MarkdownMsg,
	}
	return a.sendToC2C(userID, msg)
}

func (a *qqAPIImpl) SendC2CMarkdownTemplate(userID, templateID string, params []contract.MarkdownParam) error {
	msg := &dto.MessageToCreate{
		Markdown: buildMarkdownTemplate(templateID, params),
		MsgType:  dto.MarkdownMsg,
	}
	return a.sendToC2C(userID, msg)
}

func (a *qqAPIImpl) SendC2CRichMedia(userID string, media *contract.RichMedia) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}

	// Step 1: Upload the file to get file_info
	fileInfo, err := a.UploadC2CMedia(userID, media.FileType, media.URL)
	if err != nil {
		return fmt.Errorf("upload c2c media: %w", err)
	}

	// Step 2: Send as a passive reply with msg_id
	// Build the request body manually to avoid botgo's base64 encoding of []byte.
	baseURL := constant.APIDomain
	if a.sandbox {
		baseURL = constant.SandBoxAPIDomain
	}

	body := map[string]interface{}{
		"msg_type": 7,
		"media": map[string]string{
			"file_info": fileInfo,
		},
	}
	if media.MsgID != "" {
		body["msg_id"] = media.MsgID
	}
	if media.Content != "" {
		body["content"] = media.Content
	}

	uploadURL := fmt.Sprintf("%s/v2/users/%s/messages", baseURL, userID)
	_, err = api.Transport(context.TODO(), "POST", uploadURL, body)
	if err != nil {
		a.logger.Error("send c2c rich media failed", "error", err, "target_id", userID)
	}
	return err
}

func (a *qqAPIImpl) SendC2CMessageWithButtons(userID string, content string, buttons []contract.MessageButton) error {
	msg := buildButtonMessage(content, buttons)
	return a.sendToC2C(userID, msg)
}

// ── Message management ──

func (a *qqAPIImpl) DeleteMessage(channelID, messageID string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	return api.RetractMessage(context.TODO(), channelID, messageID)
}

func (a *qqAPIImpl) DeleteGroupMessage(groupID, messageID string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	return api.RetractGroupMessage(context.TODO(), groupID, messageID)
}

func (a *qqAPIImpl) DeleteC2CMessage(userID, messageID string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	return api.RetractC2CMessage(context.TODO(), userID, messageID)
}

func (a *qqAPIImpl) PinMessage(channelID, messageID string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	_, err := api.AddPins(context.TODO(), channelID, messageID)
	return err
}

func (a *qqAPIImpl) UnpinMessage(channelID, messageID string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	return api.DeletePins(context.TODO(), channelID, messageID)
}

// ── Reactions ──

func (a *qqAPIImpl) CreateReaction(channelID, messageID, emoji string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	e := parseEmoji(emoji)
	return api.CreateMessageReaction(context.TODO(), channelID, messageID, e)
}

func (a *qqAPIImpl) DeleteReaction(channelID, messageID, emoji string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	e := parseEmoji(emoji)
	return api.DeleteOwnMessageReaction(context.TODO(), channelID, messageID, e)
}

// parseEmoji parses a "type:id" string into a dto.Emoji struct.
// Format: "1:4" for system emoji (type=1, id=4), or "2:❤️" for unicode emoji.
func parseEmoji(s string) dto.Emoji {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		typeVal, err := strconv.Atoi(parts[0])
		if err == nil && typeVal > 0 {
			return dto.Emoji{Type: typeVal, ID: parts[1]}
		}
	}
	// Default: treat as unicode emoji
	return dto.Emoji{Type: 2, ID: s}
}

// ── Active push ──

// activeC2CBody implements dto.APIMessage with is_wakeup support for active push.
type activeC2CBody struct {
	Content  string `json:"content"`
	MsgType  int    `json:"msg_type"`
	IsWakeup bool   `json:"is_wakeup"`
}

func (b *activeC2CBody) GetEventID() string        { return "" }
func (b *activeC2CBody) GetSendType() dto.SendType { return 0 }

func (a *qqAPIImpl) SendActiveC2CMessage(userID, content string, isWakeup bool) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	body := &activeC2CBody{
		Content:  content,
		MsgType:  int(dto.TextMsg),
		IsWakeup: isWakeup,
	}
	_, err := api.PostC2CMessage(context.TODO(), userID, body)
	if err != nil {
		a.logger.Error("send active c2c message failed", "error", err, "target_id", userID)
	}
	return err
}

// ── Guild / Channel info ──

func (a *qqAPIImpl) GetGuild(guildID string) (*contract.Guild, error) {
	api := a.getAPI()
	if api == nil {
		return nil, nil
	}
	g, err := api.Guild(context.TODO(), guildID)
	if err != nil {
		a.logger.Error("get guild failed", "error", err, "guild_id", guildID)
		return nil, err
	}
	return &contract.Guild{
		ID:          g.ID,
		Name:        g.Name,
		Icon:        g.Icon,
		OwnerID:     g.OwnerID,
		MemberCount: g.MemberCount,
		MaxMembers:  g.MaxMembers,
		Desc:        g.Desc,
	}, nil
}

func (a *qqAPIImpl) GetChannel(channelID string) (*contract.Channel, error) {
	api := a.getAPI()
	if api == nil {
		return nil, nil
	}
	c, err := api.Channel(context.TODO(), channelID)
	if err != nil {
		a.logger.Error("get channel failed", "error", err, "channel_id", channelID)
		return nil, err
	}
	return &contract.Channel{
		ID:       c.ID,
		GuildID:  c.GuildID,
		Name:     c.Name,
		Type:     int(c.Type),
		ParentID: c.ParentID,
	}, nil
}

func (a *qqAPIImpl) GetGuildMember(guildID, userID string) (*contract.Member, error) {
	api := a.getAPI()
	if api == nil {
		return nil, nil
	}
	m, err := api.GuildMember(context.TODO(), guildID, userID)
	if err != nil {
		a.logger.Error("get guild member failed", "error", err, "guild_id", guildID, "user_id", userID)
		return nil, err
	}
	member := &contract.Member{
		Nick:     m.Nick,
		Roles:    m.Roles,
		JoinedAt: string(m.JoinedAt),
	}
	if m.User != nil {
		member.User = &contract.User{
			ID:       m.User.ID,
			Username: m.User.Username,
			Avatar:   m.User.Avatar,
			Bot:      m.User.Bot,
		}
	}
	return member, nil
}

// uploadMediaRequest is the request body for media file upload (channel/group/C2C).
type uploadMediaRequest struct {
	FileType int    `json:"file_type"`
	URL      string `json:"url"`
}

// uploadMediaResponse is the response body for media file upload.
type uploadMediaResponse struct {
	FileInfo string `json:"file_info"`
}

// doUploadRequest makes a direct HTTP POST to the QQ API media upload endpoint,
// bypassing botgo's resty Transport to get full visibility into HTTP status and body.
func (a *qqAPIImpl) doUploadRequest(uploadURL string, fileType int, url string) (string, error) {
	if a.tokenSource == nil {
		return "", fmt.Errorf("upload: token source not initialized")
	}

	// Get access token
	tk, err := a.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("upload: get token: %w", err)
	}

	// Build request body
	body := &uploadMediaRequest{FileType: fileType, URL: url}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("upload: marshal body: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", uploadURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("upload: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", tk.TokenType+" "+tk.AccessToken)
	req.Header.Set("X-Union-Appid", a.appID)

	// Execute with timeout
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("upload: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload: HTTP %d, body: %s",
			resp.StatusCode, string(respBody))
	}

	var result uploadMediaResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("upload: parse response: %w, body: %s", err, string(respBody))
	}

	if result.FileInfo == "" {
		return "", fmt.Errorf("upload: empty file_info in response: %s", string(respBody))
	}

	return result.FileInfo, nil
}

// UploadChannelMedia uploads a media file to a channel and returns the file_info string.
func (a *qqAPIImpl) UploadChannelMedia(channelID string, fileType int, url string) (string, error) {
	baseURL := constant.APIDomain
	if a.sandbox {
		baseURL = constant.SandBoxAPIDomain
	}
	uploadURL := fmt.Sprintf("%s/channels/%s/files", baseURL, channelID)
	return a.doUploadRequest(uploadURL, fileType, url)
}

// UploadGroupMedia uploads a media file to a group and returns the file_info string.
func (a *qqAPIImpl) UploadGroupMedia(groupID string, fileType int, url string) (string, error) {
	baseURL := constant.APIDomain
	if a.sandbox {
		baseURL = constant.SandBoxAPIDomain
	}
	uploadURL := fmt.Sprintf("%s/v2/groups/%s/files", baseURL, groupID)
	return a.doUploadRequest(uploadURL, fileType, url)
}

// UploadC2CMedia uploads a media file for C2C and returns the file_info string.
func (a *qqAPIImpl) UploadC2CMedia(userID string, fileType int, url string) (string, error) {
	baseURL := constant.APIDomain
	if a.sandbox {
		baseURL = constant.SandBoxAPIDomain
	}
	uploadURL := fmt.Sprintf("%s/v2/users/%s/files", baseURL, userID)
	return a.doUploadRequest(uploadURL, fileType, url)
}

func (a *qqAPIImpl) getAPI() openapi.OpenAPI {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.api
}

func (a *qqAPIImpl) PutInteraction(interactionID string, body string) error {
	api := a.getAPI()
	if api == nil {
		return nil
	}
	return api.PutInteraction(context.TODO(), interactionID, body)
}

func (a *qqAPIImpl) ReplyInteraction(interactionID string, content string) error {
	encoded, _ := json.Marshal(content)
	// type:2 = reply with markdown (visible in group/channel, bypasses active push limits)
	body := fmt.Sprintf(`{"type":2,"data":{"markdown":{"content":%s}}}`, string(encoded))
	return a.PutInteraction(interactionID, body)
}

// ---
// botgoLogger bridges botgo's log.Logger to our zerolog.Logger.
// ---

type botgoLogger struct {
	*framelog.Logger
}

func (l *botgoLogger) Debug(v ...interface{}) { l.Logger.Debug(fmt.Sprint(v...)) }
func (l *botgoLogger) Info(v ...interface{})  { l.Logger.Info(fmt.Sprint(v...)) }
func (l *botgoLogger) Warn(v ...interface{})  { l.Logger.Warn(fmt.Sprint(v...)) }
func (l *botgoLogger) Error(v ...interface{}) { l.Logger.Error(fmt.Sprint(v...)) }
func (l *botgoLogger) Debugf(format string, v ...interface{}) {
	l.Logger.Debug(fmt.Sprintf(format, v...))
}
func (l *botgoLogger) Infof(format string, v ...interface{}) {
	l.Logger.Info(fmt.Sprintf(format, v...))
}
func (l *botgoLogger) Warnf(format string, v ...interface{}) {
	l.Logger.Warn(fmt.Sprintf(format, v...))
}
func (l *botgoLogger) Errorf(format string, v ...interface{}) {
	l.Logger.Error(fmt.Sprintf(format, v...))
}
func (l *botgoLogger) Sync() error { return nil }

// ---
// Helpers
// ---

// extractMemberRole extracts the member_role from raw message JSON.
// The botgo dto.User struct doesn't include MemberRole, so we need to
// extract it manually from the raw JSON's d.author.member_role field.
// Returns "owner", "admin", "member", or empty string if not found.
func extractMemberRole(rawData json.RawMessage) string {
	var data struct {
		Author struct {
			MemberRole string `json:"member_role"`
		} `json:"author"`
	}
	if err := json.Unmarshal(rawData, &data); err != nil {
		return ""
	}
	return data.Author.MemberRole
}

func ensureDirs(dirs ...string) {
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "[bot] failed to create directory %s: %v\n", d, err)
		}
	}
}
