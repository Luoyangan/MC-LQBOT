// Package types defines public types, constants, and enumerations for LQBOT.
// Event types and intents align with QQ Official API v2.
package types

import "github.com/tencent-connect/botgo/dto"

// Event type constants from QQ Official API v2.
// These map to the "t" field in WebSocket WSPayload and "t" field in webhook payloads.
const (
	// === Guild events (GUILDS intent) ===
	EventGuildCreate   = string(dto.EventGuildCreate)   // "GUILD_CREATE"
	EventGuildUpdate   = string(dto.EventGuildUpdate)   // "GUILD_UPDATE"
	EventGuildDelete   = string(dto.EventGuildDelete)   // "GUILD_DELETE"
	EventChannelCreate = string(dto.EventChannelCreate) // "CHANNEL_CREATE"
	EventChannelUpdate = string(dto.EventChannelUpdate) // "CHANNEL_UPDATE"
	EventChannelDelete = string(dto.EventChannelDelete) // "CHANNEL_DELETE"

	// === Guild member events (GUILD_MEMBERS intent) ===
	EventMemberJoin   = string(dto.EventGuildMemberAdd)    // "GUILD_MEMBER_ADD"
	EventMemberUpdate = string(dto.EventGuildMemberUpdate) // "GUILD_MEMBER_UPDATE"
	EventMemberLeave  = string(dto.EventGuildMemberRemove) // "GUILD_MEMBER_REMOVE"

	// === Guild message events (GUILD_MESSAGES intent) ===
	EventMessageCreate = string(dto.EventMessageCreate) // "MESSAGE_CREATE"
	EventMessageDelete = string(dto.EventMessageDelete) // "MESSAGE_DELETE"

	// === Guild message reactions (GUILD_MESSAGE_REACTIONS intent) ===
	EventReactionAdd    = string(dto.EventMessageReactionAdd)    // "MESSAGE_REACTION_ADD"
	EventReactionRemove = string(dto.EventMessageReactionRemove) // "MESSAGE_REACTION_REMOVE"

	// === Guild @bot message events (AT_MESSAGES / GUILD_AT_MESSAGE intent) ===
	EventAtMessageCreate   = string(dto.EventAtMessageCreate)   // "AT_MESSAGE_CREATE"
	EventPublicMessageDel  = string(dto.EventPublicMessageDelete) // "PUBLIC_MESSAGE_DELETE"

	// === Guild direct message events (DIRECT_MESSAGE intent) ===
	EventDirectMsgCreate = string(dto.EventDirectMessageCreate) // "DIRECT_MESSAGE_CREATE"
	EventDirectMsgDelete = string(dto.EventDirectMessageDelete) // "DIRECT_MESSAGE_DELETE"

	// === Group & C2C events (GROUP_AND_C2C_EVENT intent) ===
	EventGroupAtMessageCreate = string(dto.EventGroupAtMessageCreate) // "GROUP_AT_MESSAGE_CREATE"
	EventC2CMessageCreate     = string(dto.EventC2CMessageCreate)     // "C2C_MESSAGE_CREATE"
	EventFriendAdd            = string(dto.EventC2CFriendAdd)          // "FRIEND_ADD"
	EventFriendDel            = string(dto.EventC2CFriendDel)          // "FRIEND_DEL"
	EventSubscribeMsgStatus   = string(dto.EventSubscribeMsgStatus)   // "SUBSCRIBE_MESSAGE_STATUS"
	EventEnterAIO             = string(dto.EventEnterAIO)             // "ENTER_AIO"
	// Group messages (new QQ API v2 event, no @ required)
	EventGroupMessageCreate = "GROUP_MESSAGE_CREATE"

	// === Interaction events (INTERACTION intent) ===
	EventInteractionCreate = string(dto.EventInteractionCreate) // "INTERACTION_CREATE"

	// === Message audit events (MESSAGE_AUDIT intent) ===
	EventAuditPass   = string(dto.EventMessageAuditPass)   // "MESSAGE_AUDIT_PASS"
	EventAuditReject = string(dto.EventMessageAuditReject) // "MESSAGE_AUDIT_REJECT"

	// === Forum events (FORUMS_EVENT intent) ===
	EventForumThreadCreate  = string(dto.EventForumThreadCreate)  // "FORUM_THREAD_CREATE"
	EventForumThreadUpdate  = string(dto.EventForumThreadUpdate)  // "FORUM_THREAD_UPDATE"
	EventForumThreadDelete  = string(dto.EventForumThreadDelete)  // "FORUM_THREAD_DELETE"
	EventForumPostCreate    = string(dto.EventForumPostCreate)    // "FORUM_POST_CREATE"
	EventForumPostDelete    = string(dto.EventForumPostDelete)    // "FORUM_POST_DELETE"
	EventForumReplyCreate   = string(dto.EventForumReplyCreate)   // "FORUM_REPLY_CREATE"
	EventForumReplyDelete   = string(dto.EventForumReplyDelete)   // "FORUM_REPLY_DELETE"
	EventForumAuditResult   = string(dto.EventForumAuditResult)   // "FORUM_PUBLISH_AUDIT_RESULT"

	// === Audio events (AUDIO_ACTION intent) ===
	EventAudioStart  = string(dto.EventAudioStart)  // "AUDIO_START"
	EventAudioFinish = string(dto.EventAudioFinish) // "AUDIO_FINISH"
	EventAudioOnMic  = string(dto.EventAudioOnMic)  // "AUDIO_ON_MIC"
	EventAudioOffMic = string(dto.EventAudioOffMic) // "AUDIO_OFF_MIC"
)

// IntentList maps human-readable intent names to botgo's dto.Intent bitmask values.
// These correspond to the intents field in config.yaml.
var IntentList = map[string]dto.Intent{
	"GUILDS":              dto.IntentGuilds,
	"GUILD_MEMBERS":       dto.IntentGuildMembers,
	"GUILD_MESSAGES":      dto.IntentGuildMessages,
	"GUILD_MESSAGE_REACTIONS": dto.IntentGuildMessageReactions,
	"GUILD_AT_MESSAGE":    dto.IntentGuildAtMessage,
	"AT_MESSAGES":         dto.IntentGuildAtMessage, // alias for GUILD_AT_MESSAGE
	"DIRECT_MESSAGE":      dto.IntentDirectMessages,
	"GROUP_AND_C2C_EVENT": dto.IntentGroupMessages,
	"INTERACTION":         dto.IntentInteraction,
	"MESSAGE_AUDIT":       dto.IntentAudit,
	"FORUMS_EVENT":        dto.IntentForum,
	"AUDIO_ACTION":        dto.IntentAudio,
}

// IntentsToBitmask converts a list of human-readable intent names to a dto.Intent bitmask.
// Unknown intent names are silently ignored (logged at debug level).
func IntentsToBitmask(intents []string) dto.Intent {
	var mask dto.Intent
	for _, name := range intents {
		if bit, ok := IntentList[name]; ok {
			mask |= bit
		}
	}
	return mask
}

// AccessType defines how the bot connects to QQ.
type AccessType string

const (
	AccessWebSocket AccessType = "websocket"
	AccessWebhook   AccessType = "webhook"
)

// LogLevel defines logging verbosity.
type LogLevel string

const (
	LogLevelTrace LogLevel = "trace"
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// StorageDriver defines the backend storage type.
type StorageDriver string

const (
	StorageSQLite StorageDriver = "sqlite"
	StorageMySQL  StorageDriver = "mysql"
	StorageRedis  StorageDriver = "redis"
)

// Config represents the top-level application configuration.
type Config struct {
	AppID      string        `yaml:"app_id"`
	AppSecret  string        `yaml:"app_secret"`
	Sandbox    bool          `yaml:"sandbox"` // true = sandbox mode, false = production mode
	Intents    []string      `yaml:"intents"` // Human-readable names, e.g. ["GUILD_MESSAGES", "GROUP_AND_C2C_EVENT"]
	AccessType AccessType    `yaml:"access_type"`
	LogLevel   LogLevel      `yaml:"log_level"`
	LogLevelDB LogLevel      `yaml:"log_level_db"` // 数据库日志级别（独立于控制台），空=不写入DB
	LogDBExclude []string    `yaml:"log_db_exclude"` // 数据库日志排除关键词列表（消息包含任一关键词则不写入DB）
	LogNoColor bool          `yaml:"log_no_color"` // Force disable ANSI color in log output
	Webhook    WebhookConfig `yaml:"webhook"`
	Storage    StorageConfig `yaml:"storage"`
	HTTP       HTTPConfig    `yaml:"http"`       // 内嵌 HTTP 服务配置
	Permissions map[string]string `yaml:"permissions"` // 指令权限映射: 指令名 → 权限级别
	Plugins    map[string]interface{} `yaml:"plugins"` // 插件独立配置
}

// WebhookConfig represents webhook adapter configuration.
type WebhookConfig struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// HTTPConfig represents the embedded HTTP server configuration.
type HTTPConfig struct {
	Enabled  bool   `yaml:"enabled"`   // 是否启用内嵌 HTTP 服务
	Port     int    `yaml:"port"`      // HTTP 监听端口（默认 80）
	Admin    bool   `yaml:"admin"`     // 是否启用网页管理界面
	CertFile string `yaml:"cert_file"` // SSL 证书文件路径（为空则不启用 SSL）
	KeyFile  string `yaml:"key_file"`  // SSL 私钥文件路径（为空则不启用 SSL）
}

// SQLiteConfig represents SQLite-specific storage configuration.
type SQLiteConfig struct {
	DSN string `yaml:"dsn"` // 数据库文件路径
}

// MySQLConfig represents MySQL-specific storage configuration.
type MySQLConfig struct {
	User string `yaml:"user"` // 数据库用户名
	Pass string `yaml:"pass"` // 数据库密码
	Host string `yaml:"host"` // 数据库主机
	Port int    `yaml:"port"` // 数据库端口
	DB   string `yaml:"db"`   // 数据库名称
}

// StorageConfig represents storage backend configuration.
type StorageConfig struct {
	Driver     StorageDriver    `yaml:"driver"`
	DSN        string           `yaml:"-"` // computed DSN, populated at config load
	SQLite     SQLiteConfig     `yaml:"sqlite"`
	MySQL      MySQLConfig      `yaml:"mysql"`
	LogCleanup LogCleanupConfig `yaml:"log_cleanup"`
}

// LogCleanupConfig represents periodic log cleanup configuration.
type LogCleanupConfig struct {
	Enabled    bool   `yaml:"enabled"`              // 是否启用日志清理
	Interval   string `yaml:"interval"`             // 清理周期，如 "24h"（time.Duration 格式）
	RetainDays int    `yaml:"retain_days"`           // 保留多少天的日志
}

// LogEventContext carries structured QQ event metadata for database log entries.
// All fields are optional; unset fields should be empty strings.
type LogEventContext struct {
	EventType  string // QQ event type, e.g. "MESSAGE_CREATE"
	ChannelID  string // Source channel ID (empty for non-message events)
	GuildID    string // Guild/server ID (empty for group/C2C)
	GroupID    string // Group chat ID (empty for guild/C2C)
	AuthorID   string // Message sender ID (empty for system events)
	AuthorName string // Message sender username (empty for system events)
	MemberRole string // Group member role: "owner", "admin", "member" (empty for non-group)
	MessageID  string // QQ message ID (empty for non-message events)
}
