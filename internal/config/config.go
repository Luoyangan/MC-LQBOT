// Package config handles configuration loading and management.
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Luoyangan/LQBOT/internal/types"
	"gopkg.in/yaml.v3"
)

// Init ensures the config file exists, creating it from the example template if missing.
// Returns the path to use for loading.
func Init(path string) string {
	if fileExists(path) {
		return path
	}

	// Try the example template
	examplePath := examplePathFrom(path)
	if examplePath != "" && fileExists(examplePath) {
		log.Printf("[config] %s not found, copying from %s", path, examplePath)
		if err := copyFile(examplePath, path); err != nil {
			log.Printf("[config] failed to copy config: %v", err)
		} else {
			return path
		}
	}

	// No config at all — create a minimal one
	log.Printf("[config] no config files found, creating minimal %s", path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
		minimal := `# LQBOT 配置文件示例
app_id: "your_app_id_here"       # QQ 开放平台申请的 AppID
app_secret: "your_app_secret_here" # QQ 开放平台申请的 AppSecret
sandbox: false                    # true = 沙箱模式, false = 生产模式
intents:
  # 注意: 公域机器人仅支持 AT_MESSAGES + GROUP_AND_C2C_EVENT + INTERACTION + MESSAGE_AUDIT
  #       私域机器人可额外使用 GUILDS/GUILD_MEMBERS/GUILD_MESSAGES 等
  - AT_MESSAGES                  # 频道 @机器人消息（公/私域）
  #- GUILD_MESSAGES               # 频道全部消息（仅私域）
  - GROUP_AND_C2C_EVENT          # 群聊和私聊（需申请权限）
  - INTERACTION                  # 按钮/选择框交互事件
  #- GUILD_MESSAGE_REACTIONS     # 消息表情表态事件（仅私域）
  #- DIRECT_MESSAGE              # 频道私信事件（仅私域）
  #- FORUMS_EVENT                # 论坛/帖子事件（仅私域）
  #- AUDIO_ACTION                # 音频事件（仅私域）
  #- MESSAGE_AUDIT               # 消息审核事件（公/私域）
access_type: websocket           # websocket | webhook
log_level: info                  # 控制台日志级别: trace/debug/info/warn/error
log_level_db: info               # 数据库日志级别: trace/debug/info/warn/error（空=不写入DB）
log_db_exclude:                  # 数据库日志排除关键词（消息包含任一关键词则不写入DB）
  - "Heartbeat"                  # 过滤心跳日志，保留事件消息

webhook:                         # webhook 模式配置（access_type: webhook 时生效）
  port: 8080                     # HTTP 监听端口
  path: /webhook                 # 回调路径

storage:                         # 数据库配置
  driver: sqlite                 # 数据库驱动，支持 sqlite / mysql
  sqlite:
    dsn: "data/lqbot.db"         # SQLite 数据库文件路径
  mysql:
    user: "root"                 # MySQL 用户名
    pass: "123456"               # MySQL 密码
    host: "127.0.0.1"            # MySQL 主机
    port: 3306                   # MySQL 端口
    db: "lqbot"                  # MySQL 数据库名
  log_cleanup:                   # 日志清理配置
    enabled: true                # 是否启用日志清理
    interval: "24h"              # 清理周期（time.Duration 格式）
    retain_days: 7               # 保留多少天的日志

http:                            # 内嵌 HTTP 服务配置（可选）
  enabled: false                 # true = 启用内嵌 HTTP 服务
  port: 80                       # HTTP 监听端口（默认 80）
  admin: true                    # true = 启用网页管理界面
  #cert_file: "ssl/cert.pem"     # SSL 证书文件路径（设置后启用 HTTPS）
  #key_file: "ssl/key.pem"       # SSL 私钥文件路径

# 指令权限映射（可选）
# 可用级别: owner（群主）, admin（管理员）, member（群成员）, public（所有人）
# 不在列表中的指令默认 public（无限制）
permissions:
  # delete: "admin"             # /delete 指令仅管理员可用

# 插件独立配置（可选，由各插件自行定义和解析）
# plugins:
#   hello:
#     welcome_message: "欢迎使用 LQBOT"
`
		_ = os.WriteFile(path, []byte(minimal), 0644)
	}

	return path
}

// Load reads and parses a YAML configuration file.
// Returns a validated Config struct. Missing optional fields get defaults.
func Load(path string) (*types.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &types.Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	applyDefaults(cfg)
	warnPlaceholders(cfg)

	return cfg, nil
}

// applyDefaults fills in default values for optional fields.
func applyDefaults(cfg *types.Config) {
	if cfg.AccessType == "" {
		cfg.AccessType = types.AccessWebSocket
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = types.LogLevelInfo
	}
	if cfg.Storage.Driver == "" {
		cfg.Storage.Driver = types.StorageSQLite
	}
	// Build DSN from sub-config
	switch cfg.Storage.Driver {
	case types.StorageMySQL:
		if cfg.Storage.MySQL.Host == "" {
			cfg.Storage.MySQL.Host = "127.0.0.1"
		}
		if cfg.Storage.MySQL.Port == 0 {
			cfg.Storage.MySQL.Port = 3306
		}
		if cfg.Storage.MySQL.User == "" {
			cfg.Storage.MySQL.User = "root"
		}
		if cfg.Storage.MySQL.DB == "" {
			cfg.Storage.MySQL.DB = "lqbot"
		}
		cfg.Storage.DSN = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True",
			cfg.Storage.MySQL.User, cfg.Storage.MySQL.Pass,
			cfg.Storage.MySQL.Host, cfg.Storage.MySQL.Port, cfg.Storage.MySQL.DB)
	default: // sqlite
		if cfg.Storage.SQLite.DSN == "" {
			cfg.Storage.SQLite.DSN = "data/lqbot.db"
		}
		cfg.Storage.DSN = cfg.Storage.SQLite.DSN
	}
	if len(cfg.Intents) == 0 {
		cfg.Intents = []string{"GUILD_MESSAGES"}
	}
	// Log cleanup defaults
	if cfg.Storage.LogCleanup.Interval == "" {
		cfg.Storage.LogCleanup.Interval = "24h"
	}
	if cfg.Storage.LogCleanup.RetainDays == 0 {
		cfg.Storage.LogCleanup.RetainDays = 7
	}
	// HTTP server defaults
	if cfg.HTTP.Port == 0 {
		cfg.HTTP.Port = 80
	}
}

// warnPlaceholders logs warnings for placeholder/empty values but doesn't block startup.
func warnPlaceholders(cfg *types.Config) {
	if cfg.AppID == "" || cfg.AppID == "your_app_id_here" {
		log.Println("[config] WARNING: app_id is not set. Edit configs/config.yaml to connect to QQ.")
	}
	if cfg.AppSecret == "" || cfg.AppSecret == "your_app_secret_here" {
		log.Println("[config] WARNING: app_secret is not set. Edit configs/config.yaml to connect to QQ.")
	}
}

// --- helpers ---

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func examplePathFrom(configPath string) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)

	candidates := []string{
		filepath.Join(dir, "config.example.yaml"),
		filepath.Join(dir, "config.sample.yaml"),
		filepath.Join(dir, "example.yaml"),
	}

	// Try inserting .example before extension
	ext := filepath.Ext(base)
	if ext != "" {
		name := base[:len(base)-len(ext)]
		candidates = append([]string{filepath.Join(dir, name+".example"+ext)}, candidates...)
	}

	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
