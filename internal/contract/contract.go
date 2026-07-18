// Package contract defines the core interfaces for the LQBOT framework.
package contract

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Logger abstracts logging for plugins.
type Logger interface {
	Debug(msg string, keysAndValues ...interface{})
	Info(msg string, keysAndValues ...interface{})
	Warn(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
}

// Storage provides key-value data persistence for plugins.
type Storage interface {
	Get(key string, dest interface{}) error
	Set(key string, value interface{}) error
	Delete(key string) error
	Close() error
	Driver() string
}

// QQAPI abstracts calls to the QQ Official API v2.
// All methods support both passive reply and active push where applicable.
// See https://bot.q.qq.com/wiki/develop/api-v2/ for more details.
type QQAPI interface {
	// === Channel message sending (文字子频道) ===
	// SendMessage sends a plain text message to a text channel.
	SendMessage(channelID, content string) error
	// SendMarkdown sends a markdown message to a text channel.
	SendMarkdown(channelID, content string) error
	// ReplyMarkdown replies to a specific message with markdown content (passive reply).
	// The msgID is the original message being replied to. For public bots, passive
	// replies are less restrictive than active sends.
	ReplyMarkdown(channelID, msgID, content string) error
	// ReplyImage replies to a specific message with an image (passive reply).
	ReplyImage(channelID, msgID, imageURL string) error
	// SendMarkdownTemplate sends a markdown template message to a text channel.
	// templateID is the custom_template_id registered on QQ Open Platform.
	SendMarkdownTemplate(channelID, templateID string, params []MarkdownParam) error
	// SendImage sends an image message to a text channel.
	SendImage(channelID, imageURL string) error
	// SendEmbedMessage sends an embed (rich card) message to a text channel.
	// Only supported in text channels and channel DMs (not group/C2C).
	SendEmbedMessage(channelID string, embed *MessageEmbed) error
	// SendArkMessage sends an ark template message to a text channel.
	// template_id must be registered on QQ Open Platform first.
	SendArkMessage(channelID string, ark *MessageArk) error
	// SendRichMedia sends a rich media message (image/video/voice/file) to a text channel.
	// fileType: 1=image, 2=video, 3=voice, 4=file
	SendRichMedia(channelID string, media *RichMedia) error
	// SendMessageWithButtons sends a text channel message with interactive buttons.
	// Buttons are attached to a markdown message internally (MsgType: 2).
	SendMessageWithButtons(channelID string, content string, buttons []MessageButton) error

	// === Group message sending (群聊) ===
	// SendGroupMessage sends a plain text message to a group.
	SendGroupMessage(groupID string, content string) error
	// SendGroupMarkdown sends a markdown message to a group.
	SendGroupMarkdown(groupID string, content string) error
	// SendGroupMarkdownTemplate sends a markdown template message to a group.
	SendGroupMarkdownTemplate(groupID, templateID string, params []MarkdownParam) error
	// SendGroupRichMedia sends a rich media message (image/video/voice/file) to a group.
	SendGroupRichMedia(groupID string, media *RichMedia) error
	// SendGroupArkMessage sends an ark template message to a group.
	// Default template IDs: 23 (link+text list), 24 (text+thumbnail), 37 (big image).
	SendGroupArkMessage(groupID string, ark *MessageArk) error
	// SendGroupMessageWithButtons sends a group message with interactive buttons.
	SendGroupMessageWithButtons(groupID string, content string, buttons []MessageButton) error

	// === C2C / DM message sending (私聊) ===
	// SendC2CMessage sends a plain text message to a user (single chat).
	SendC2CMessage(userID, content string) error
	// SendC2CMarkdown sends a markdown message to a user.
	SendC2CMarkdown(userID, content string) error
	// SendC2CMarkdownTemplate sends a markdown template message to a user.
	SendC2CMarkdownTemplate(userID, templateID string, params []MarkdownParam) error
	// SendC2CRichMedia sends a rich media message (image/video/voice/file) to a user.
	SendC2CRichMedia(userID string, media *RichMedia) error
	// SendC2CArkMessage sends an ark template message to a user.
	SendC2CArkMessage(userID string, ark *MessageArk) error
	// SendC2CMessageWithButtons sends a C2C message with interactive buttons.
	SendC2CMessageWithButtons(userID string, content string, buttons []MessageButton) error

	// === Message management ===
	// DeleteMessage recalls/deletes a channel message.
	DeleteMessage(channelID, messageID string) error
	// DeleteGroupMessage recalls/deletes a group message.
	DeleteGroupMessage(groupID, messageID string) error
	// DeleteC2CMessage recalls/deletes a C2C message.
	DeleteC2CMessage(userID, messageID string) error
	// PinMessage pins a message in a channel as an announcement.
	PinMessage(channelID, messageID string) error
	// UnpinMessage removes a pinned message.
	UnpinMessage(channelID, messageID string) error

	// === Reactions (表情表态) ===
	// CreateReaction adds an emoji reaction to a message.
	// emoji format: "type:id", e.g. "1:4" for system emoji, or "2:❤️" for unicode.
	CreateReaction(channelID, messageID, emoji string) error
	// DeleteReaction removes own emoji reaction from a message.
	DeleteReaction(channelID, messageID, emoji string) error

	// === Interaction callback (按钮交互) ===
	// PutInteraction acknowledges an interaction (button click, etc.).
	// The body is a JSON string matching the QQ API callback format, e.g. `{"code":0}`.
	PutInteraction(interactionID string, body string) error
	// ReplyInteraction acknowledges an interaction and replies with a text message.
	ReplyInteraction(interactionID string, content string) error

	// === Active push (主动推送) ===
	// SendActiveC2CMessage sends an active (wake-up / recall) message to a user.
	// This is for the "互动召回" feature: within 30 days after user interaction,
	// the bot can send up to 4 recall messages across 4 time windows.
	// See: https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/send-receive/send.html
	SendActiveC2CMessage(userID, content string, isWakeup bool) error

	// === Bot info ===
	// AppID returns the bot's App ID (from config), used for constructing
	// platform URLs such as the user avatar endpoint:
	//   https://q.qlogo.cn/qqapp/{appid}/{openid}/640
	AppID() string

	// === Guild / Channel info ===
	// GetGuild retrieves guild (server) information by ID.
	GetGuild(guildID string) (*Guild, error)
	// GetChannel retrieves channel information by ID.
	GetChannel(channelID string) (*Channel, error)
	// GetGuildMember retrieves a guild member's information.
	GetGuildMember(guildID, userID string) (*Member, error)

	// === Channel media upload ===
	// UploadChannelMedia uploads a media file to a channel and returns the file_info string.
	// This is required before sending rich media messages to text channels.
	// fileType: 1=image, 2=video, 3=voice, 4=file
	// After uploading, pass the returned file_info to SendRichMedia's RichMedia.FileInfo field.
	// See: https://bot.q.qq.com/wiki/develop/api-v2/server-inter/rich-media/upload-media.html
	UploadChannelMedia(channelID string, fileType int, url string) (string, error)
}

// --- Rich message types ---

// MessageEmbed represents an embed (rich card) message.
// Supported scenes: text channel, channel DM only.
type MessageEmbed struct {
	Title       string       `json:"title,omitempty"`       // Card title
	Prompt      string       `json:"prompt,omitempty"`      // Notification prompt text
	Description string       `json:"description,omitempty"` // Card description
	Thumbnail   string       `json:"thumbnail,omitempty"`   // Thumbnail image URL
	Fields      []EmbedField `json:"fields,omitempty"`      // Text fields (max 4)
}

// EmbedField is a single text field in an embed message.
type EmbedField struct {
	Name string `json:"name"` // Field text content
}

// MessageArk represents an ark template message.
// template_id must be pre-registered on QQ Open Platform.
type MessageArk struct {
	TemplateID int     `json:"template_id"`  // Ark template ID
	KV         []ArkKV `json:"kv,omitempty"` // Key-value params
}

// ArkKV is a key-value pair for ark template parameters.
// A KV can have either a flat Value or nested Obj (but not both).
type ArkKV struct {
	Key   string   `json:"key"`
	Value string   `json:"value,omitempty"`
	Obj   []ArkObj `json:"obj,omitempty"` // Nested objects (for complex templates)
}

// ArkObj represents a nested object in ark template parameters.
type ArkObj struct {
	ObjKV []ArkObjKV `json:"obj_kv,omitempty"`
}

// ArkObjKV is a key-value pair within a nested ark object.
type ArkObjKV struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// RichMedia represents a rich media message (image/video/voice/file).
// FileType: 1=image, 2=video, 3=voice, 4=file
// URL must be pre-registered on QQ Open Platform (开发设置→消息URL配置).
// For channel messages, FileInfo (from upload API) is required instead of URL.
// For group/C2C messages, URL + FileType is sufficient.
type RichMedia struct {
	FileType int    `json:"file_type"`         // 1=image, 2=video, 3=voice, 4=file
	URL      string `json:"url,omitempty"`     // Media file URL (group/C2C)
	Content  string `json:"content,omitempty"` // Optional text description
	// SrvSendMsg controls whether the media is directly sent to
	// the target (true) or returned as a media asset for later use (false).
	SrvSendMsg bool `json:"srv_send_msg,omitempty"`
	// FileInfo is raw file_info string from the upload API (required for channel media).
	FileInfo string `json:"-"` // Not serialized to JSON
	// MsgID sets the original message ID to reply to, making it a passive reply.
	// Required for public bots to bypass active message restrictions.
	MsgID string `json:"-"`
}

// --- Named structs for GetGuild / GetChannel / GetGuildMember ---

// Guild represents a guild (QQ channel server).
type Guild struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	OwnerID     string `json:"owner_id"`
	MemberCount int    `json:"member_count"`
	MaxMembers  int64  `json:"max_members"`
	Desc        string `json:"desc"` // Guild description
}

// Channel represents a channel (sub-channel within a guild).
type Channel struct {
	ID       string `json:"id"`
	GuildID  string `json:"guild_id"`
	Name     string `json:"name"`
	Type     int    `json:"type"` // 0=text, 1=voice, etc.
	ParentID string `json:"parent_id"`
}

// Member represents a guild member.
type Member struct {
	User     *User    `json:"user,omitempty"`
	Nick     string   `json:"nick"`
	Roles    []string `json:"roles"`
	JoinedAt string   `json:"joined_at"`
}

// User represents a QQ user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
	Bot      bool   `json:"bot"`
}

// MessageButton represents an interactive button component.
// Fields align with QQ Official API v2 button action definitions.
//
// Button visual states (render_data):
//   Normal → Label     — 按钮上的文字
//   Press  → VisitedLabel — 点击后按钮上显示的文字（为空时与 Label 相同）
//   Loading → 客户端自动展示 loading 动画，需通过 DeferReply/PutInteraction 响应解除
type MessageButton struct {
	ID    string `json:"id"`    // Button ID (unique within a message)
	Label string `json:"label"` // Normal state: button label text
	Style int    `json:"style"` // 0: grey outline, 1: blue outline, 3: red text+white bg, 4: blue bg+white text

	// VisitedLabel sets the button text after being clicked (Press state).
	// When empty, defaults to Label text.
	VisitedLabel string `json:"visited_label,omitempty"`
	Data  string `json:"data"`  // Action data: callback data (type=1), command (type=2)

	// URL sets the button as a jump button (ActionType=0).
	// Mutually exclusive with ActionType - when URL is set, ActionType defaults to 0 (jump).
	// This avoids the Go zero-value ambiguity where int 0 clashes with QQ API's jump type 0.
	URL string `json:"url,omitempty"` // Jump URL (http/mini-program/scheme)

	// Action type (default: 2 = command when neither ActionType nor URL is set)
	//   0 = jump/URL - use the URL field instead (to avoid Go zero-value ambiguity)
	//   1 = callback button (Data sent to interaction callback)
	//   2 = command button (auto-inserts "@bot Data" in input)
	ActionType int `json:"action_type,omitempty"`

	// Permission type (default: 2 = everyone)
	//   0 = specific users (SpecifyUserIDs)
	//   1 = manager only
	//   2 = everyone
	//   3 = specific roles, channel only (SpecifyRoleIDs)
	Permission int `json:"permission,omitempty"`

	// SpecifyUserIDs limits the button to specific users (Permission=0).
	SpecifyUserIDs []string `json:"specify_user_ids,omitempty"`
	// SpecifyRoleIDs limits the button to specific roles, channel only (Permission=3).
	SpecifyRoleIDs []string `json:"specify_role_ids,omitempty"`

	// Command button fields (ActionType=2):
	Enter         bool   `json:"enter,omitempty"`          // click to auto-send command (C2C only, default false)
	Reply         bool   `json:"reply,omitempty"`          // quote-reply the original message (default false)
	Anchor        int    `json:"anchor,omitempty"`         // 1 = auto-open image picker (mobile 8983+ C2C only)
	UnsupportTips string `json:"unsupport_tips,omitempty"` // toast message when client doesn't support this action
}

// InteractionData represents the data from an interaction callback (button click, etc.).
type InteractionData struct {
	ID          string `json:"id"`
	Type        int    `json:"type"`        // Interaction type
	ButtonID    string `json:"button_id"`   // The button/component ID
	ButtonData  string `json:"button_data"` // The button's callback data
	ChannelID   string `json:"channel_id"`
	GuildID     string `json:"guild_id"`
	GroupOpenID string `json:"group_openid"` // Group open ID (for group chat interactions)
	UserID      string `json:"user_id"`
	UserOpenID  string `json:"user_openid"` // C2C user open ID (for C2C interactions)
	MessageID   string `json:"message_id"`
	Scene       string `json:"scene"` // "guild", "group", or "c2c"
}

// InteractionHandler is the function signature for interaction event handling.
type InteractionHandler func(ctx InteractionContext) error

// InteractionContext provides the execution environment for interaction events.
// Interaction events are triggered when a user clicks a button or selects a menu option.
type InteractionContext interface {
	InteractionData() *InteractionData // Raw interaction data
	ButtonID() string                  // The clicked button ID
	ButtonData() string                // The button's callback data
	UserID() string                    // User who triggered the interaction
	ChannelID() string                 // Source channel/group ID
	GroupOpenID() string               // Group open ID (empty for non-group scenes)
	UserOpenID() string                // C2C user open ID (empty for non-C2C scenes)
	MessageID() string                 // The original message ID
	Reply(msg string) error            // Send a text message (auto-routes to channel or group)
	Callback(content string) error     // Acknowledge the interaction with a reply
	DeferReply() error                 // Acknowledge the interaction without reply (for long processing)
}

// Command defines a chat command that users can trigger.
// Users may send the command with or without a "/" prefix.
// In group chats, @mentions of the bot are stripped before matching.
type Command struct {
	Name        string         // Command name, e.g. "ping"
	Aliases     []string       // Aliases, e.g. ["p"]
	Description string         // Command description
	Usage       string         // Usage example, e.g. "ping [times]"
	Permission  string         // Required permission node (checked by middleware)
	Handler     CommandHandler // Execution function
}

// CommandContext provides the execution environment for a command.
type CommandContext interface {
	Args() []string         // Space-split argument list
	Arg(i int) string       // Get the i-th argument (0-based)
	ArgCount() int          // Number of arguments
	Reply(msg string) error // Quick reply to the same channel
	EventContext            // Embedded event context with message source info
}

// CommandHandler is the function signature for command execution.
type CommandHandler func(ctx CommandContext) error

// Listener listens for specific QQ event types.
// The framework ensures events are delivered after Start() and stopped after Stop().
type Listener struct {
	Event   string       // Event type identifier, e.g. "message.create"
	Handler EventHandler // Handler function
	Order   int          // Execution order (lower values run first, default 0)
}

// EventContext provides the execution environment for an event handler.
// Fields align with QQ Official API v2 dto.Message fields.
type EventContext interface {
	Content() string           // Message text (with @bot prefix stripped)
	RawContent() string        // Raw message text (including @bot)
	ChannelID() string         // Source channel/group ID
	AuthorID() string          // Sender ID
	Role() string              // Sender role in group: "owner", "admin", "member" (empty for non-group)
	MessageID() string         // Message ID
	IsMentioned() bool         // Whether the bot was @mentioned
	GuildID() string           // Guild/server ID (empty for C2C/group)
	GroupID() string           // Group chat ID (empty for guild/C2C)
	Mentions() []string        // IDs of all mentioned users (empty if none)
	Attachments() []Attachment // Attachments (images, files, etc.)
	Scene() MessageScene       // Scene of the message: Guild, Group, or C2C
	Reply(msg string) error    // Quick reply to the same channel
	// ReplyMarkdown sends a markdown message as a passive reply to the original message.
	// Uses msg_id for passive reply to bypass active push restrictions for public bots.
	ReplyMarkdown(content string) error
	// ReplyImage sends an image as a passive reply to the original message.
	// Uses msg_id for passive reply to bypass active push restrictions for public bots.
	ReplyImage(url string) error
	// ReplyWithButtons sends a markdown message with interactive buttons
	// as a passive reply to the original message.
	ReplyWithButtons(content string, buttons []MessageButton) error
	// ReplyWithButtonRows sends a markdown message with multi-row buttons.
	// Each inner slice represents one row of buttons.
	// Example: [][]MessageButton{{btn1}, {btn2, btn3}} = row1: 1 btn, row2: 2 btns
	ReplyWithButtonRows(content string, rows [][]MessageButton) error
	// ReplyArk sends an Ark template message as a passive reply.
	ReplyArk(ark *MessageArk) error
	// ReplyMarkdownTemplate sends a templated markdown message as a passive reply.
	ReplyMarkdownTemplate(templateID string, params []MarkdownParam) error
}

// MessageScene identifies the source scene of a message.
type MessageScene int

const (
	SceneGuild MessageScene = iota // Guild channel message (MESSAGE_CREATE / AT_MESSAGE_CREATE)
	SceneGroup                     // Group chat message (GROUP_AT_MESSAGE_CREATE)
	SceneC2C                       // Direct message (C2C_MESSAGE_CREATE)
)

// Attachment represents a file/media attachment in a message.
type Attachment struct {
	URL      string `json:"url"`       // Download URL
	FileName string `json:"file_name"` // Original file name
	MimeType string `json:"mime_type"` // MIME type (image/png, etc.)
	Width    int    `json:"width"`     // Image width (0 for non-images)
	Height   int    `json:"height"`    // Image height (0 for non-images)
}

// EventHandler is the function signature for event handling.
type EventHandler func(ctx EventContext) error

// Middleware defines a message processing pipeline.
// Middleware can intercept, modify, or filter messages before they reach plugins.
type Middleware interface {
	Name() string
	Order() int // Execution order (lower values run first)
	Handle(ctx EventContext, next func() error) error
}

// Adapter abstracts protocol transport (WebSocket / Webhook).
// The framework uses this to receive events from QQ.
type Adapter interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Events() <-chan []byte // Raw event stream from QQ
	BotUserID() string     // Bot's own user ID from QQ session
}

// CommandRegister is the narrow interface plugins use to register commands.
// Plugins receive this instead of the full router to keep coupling minimal.
type CommandRegister interface {
	Register(cmd Command)
}

// ListenerRegister is the narrow interface plugins use to subscribe to events.
type ListenerRegister interface {
	Subscribe(listener Listener)
}

// Scheduler is the narrow interface for registering scheduled/cron tasks.
type Scheduler interface {
	// Every registers a cron expression task. Cron format: "0 8 * * *" (sec min hour dom mon dow).
	Every(cronExpr string, fn func()) error
	// Interval registers a task that runs repeatedly at the given interval.
	Interval(d time.Duration, fn func()) error
}

// HTTPServer is the narrow interface for registering HTTP routes.
// Only available when the embedded HTTP server is enabled in config.
type HTTPServer interface {
	// Handle registers an HTTP handler for the given path.
	// Path should start with "/", e.g. "/webhook/custom".
	Handle(path string, handler http.HandlerFunc)
	// ServeMux returns the underlying http.ServeMux for advanced use.
	ServeMux() *http.ServeMux
}

// Plugin is the optional interface a plugin package can implement
// for lifecycle-aware initialization.
type Plugin interface {
	// Name returns a unique plugin identifier.
	Name() string
	// Init is called during bot startup. Use it to register commands, listeners, etc.
	Init(reg *PluginContext) error
}

// PluginContext provides plugins access to framework services during Init.
type PluginContext struct {
	Commands  CommandRegister
	Listeners ListenerRegister
	Logger    Logger
	Storage   Storage
	QQAPI     QQAPI

	// PluginConfig is the optional configuration section from config.yaml for this plugin.
	// The value comes from config.plugins[plugin.Name()] and is nil if not configured.
	PluginConfig interface{}

	// SharedConfig is the shared configuration section from config.yaml for all plugins.
	// The value comes from config.plugins["mc"] and is nil if not configured.
	// Use this for common settings shared across multiple plugins (e.g. MC server URL).
	SharedConfig interface{}

	// Scheduler for registering cron/interval tasks.
	// May be nil if the scheduler is not initialized.
	Scheduler Scheduler

	// HTTPServer for registering HTTP routes.
	// Only available when the embedded HTTP server is enabled.
	// May be nil if the HTTP server is not initialized.
	HTTPServer HTTPServer

	// RawDB provides direct database access for plugins that need custom tables.
	// Type-assert to *gorm.DB to use (requires importing gorm.io/gorm).
	RawDB interface{}
}

// ── Text chain interactive elements ──
// See: https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/trans/text-chain.html

// MentionUser returns a text chain element that @-mentions a specific user.
// Usable in: group chat, text channel, markdown messages.
// Format: <qqbot-at-user id="userID" />
// Deprecated format (<@userid>) will be removed in the future.
func MentionUser(userID string) string {
	return "<qqbot-at-user id=\"" + userID + "\" />"
}

// MentionEveryone returns a text chain element that @-mentions everyone.
// Only usable in text channels (not group chat).
// Requires bot permission to @everyone.
func MentionEveryone() string {
	return "<qqbot-at-everyone />"
}

// CmdEnter creates a clickable command tag that immediately sends text on click.
// Only supported in markdown messages (C2C scene only; not in group/text channel).
// text is URL-encoded automatically (per QQ API requirement), max 100 chars.
func CmdEnter(text string) string {
	return "<qqbot-cmd-enter text=\"" + url.QueryEscape(text) + "\" />"
}

// CmdInput creates a clickable command tag that inserts text into the input box.
// Only supported in markdown messages (C2C scene only; not in group/text channel).
// text: inserted text (urlencoded, max 100 chars)
// show: displayed text (optional, defaults to text)
// reference: whether to include message reply reference (optional, defaults to false)
func CmdInput(text, show string, reference bool) string {
	s := "<qqbot-cmd-input text=\"" + url.QueryEscape(text) + "\""
	if show != "" {
		s += " show=\"" + url.QueryEscape(show) + "\""
	}
	if reference {
		s += " reference=\"true\""
	}
	s += " />"
	return s
}

// ChannelLink creates a clickable channel link.
// Only usable within the same guild.
// example: <#123456> displays as "#general"
func ChannelLink(channelID string) string {
	return "<#" + channelID + ">"
}

// Emoji returns a system emoji text chain element.
// Only supports type=1 system emojis. For type=2 unicode emojis, use the string directly.
// See: https://bot.q.qq.com/wiki/develop/api-v2/openapi/emoji/model.html
func Emoji(id string) string {
	return "<emoji:" + id + ">"
}

// MarkdownParam represents a key-value parameter for markdown template messages.
type MarkdownParam struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// ── ButtonReplyStore: 存储按钮消息的 msg_id 用于被动回复 ──

var (
	buttonMsgStore = make(map[string]string) // groupOpenID → originalCmdMsgID
	buttonMsgMu    sync.RWMutex
)

// StoreButtonMsgID 存储群组的原始命令消息 ID，供按钮交互回调被动回复使用。
// groupID: 群 open ID（即 GroupOpenID）
// msgID: 原始命令消息的 ID
func StoreButtonMsgID(groupID, msgID string) {
	buttonMsgMu.Lock()
	buttonMsgStore[groupID] = msgID
	buttonMsgMu.Unlock()
}

// GetButtonMsgID 获取群组存储的命令消息 ID，用于被动回复。
func GetButtonMsgID(groupID string) string {
	buttonMsgMu.RLock()
	defer buttonMsgMu.RUnlock()
	return buttonMsgStore[groupID]
}

// ClearButtonMsgID 清除群组存储的消息 ID。
func ClearButtonMsgID(groupID string) {
	buttonMsgMu.Lock()
	delete(buttonMsgStore, groupID)
	buttonMsgMu.Unlock()
}

// ── MsgSeqStore: 按钮被动回复消息序号（防去重） ──

var (
	msgSeqStore = make(map[string]uint32) // groupOpenID → current seq
	msgSeqMu    sync.RWMutex
)

// NextMsgSeq 获取并递增群组的消息序号，用于 msg_seq 字段防去重。
// groupID: 群 open ID（即 GroupOpenID）
func NextMsgSeq(groupID string) uint32 {
	msgSeqMu.Lock()
	defer msgSeqMu.Unlock()
	msgSeqStore[groupID]++
	return msgSeqStore[groupID]
}
