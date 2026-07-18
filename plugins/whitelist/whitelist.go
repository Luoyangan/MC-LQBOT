// Package whitelist 提供 MC 白名单申请功能。
// 用户在群中发送 "/申请白名单 玩家名" 或 "申请白名单 玩家名"，
// 机器人将向 MC 服务器发送 POST 请求添加白名单，并在本地数据库去重。
package whitelist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	"gorm.io/gorm"
)

const (
	defaultMCURL   = "http://localhost:25566"
	defaultMCToken = ""
)

// WhitelistRecord 是白名单记录的数据表模型。
type WhitelistRecord struct {
	PlayerName  string    `gorm:"primaryKey;size:32"`                         // MC 玩家名（小写，主键）
	DisplayName string    `gorm:"column:display_name;size:32"`                // MC 玩家名（原始大小写）
	AppliedBy   string    `gorm:"column:applied_by;type:text;not null;index"` // 申请者 QQ 用户 ID
	GroupID     string    `gorm:"column:group_id;type:text;not null"`         // 申请时所在群
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`           // 申请时间
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`           // 更新时间
}

// WhitelistPlugin 实现 contract.Plugin 接口（可通过 config.yaml 注入配置）。
type WhitelistPlugin struct{}

func (p *WhitelistPlugin) Name() string { return "whitelist" }

func (p *WhitelistPlugin) Init(pc *contract.PluginContext) error {
	// Auto-migrate whitelist table
	db := pc.RawDB.(*gorm.DB)
	if err := db.AutoMigrate(&WhitelistRecord{}); err != nil {
		return fmt.Errorf("auto migrate whitelist table: %w", err)
	}

	mcServerURL := defaultMCURL
	mcToken := defaultMCToken
	if pc.SharedConfig != nil {
		if cfg, ok := pc.SharedConfig.(map[string]interface{}); ok {
			if url, ok := cfg["mc_server_url"].(string); ok && url != "" {
				mcServerURL = url
			}
			if token, ok := cfg["mc_token"].(string); ok && token != "" {
				mcToken = token
			}
		}
	}

	pc.Logger.Info("whitelist plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	pc.Commands.Register(contract.Command{
		Name:        "申请白名单",
		Description: "申请 Minecraft 服务器白名单",
		Usage:       "申请白名单 <玩家名>",
		Handler: func(ctx contract.CommandContext) error {
			return handleWhitelist(ctx, pc, db, mcServerURL, mcToken)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "移除白名单",
		Description: "移出 Minecraft 服务器白名单（仅群主可用）",
		Usage:       "移除白名单 <玩家名>",
		Permission:  "owner_exact",
		Handler: func(ctx contract.CommandContext) error {
			return handleRemoveWhitelist(ctx, pc, db, mcServerURL, mcToken)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "查询白名单",
		Description: "查询玩家白名单状态",
		Usage:       "查询白名单 @用户|<玩家名>",
		Handler: func(ctx contract.CommandContext) error {
			return handleCheckWhitelist(ctx, pc, db, mcServerURL, mcToken)
		},
	})

	return nil
}

// mcPlayerNameRegex 校验 MC 玩家名：3-16 位，允许字母、数字、下划线。
var mcPlayerNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)

// handleWhitelist 是申请白名单的核心逻辑。
func handleWhitelist(ctx contract.CommandContext, pc *contract.PluginContext, db *gorm.DB, mcServerURL, mcToken string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())
	// 只允许群聊使用
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	if ctx.ArgCount() == 0 {
		md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
			"## 申请白名单用法\n" +
			"- 申请白名单 <玩家名>\n" +
			"- 示例：申请白名单 Steve\n" +
			contract.CmdInput("我是大傻逼", "点我可直接申请", false) + "\n\n"
		buttons := [][]contract.MessageButton{
			{{ID: "btn_baiming", Label: "申请白名单", Data: "申请白名单 ", Style: 1, ActionType: 2}},
		}
		return ctx.ReplyWithButtonRows(md, buttons)
	}

	playerName := strings.TrimSpace(ctx.Arg(0))
	if playerName == "" {
		return ctx.Reply("玩家名不能为空")
	}

	// ── 玩家名校验 ──
	if !mcPlayerNameRegex.MatchString(playerName) {
		md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
			"## 玩家名不合法\n" +
			"1. 字符长度：3 ~ 16 位\n" +
			"2. 允许字符：大小写英文字母、数字、下划线（_）\n" +
			"3. 禁止：中文、特殊符号、空格等"
		buttons := [][]contract.MessageButton{
			{{ID: "btn_baiming", Label: "申请白名单", Data: "申请白名单 ", Style: 1, ActionType: 2}},
		}
		return ctx.ReplyWithButtonRows(md, buttons)
	}

	lowerName := strings.ToLower(playerName)

	// ── 检查该 QQ 用户是否已申请过 ──
	var existingUser WhitelistRecord
	if err := db.Where("applied_by = ?", ctx.AuthorID()).First(&existingUser).Error; err == nil {
		return ctx.ReplyMarkdown(fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
			"## 已申请过白名单\n"+
			"- **玩家名**: %s\n"+
			"- **状态**: ❌ 请勿重复申请", displayName(existingUser)))
	}

	// ── 玩家名去重检查 ──
	var existing WhitelistRecord
	if err := db.Where("player_name = ?", lowerName).First(&existing).Error; err == nil {
		md := fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
			"## 玩家名已被占用\n"+
			"- **玩家名**: %s\n"+
			"- **状态**: ❌ 该玩家名已在白名单中\n"+
			"- **申请用户**: %s\n"+
			"- **申请时间**: %s",
			playerName, contract.MentionUser(existing.AppliedBy), existing.CreatedAt.Format("2006-01-02 15:04:05"))
		buttons := [][]contract.MessageButton{
			{{ID: "btn_baiming", Label: "再次申请", Data: "申请白名单 ", Style: 1, ActionType: 2}},
		}
		return ctx.ReplyWithButtonRows(md, buttons)
	}

	// ── 发送 POST 请求到 MC 服务器 ──
	if err := addWhitelist(mcServerURL, mcToken, playerName); err != nil {
		return ctx.Reply(fmt.Sprintf("白名单添加失败：%v", err))
	}

	// ── 写入数据库 ──
	record := WhitelistRecord{
		PlayerName:  lowerName,
		DisplayName: playerName,
		AppliedBy:   ctx.AuthorID(),
		GroupID:     ctx.GroupID(),
	}
	if err := db.Create(&record).Error; err != nil {
		pc.Logger.Error("failed to save whitelist record", "player", playerName, "error", err)
		return ctx.Reply("白名单添加成功，但本地记录保存失败，请联系管理员")
	}

	pc.Logger.Info("whitelist added",
		"player", playerName,
		"group_id", ctx.GroupID(),
		"author_id", ctx.AuthorID(),
	)
	am := fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
		"## 申请白名单成功\n"+
		"- **玩家名**: %s\n"+
		"- **用户ID**: %s\n"+
		"- **申请时间**: %s\n\n",
		playerName,
		ctx.AuthorID(),
		time.Now().Format("2006-01-02 15:04:05"),
	)
	return ctx.ReplyMarkdown(am)
}

// ── 移除白名单 ──

// handleRemoveWhitelist 处理移除白名单命令，仅群主可执行。
func handleRemoveWhitelist(ctx contract.CommandContext, pc *contract.PluginContext, db *gorm.DB, mcServerURL, mcToken string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	if ctx.ArgCount() == 0 {
		md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
			"## 移除白名单\n" +
			"- **用法**：移除白名单 <玩家名>\n" +
			"- **示例**：移除白名单 Steve\n\n"
		return ctx.ReplyMarkdown(md)
	}

	playerName := strings.TrimSpace(ctx.Arg(0))
	if playerName == "" {
		return ctx.Reply("玩家名不能为空")
	}

	if !mcPlayerNameRegex.MatchString(playerName) {
		return ctx.Reply("玩家名不合法")
	}

	// 发送移除请求到 MC 服务器
	if err := whitelistRequest("remove", mcServerURL, mcToken, playerName); err != nil {
		return ctx.Reply(fmt.Sprintf("白名单移除失败：%v", err))
	}

	// 清理本地数据库记录
	lowerName := strings.ToLower(playerName)
	if err := db.Where("player_name = ?", lowerName).Delete(&WhitelistRecord{}).Error; err != nil {
		pc.Logger.Error("failed to delete whitelist record", "player", playerName, "error", err)
	}

	pc.Logger.Info("whitelist removed",
		"player", playerName,
		"group_id", ctx.GroupID(),
		"author_id", ctx.AuthorID(),
	)
	return ctx.Reply(fmt.Sprintf("玩家名:%s 已成功移除白名单", playerName))
}

// ── 查询白名单 ──

// handleCheckWhitelist 查询玩家白名单状态。支持玩家名或 @用户 查询。
func handleCheckWhitelist(ctx contract.CommandContext, pc *contract.PluginContext, db *gorm.DB, mcServerURL, mcToken string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	var playerName string

	// 情况 1：@用户 查询
	if mentions := ctx.Mentions(); len(mentions) > 0 {
		userID := mentions[0]
		var userRecord WhitelistRecord
		if err := db.Where("applied_by = ?", userID).First(&userRecord).Error; err != nil {
			return ctx.Reply("该用户尚未申请白名单")
		}
		playerName = displayName(userRecord)
	}

	// 情况 2：玩家名查询
	if playerName == "" {
		if ctx.ArgCount() == 0 {
			md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
				"## 查询白名单\n" +
				"- **用法**：查询白名单 <玩家名>\n" +
				"- **用法**：查询白名单 @用户\n" +
				"- **示例**：查询白名单 Steve\n" +
				"\n" +
				"**提示**：您可以使用 @用户 来查询该用户的白名单状态。\n"
			return ctx.ReplyMarkdown(md)
		}
		playerName = strings.TrimSpace(ctx.Arg(0))
	}

	if playerName == "" {
		return ctx.Reply("玩家名不能为空")
	}

	if !mcPlayerNameRegex.MatchString(playerName) {
		return ctx.Reply("玩家名不合法")
	}

	lowerName := strings.ToLower(playerName)

	// 先查本地记录
	var record WhitelistRecord
	if err := db.Where("player_name = ?", lowerName).First(&record).Error; err == nil {
		body, err := checkWhitelist(mcServerURL, mcToken, playerName)
		if err != nil {
			return ctx.ReplyMarkdown(fmt.Sprintf("## 查询失败\n"+
				contract.MentionUser(ctx.AuthorID())+"\n"+
				"- **玩家名**: %s\n"+
				"- **本地记录**: ✅ 已申请\n"+
				"  - **申请用户**: %s\n"+
				"  - **申请时间**: %s\n"+
				"- **远程状态**: ❌ 查询失败\n"+
				"- **原因**: %v\n"+
				"请寻找**群主**或**管理员**解决",
				playerName, contract.MentionUser(record.AppliedBy), record.CreatedAt.Format("2006-01-02 15:04:05"), err))
		}
		return ctx.ReplyMarkdown(fmt.Sprintf("## 白名单查询结果\n"+
			contract.MentionUser(ctx.AuthorID())+"\n"+
			"- **玩家名**: %s\n"+
			"- **远程状态**: %s\n"+
			"- **本地记录**: ✅ 已申请\n"+
			"  - **申请用户**: %s\n"+
			"  - **申请时间**: %s\n",
			playerName, formatRemoteResponse(body), contract.MentionUser(record.AppliedBy), record.CreatedAt.Format("2006-01-02 15:04:05")))
	}

	body, err := checkWhitelist(mcServerURL, mcToken, playerName)
	if err != nil {
		return ctx.ReplyMarkdown(fmt.Sprintf("## 查询失败\n"+
			contract.MentionUser(ctx.AuthorID())+"\n"+
			"- **玩家名**: %s\n"+
			"- **本地记录**: ❌ 无申请记录\n"+
			"- **远程状态**: ❌ 查询失败\n"+
			"- **原因**: %v\n"+
			"请寻找**群主**或**管理员**解决", playerName, err))
	}
	return ctx.ReplyMarkdown(fmt.Sprintf("## 白名单查询结果\n"+
		contract.MentionUser(ctx.AuthorID())+"\n"+
		"- **玩家名**: %s\n"+
		"- **远程状态**: %s\n"+
		"- **本地记录**: ❌ 无申请记录", playerName, formatRemoteResponse(body)))
}

// ── MC 服务器 API 请求 ──

type whitelistRequestBody struct {
	PlayerName string `json:"playerName"`
	Action     string `json:"action"`
}

type whitelistRemoteResponse struct {
	Status  bool   `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		IsWhitelisted bool `json:"isWhitelisted"`
	} `json:"data"`
}

// whitelistRequest 向 MC 服务器发送 POST 请求执行白名单操作。
func whitelistRequest(action, serverURL, token, playerName string) error {
	body := whitelistRequestBody{
		PlayerName: playerName,
		Action:     action,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+"/api/whitelist", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 MC 服务器失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("MC 服务器返回错误状态码: %d", resp.StatusCode)
	}

	return nil
}

// addWhitelist 便捷函数，用于添加白名单。
func addWhitelist(serverURL, token, playerName string) error {
	return whitelistRequest("add", serverURL, token, playerName)
}

// checkWhitelist 向 MC 服务器发送 POST 请求查询白名单，返回响应体。
func checkWhitelist(serverURL, token, playerName string) (string, error) {
	body := whitelistRequestBody{
		PlayerName: playerName,
		Action:     "check",
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+"/api/whitelist", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 MC 服务器失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("MC 服务器返回错误状态码: %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// displayName 返回玩家名的原始大小写，旧记录无 DisplayName 时回退到 PlayerName。
func displayName(r WhitelistRecord) string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	return r.PlayerName
}

func formatRemoteResponse(jsonStr string) string {
	var resp whitelistRemoteResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return jsonStr
	}
	if resp.Status && resp.Data.IsWhitelisted {
		return "✅ 在白名单中"
	}
	if resp.Status && !resp.Data.IsWhitelisted {
		return "❌ 不在白名单中"
	}
	return fmt.Sprintf("❌ %s", resp.Message)
}
