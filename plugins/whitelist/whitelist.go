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
)

const (
	defaultMCURL   = "http://localhost:25566"
	defaultMCToken = ""
)

// WhitelistPlugin 实现 contract.Plugin 接口（可通过 config.yaml 注入配置）。
type WhitelistPlugin struct{}

func (p *WhitelistPlugin) Name() string { return "whitelist" }

func (p *WhitelistPlugin) Init(pc *contract.PluginContext) error {
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
			return handleWhitelist(ctx, pc, mcServerURL, mcToken)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "移除白名单",
		Description: "移出 Minecraft 服务器白名单（仅群主可用）",
		Usage:       "移除白名单 <玩家名>",
		Permission:  "owner_exact",
		Handler: func(ctx contract.CommandContext) error {
			return handleRemoveWhitelist(ctx, pc, mcServerURL, mcToken)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "查询白名单",
		Description: "查询玩家白名单状态",
		Usage:       "查询白名单 @用户|<玩家名>",
		Handler: func(ctx contract.CommandContext) error {
			return handleCheckWhitelist(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// mcPlayerNameRegex 校验 MC 玩家名：3-16 位，允许字母、数字、下划线。
var mcPlayerNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)

// whitelistStorageKey 根据玩家名生成存储键。
func whitelistStorageKey(playerName string) string {
	return "whitelist:" + strings.ToLower(playerName)
}

// whitelistUserKey 根据 QQ 用户 OpenID 生成存储键，用于限制一用户一玩家名。
func whitelistUserKey(authorID string) string {
	return "whitelist:user:" + authorID
}

// handleWhitelist 是申请白名单的核心逻辑。
func handleWhitelist(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
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

	// ── 检查该 QQ 用户是否已申请过 ──
	userKey := whitelistUserKey(ctx.AuthorID())
	var existingPlayer string
	if err := pc.Storage.Get(userKey, &existingPlayer); err == nil && existingPlayer != "" {
		return ctx.ReplyMarkdown(fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
			"## 已申请过白名单\n"+
			"- **玩家名**: %s\n"+
			"- **状态**: ❌ 请勿重复申请", existingPlayer))
	}

	// ── 玩家名去重检查 ──
	key := whitelistStorageKey(playerName)
	var existing string
	if err := pc.Storage.Get(key, &existing); err == nil && existing != "" {
		var record whitelistLocalRecord
		if json.Unmarshal([]byte(existing), &record) == nil {
			md := fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
				"## 玩家名已被占用\n"+
				"- **玩家名**: %s\n"+
				"- **状态**: ❌ 该玩家名已在白名单中\n"+
				"- **申请用户**: %s\n"+
				"- **申请时间**: %s", playerName, contract.MentionUser(record.AppliedBy), formatLocalRecord(existing))
			buttons := [][]contract.MessageButton{
				{{ID: "btn_baiming", Label: "再次申请", Data: "申请白名单 ", Style: 1, ActionType: 2}},
			}
			return ctx.ReplyWithButtonRows(md, buttons)
		}
		md := fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
			"## 玩家名已被占用\n"+
			"- **玩家名**: %s\n"+
			"- **状态**: ❌ 该玩家名已在白名单中\n"+
			"- **申请时间**: %s", playerName, formatLocalRecord(existing))
		buttons := [][]contract.MessageButton{
			{{ID: "btn_baiming", Label: "再次申请", Data: "申请白名单 ", Style: 1, ActionType: 2}},
		}
		return ctx.ReplyWithButtonRows(md, buttons)
	}

	// ── 发送 POST 请求到 MC 服务器 ──
	if err := addWhitelist(mcServerURL, mcToken, playerName); err != nil {
		return ctx.Reply(fmt.Sprintf("白名单添加失败：%v", err))
	}

	// ── 写入本地数据库 ──
	record := fmt.Sprintf(`{"applied_by":"%s","group_id":"%s","time":"%s"}`,
		ctx.AuthorID(),
		ctx.GroupID(),
		time.Now().Format(time.RFC3339),
	)
	if err := pc.Storage.Set(key, record); err != nil {
		pc.Logger.Error("failed to save whitelist record", "player", playerName, "error", err)
	}
	// 记录用户 → 玩家名映射，防止重复申请
	if err := pc.Storage.Set(userKey, playerName); err != nil {
		pc.Logger.Error("failed to save user mapping", "author_id", ctx.AuthorID(), "error", err)
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
func handleRemoveWhitelist(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
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
	key := whitelistStorageKey(playerName)
	var existing string
	if err := pc.Storage.Get(key, &existing); err == nil && existing != "" {
		_ = pc.Storage.Delete(key)
	}

	// 查找并删除该玩家名对应的用户映射
	// 通过遍历所有 whitelist:user: 前缀的 key 来查找
	_ = pc.Storage.Delete("whitelist:user:" + ctx.AuthorID()) // 当前群主的映射一并清理

	pc.Logger.Info("whitelist removed",
		"player", playerName,
		"group_id", ctx.GroupID(),
		"author_id", ctx.AuthorID(),
	)
	return ctx.Reply(fmt.Sprintf("玩家名:%s 已成功移除白名单", playerName))
}

// ── 查询白名单 ──

// handleCheckWhitelist 查询玩家白名单状态。支持玩家名或 @用户 查询。
func handleCheckWhitelist(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	var playerName string

	// 情况 1：@用户 查询
	if mentions := ctx.Mentions(); len(mentions) > 0 {
		userID := mentions[0]
		userKey := whitelistUserKey(userID)
		var storedPlayer string
		if err := pc.Storage.Get(userKey, &storedPlayer); err != nil || storedPlayer == "" {
			return ctx.Reply("该用户尚未申请白名单")
		}
		playerName = storedPlayer
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

	// 先查本地记录
	key := whitelistStorageKey(playerName)
	var localRecord string
	if err := pc.Storage.Get(key, &localRecord); err == nil && localRecord != "" {
		recAppliedBy, recTime := parseLocalRecord(localRecord)
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
				"请寻找**群主**或**管理员**解决", playerName, contract.MentionUser(recAppliedBy), recTime, err))
		}
		return ctx.ReplyMarkdown(fmt.Sprintf("## 白名单查询结果\n"+
			contract.MentionUser(ctx.AuthorID())+"\n"+
			"- **玩家名**: %s\n"+
			"- **远程状态**: %s\n"+
			"- **本地记录**: ✅ 已申请\n"+
			"  - **申请用户**: %s\n"+
			"  - **申请时间**: %s\n", playerName, formatRemoteResponse(body), contract.MentionUser(recAppliedBy), formatLocalRecord(localRecord)))
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

type whitelistLocalRecord struct {
	AppliedBy string `json:"applied_by"`
	GroupID   string `json:"group_id"`
	Time      string `json:"time"`
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

func formatLocalRecord(jsonStr string) string {
	var record whitelistLocalRecord
	if err := json.Unmarshal([]byte(jsonStr), &record); err != nil {
		return jsonStr
	}
	if t, err := time.Parse(time.RFC3339, record.Time); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}
	return record.Time
}

// parseLocalRecord 从 JSON 字符串中解析出申请用户 ID 和格式化后的时间。
func parseLocalRecord(jsonStr string) (appliedBy, formattedTime string) {
	var record whitelistLocalRecord
	if err := json.Unmarshal([]byte(jsonStr), &record); err != nil {
		return "", jsonStr
	}
	t, err := time.Parse(time.RFC3339, record.Time)
	if err != nil {
		return record.AppliedBy, record.Time
	}
	return record.AppliedBy, t.Format("2006-01-02 15:04:05")
}
