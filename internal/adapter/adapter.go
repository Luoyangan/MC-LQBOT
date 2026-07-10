// Package adapter implements protocol adapters for QQ API connections.
// It provides WebSocket and Webhook adapters that implement contract.Adapter.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Luoyangan/LQBOT/internal/log"
	"github.com/Luoyangan/LQBOT/internal/types"
	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/interaction/webhook"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
)

// WebSocketAdapter implements contract.Adapter using QQ WebSocket (via botgo).
type WebSocketAdapter struct {
	name      string
	appID     string
	appSecret string
	sandbox   bool     // true = use sandbox API endpoints
	intents   []string // only register these intents; empty = all registered
	events    chan []byte
	logger    *log.Logger
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	botUserID string // Bot's own user ID, set from Ready event
	mu        sync.RWMutex
	backoff   BackoffConfig
	backoffMu sync.Mutex
	backoffCur time.Duration // current backoff duration, reset on Ready
}

// NewWebSocketAdapter creates a new WebSocket adapter.
// If sandbox is true, the adapter connects to QQ's sandbox environment.
// intents is the list of configured intents; if empty, all registered handlers are used.
func NewWebSocketAdapter(appID, appSecret string, sandbox bool, intents []string, logger *log.Logger) (*WebSocketAdapter, error) {
	return &WebSocketAdapter{
		name:      "websocket",
		appID:     appID,
		appSecret: appSecret,
		sandbox:   sandbox,
		intents:   intents,
		events:    make(chan []byte, 512),
		logger:    logger,
		backoff:   DefaultBackoff,
		backoffCur: DefaultBackoff.Initial,
	}, nil
}

// Name returns the adapter name.
func (a *WebSocketAdapter) Name() string { return a.name }

// Start establishes the WebSocket connection and begins receiving events.
func (a *WebSocketAdapter) Start(ctx context.Context) error {
	// Skip actual connection if credentials are placeholder values
	if a.appID == "" || a.appID == "your_app_id_here" {
		a.logger.Warn("WebSocket adapter: app_id not configured, skipping connection")
		return nil
	}

	// Redirect botgo's internal logs to our zerolog logger
	botgo.SetLogger(&webhookLogger{inner: a.logger})

	ctx, a.cancel = context.WithCancel(ctx)

	// 1. Create OAuth2 credentials
	credentials := &token.QQBotCredentials{
		AppID:     a.appID,
		AppSecret: a.appSecret,
	}
	tokenSource := token.NewQQBotTokenSource(credentials)

	// 3. Start background token refresh
	if err := token.StartRefreshAccessToken(ctx, tokenSource); err != nil {
		return fmt.Errorf("start token refresh: %w", err)
	}

	// 4. Create OpenAPI client (used to get WebSocket endpoint)
	var api openapi.OpenAPI
	if a.sandbox {
		api = botgo.NewSandboxOpenAPI(a.appID, tokenSource)
	} else {
		api = botgo.NewOpenAPI(a.appID, tokenSource)
	}

	// 5. Get WebSocket access point (gateway)
	wsAP, err := api.WS(ctx, nil, "")
	if err != nil {
		return fmt.Errorf("get websocket gateway: %w", err)
	}
	a.logger.Info("WebSocket gateway obtained", "url", wsAP.URL, "shards", wsAP.Shards)

	// 6. Register event handlers — emit events with native QQ API event type strings
	intent := event.RegisterHandlers(
		// Ready — connection established
		event.ReadyHandler(func(event *dto.WSPayload, data *dto.WSReadyData) {
			a.mu.Lock()
			a.botUserID = data.User.ID
			a.mu.Unlock()
			// Reset backoff on successful connection: next reconnection will be instant
			a.backoffMu.Lock()
			a.backoffCur = a.backoff.Initial
			a.backoffMu.Unlock()
			a.logger.Info("WebSocket ready",
				"version", data.Version,
				"session_id", data.SessionID,
				"user", data.User.ID,
				"shard", fmt.Sprintf("%d/%d", data.Shard[0], data.Shard[1]),
			)
		}),
		// Error notify
		event.ErrorNotifyHandler(func(err error) {
			a.logger.Error("WebSocket error", "error", err)
		}),

		// ==== Guild events (GUILDS intent) ====
		// GUILD_CREATE / GUILD_UPDATE / GUILD_DELETE
		event.GuildEventHandler(func(event *dto.WSPayload, data *dto.WSGuildData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// CHANNEL_CREATE / CHANNEL_UPDATE / CHANNEL_DELETE
		event.ChannelEventHandler(func(event *dto.WSPayload, data *dto.WSChannelData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild member events (GUILD_MEMBERS intent) ====
		// GUILD_MEMBER_ADD / GUILD_MEMBER_UPDATE / GUILD_MEMBER_REMOVE
		event.GuildMemberEventHandler(func(event *dto.WSPayload, data *dto.WSGuildMemberData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild message events (GUILD_MESSAGES intent) ====
		// MESSAGE_CREATE
		event.MessageEventHandler(func(event *dto.WSPayload, data *dto.WSMessageData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// MESSAGE_DELETE
		event.MessageDeleteEventHandler(func(event *dto.WSPayload, data *dto.WSMessageDeleteData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild message reactions (GUILD_MESSAGE_REACTIONS intent) ====
		// MESSAGE_REACTION_ADD / MESSAGE_REACTION_REMOVE
		event.MessageReactionEventHandler(func(event *dto.WSPayload, data *dto.WSMessageReactionData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild @bot message events (AT_MESSAGES / GUILD_AT_MESSAGE intent) ====
		// AT_MESSAGE_CREATE
		event.ATMessageEventHandler(func(event *dto.WSPayload, data *dto.WSATMessageData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// PUBLIC_MESSAGE_DELETE
		event.PublicMessageDeleteEventHandler(func(event *dto.WSPayload, data *dto.WSPublicMessageDeleteData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild direct message events (DIRECT_MESSAGE intent) ====
		// DIRECT_MESSAGE_CREATE
		event.DirectMessageEventHandler(func(event *dto.WSPayload, data *dto.WSDirectMessageData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// DIRECT_MESSAGE_DELETE
		event.DirectMessageDeleteEventHandler(func(event *dto.WSPayload, data *dto.WSDirectMessageDeleteData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Group & C2C events (GROUP_AND_C2C_EVENT intent) ====
		// GROUP_AT_MESSAGE_CREATE
		// NOTE: extract "d" from raw bytes to preserve original fields (like member_role)
		// that botgo strips from dto.User during unmarshal.
		event.GroupATMessageEventHandler(func(event *dto.WSPayload, data *dto.WSGroupATMessageData) error {
			var raw struct {
				D json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(event.RawMessage, &raw); err != nil || raw.D == nil {
				return nil
			}
			return a.emitEvent(string(event.Type), raw.D)
		}),
		// C2C_MESSAGE_CREATE
		event.C2CMessageEventHandler(func(event *dto.WSPayload, data *dto.WSC2CMessageData) error {
			var raw struct {
				D json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(event.RawMessage, &raw); err != nil || raw.D == nil {
				return nil
			}
			return a.emitEvent(string(event.Type), raw.D)
		}),
		// FRIEND_ADD / FRIEND_DEL
		event.C2CFriendEventHandler(func(event *dto.WSPayload, data *dto.WSC2CFriendData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// C2C_MSG_REJECT / C2C_MSG_RECEIVE / GROUP_MSG_REJECT / GROUP_MSG_RECEIVE
		event.SubscribeMsgStatusEventHandler(func(event *dto.WSPayload, data *dto.WSSubscribeMsgStatus) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// Enter AIO
		event.EnterAIOEventHandler(func(event *dto.WSPayload, data *dto.WSEnterAIOData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Interaction events (INTERACTION intent) ====
		// INTERACTION_CREATE
		event.InteractionEventHandler(func(event *dto.WSPayload, data *dto.WSInteractionData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Message audit events (MESSAGE_AUDIT intent) ====
		// MESSAGE_AUDIT_PASS / MESSAGE_AUDIT_REJECT
		event.MessageAuditEventHandler(func(event *dto.WSPayload, data *dto.WSMessageAuditData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Forum events (FORUMS_EVENT intent) ====
		// FORUM_THREAD_CREATE / FORUM_THREAD_UPDATE / FORUM_THREAD_DELETE
		event.ThreadEventHandler(func(event *dto.WSPayload, data *dto.WSThreadData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// FORUM_POST_CREATE / FORUM_POST_DELETE
		event.PostEventHandler(func(event *dto.WSPayload, data *dto.WSPostData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// FORUM_REPLY_CREATE / FORUM_REPLY_DELETE
		event.ReplyEventHandler(func(event *dto.WSPayload, data *dto.WSReplyData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		// FORUM_PUBLISH_AUDIT_RESULT
		event.ForumAuditEventHandler(func(event *dto.WSPayload, data *dto.WSForumAuditData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Audio events (AUDIO_ACTION intent) ====
		// AUDIO_START / AUDIO_FINISH / AUDIO_ON_MIC / AUDIO_OFF_MIC
		event.AudioEventHandler(func(event *dto.WSPayload, data *dto.WSAudioData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// Catch-all for events not yet in botgo (e.g. GROUP_MESSAGE_CREATE)
		// NOTE: message is the full WSPayload bytes ({"op":0,"s":...,"t":"...","d":{...}}),
		// so we must extract the "d" field before passing through.
		event.PlainEventHandler(func(wsEvent *dto.WSPayload, message []byte) error {
			var raw struct {
				D json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(message, &raw); err != nil || raw.D == nil {
				return nil
			}
			return a.emitEvent(string(wsEvent.Type), raw.D)
		}),
	)

	a.logger.Info("Connecting to QQ WebSocket...")

	// Determine which intents to subscribe to:
	//   - If configured intents are provided, use those (respecting public bot limits)
	//   - Otherwise fall back to the union of all registered handlers (original behaviour)
	sessionIntent := intent
	if len(a.intents) > 0 {
		sessionIntent = types.IntentsToBitmask(a.intents)
		a.logger.Info("using configured intents", "intents", a.intents, "bitmask", sessionIntent)
	}

	// 6. Start session manager with automatic reconnection loop
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			sessionManager := botgo.NewSessionManager()
			if err := sessionManager.Start(wsAP, tokenSource, &sessionIntent); err != nil {
				// Normal: session ended, will reconnect with backoff
				a.logger.Warn("WebSocket session ended, reconnecting...",
					"error", err,
				)
			} else {
				// Clean exit (context cancelled), stop reconnecting
				return
			}

			// Read current backoff and grow for next failure
			a.backoffMu.Lock()
			wait := a.backoffCur
			a.backoffCur = time.Duration(float64(a.backoffCur) * a.backoff.Factor)
			if a.backoffCur > a.backoff.Max {
				a.backoffCur = a.backoff.Max
			}
			a.backoffMu.Unlock()

			// Wait with backoff before reconnection attempt
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}

			// Re-fetch WebSocket gateway (URL may change)
			newWSAP, err := api.WS(ctx, nil, "")
			if err != nil {
				a.logger.Error("failed to get WebSocket gateway for reconnection, will retry", "error", err)
				continue
			}
			wsAP = newWSAP
		}
	}()

	return nil
}

// emitEvent serializes a data object with the native QQ API event type
// in the format matching WSPayload: {"t": "EVENT_TYPE", "d": <data>}
func (a *WebSocketAdapter) emitEvent(eventType string, data interface{}) error {
	raw, err := json.Marshal(map[string]interface{}{
		"t": eventType,
		"d": data,
	})
	if err != nil {
		a.logger.Error("failed to marshal event", "error", err)
		return nil
	}

	select {
	case a.events <- raw:
	default:
		a.logger.Warn("event channel full, dropping event", "type", eventType)
	}
	return nil
}

// Stop gracefully closes the WebSocket connection.
func (a *WebSocketAdapter) Stop(ctx context.Context) error {
	a.closeOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
	})
	// Wait for the session goroutine to finish
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	close(a.events)
	a.logger.Info("WebSocket adapter stopped")
	return nil
}

// Events returns the channel for consuming raw events.
func (a *WebSocketAdapter) Events() <-chan []byte {
	return a.events
}

// BotUserID returns the bot's own user ID from the QQ session.
func (a *WebSocketAdapter) BotUserID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.botUserID
}

// ---
// WebhookAdapter
// ---

// WebhookAdapter implements contract.Adapter using HTTP Webhook via botgo's HTTPHandler.
type WebhookAdapter struct {
	name      string
	appID     string
	appSecret string
	port      int
	path      string
	events    chan []byte
	logger    *log.Logger
	server    *http.Server
	closeOnce sync.Once
}

// NewWebhookAdapter creates a new Webhook adapter.
func NewWebhookAdapter(appID, appSecret string, port int, path string, logger *log.Logger) *WebhookAdapter {
	if port == 0 {
		port = 9000
	}
	if path == "" {
		path = "/webhook"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &WebhookAdapter{
		name:      "webhook",
		appID:     appID,
		appSecret: appSecret,
		port:      port,
		path:      path,
		events:    make(chan []byte, 256),
		logger:    logger,
	}
}

func (a *WebhookAdapter) Name() string { return a.name }

// Start begins the HTTP server for receiving webhook events.
// Registers event handlers and uses botgo's webhook.HTTPHandler for
// correct signature verification, OpCode 13 validation, heartbeat ACK, and event dispatch.
func (a *WebhookAdapter) Start(ctx context.Context) error {
	// Skip actual server if credentials are placeholder values
	if a.appID == "" || a.appID == "your_app_id_here" {
		a.logger.Warn("Webhook adapter: app_id not configured, skipping server")
		return nil
	}

	botgo.SetLogger(&webhookLogger{inner: a.logger})

	// Register event handlers — same handlers used by WebSocket adapter.
	// These use a.emitEvent() to push events to the channel for bot processing.
	event.RegisterHandlers(
		// ==== Guild events (GUILDS intent) ====
		event.GuildEventHandler(func(event *dto.WSPayload, data *dto.WSGuildData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.ChannelEventHandler(func(event *dto.WSPayload, data *dto.WSChannelData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild member events (GUILD_MEMBERS intent) ====
		event.GuildMemberEventHandler(func(event *dto.WSPayload, data *dto.WSGuildMemberData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild message events (GUILD_MESSAGES intent) ====
		event.MessageEventHandler(func(event *dto.WSPayload, data *dto.WSMessageData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.MessageDeleteEventHandler(func(event *dto.WSPayload, data *dto.WSMessageDeleteData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild message reactions (GUILD_MESSAGE_REACTIONS intent) ====
		event.MessageReactionEventHandler(func(event *dto.WSPayload, data *dto.WSMessageReactionData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild @bot message events (AT_MESSAGES intent) ====
		event.ATMessageEventHandler(func(event *dto.WSPayload, data *dto.WSATMessageData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.PublicMessageDeleteEventHandler(func(event *dto.WSPayload, data *dto.WSPublicMessageDeleteData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Guild direct message events (DIRECT_MESSAGE intent) ====
		event.DirectMessageEventHandler(func(event *dto.WSPayload, data *dto.WSDirectMessageData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.DirectMessageDeleteEventHandler(func(event *dto.WSPayload, data *dto.WSDirectMessageDeleteData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Group & C2C events (GROUP_AND_C2C_EVENT intent) ====
		event.GroupATMessageEventHandler(func(event *dto.WSPayload, data *dto.WSGroupATMessageData) error {
			var raw struct {
				D json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(event.RawMessage, &raw); err != nil || raw.D == nil {
				return nil
			}
			return a.emitEvent(string(event.Type), raw.D)
		}),
		event.C2CMessageEventHandler(func(event *dto.WSPayload, data *dto.WSC2CMessageData) error {
			var raw struct {
				D json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(event.RawMessage, &raw); err != nil || raw.D == nil {
				return nil
			}
			return a.emitEvent(string(event.Type), raw.D)
		}),
		event.C2CFriendEventHandler(func(event *dto.WSPayload, data *dto.WSC2CFriendData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.SubscribeMsgStatusEventHandler(func(event *dto.WSPayload, data *dto.WSSubscribeMsgStatus) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.EnterAIOEventHandler(func(event *dto.WSPayload, data *dto.WSEnterAIOData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Interaction events (INTERACTION intent) ====
		event.InteractionEventHandler(func(event *dto.WSPayload, data *dto.WSInteractionData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Message audit events (MESSAGE_AUDIT intent) ====
		event.MessageAuditEventHandler(func(event *dto.WSPayload, data *dto.WSMessageAuditData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Forum events (FORUMS_EVENT intent) ====
		event.ThreadEventHandler(func(event *dto.WSPayload, data *dto.WSThreadData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.PostEventHandler(func(event *dto.WSPayload, data *dto.WSPostData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.ReplyEventHandler(func(event *dto.WSPayload, data *dto.WSReplyData) error {
			return a.emitEvent(string(event.Type), data)
		}),
		event.ForumAuditEventHandler(func(event *dto.WSPayload, data *dto.WSForumAuditData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// ==== Audio events (AUDIO_ACTION intent) ====
		event.AudioEventHandler(func(event *dto.WSPayload, data *dto.WSAudioData) error {
			return a.emitEvent(string(event.Type), data)
		}),

		// Catch-all for events not in botgo
		event.PlainEventHandler(func(wsEvent *dto.WSPayload, message []byte) error {
			var raw struct {
				D json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(message, &raw); err != nil || raw.D == nil {
				return nil
			}
			return a.emitEvent(string(wsEvent.Type), raw.D)
		}),
	)

	// Create botgo credentials for the HTTPHandler
	credentials := &token.QQBotCredentials{
		AppID:     a.appID,
		AppSecret: a.appSecret,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(a.path, func(w http.ResponseWriter, r *http.Request) {
		// Use botgo's built-in webhook handler which handles:
		//   - Signature verification (OpCode 13 validation)
		//   - Heartbeat ACK (OpCode 11)
		//   - Event dispatch ACK (OpCode 12)
		//   - All registered event handlers
		webhook.HTTPHandler(w, r, credentials)
	})

	a.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", a.port),
		Handler: mux,
	}

	// Start server in background
	go func() {
		a.logger.Info("Webhook adapter listening",
			"addr", a.server.Addr,
			"path", a.path,
		)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("webhook server error", "error", err)
		}
	}()

	// Stop server when context is cancelled
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			a.logger.Error("webhook server shutdown error", "error", err)
		}
	}()

	return nil
}

func (a *WebhookAdapter) Stop(ctx context.Context) error {
	a.closeOnce.Do(func() {
		if a.server != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.server.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("webhook server shutdown error", "error", err)
			}
		}
	})
	close(a.events)
	a.logger.Info("Webhook adapter stopped")
	return nil
}

func (a *WebhookAdapter) Events() <-chan []byte {
	return a.events
}

// BotUserID returns the bot's own user ID (not available via webhook).
func (a *WebhookAdapter) BotUserID() string { return "" }

// emitEvent serializes a data object with the native QQ API event type
// in the format matching WSPayload: {"t": "EVENT_TYPE", "d": <data>}
func (a *WebhookAdapter) emitEvent(eventType string, data interface{}) error {
	raw, err := json.Marshal(map[string]interface{}{
		"t": eventType,
		"d": data,
	})
	if err != nil {
		a.logger.Error("failed to marshal event", "error", err)
		return nil
	}

	select {
	case a.events <- raw:
	default:
		a.logger.Warn("event channel full, dropping event", "type", eventType)
	}
	return nil
}

// webhookLogger bridges botgo's log.Logger to our log.Logger.
type webhookLogger struct {
	inner *log.Logger
}

func (l *webhookLogger) Debug(v ...interface{}) { l.inner.Debug(fmt.Sprint(v...)) }
func (l *webhookLogger) Info(v ...interface{})  { l.inner.Info(fmt.Sprint(v...)) }
func (l *webhookLogger) Warn(v ...interface{})  { l.inner.Warn(fmt.Sprint(v...)) }
func (l *webhookLogger) Error(v ...interface{}) { l.inner.Error(fmt.Sprint(v...)) }
func (l *webhookLogger) Debugf(format string, v ...interface{}) {
	l.inner.Debug(fmt.Sprintf(format, v...))
}
func (l *webhookLogger) Infof(format string, v ...interface{}) {
	l.inner.Info(fmt.Sprintf(format, v...))
}
func (l *webhookLogger) Warnf(format string, v ...interface{}) {
	l.inner.Warn(fmt.Sprintf(format, v...))
}
func (l *webhookLogger) Errorf(format string, v ...interface{}) {
	l.inner.Error(fmt.Sprintf(format, v...))
}
func (l *webhookLogger) Sync() error { return nil }

// BackoffConfig controls reconnection backoff behavior.
type BackoffConfig struct {
	Initial time.Duration
	Max     time.Duration
	Factor  float64
}

// DefaultBackoff is the default reconnection backoff configuration.
var DefaultBackoff = BackoffConfig{
	Initial: 100 * time.Millisecond,
	Max:     30 * time.Second,
	Factor:  2.0,
}
