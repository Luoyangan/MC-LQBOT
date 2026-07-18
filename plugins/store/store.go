// Package store 提供商店功能。
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	"github.com/Luoyangan/LQBOT/internal/storage"
	"gorm.io/gorm"
)

const (
	defaultMCURL   = "http://localhost:25566"
	defaultMCToken = ""
)

// ── 商品定义 ──

// product 定义商店商品。
type product struct {
	Category string // 分类：权限、物品
	Name     string // 商品名
	Label    string // 显示名称
	GoldCost int    // 金币价格
	ExpCost  int    // 经验值价格
	ExpLCost int    // 经验等级价格
}

// products 商品列表。
var products = []product{
	{Category: "权限", Name: "飞行", Label: "一次性飞行", GoldCost: 500, ExpCost: 200, ExpLCost: 1},
}

// StorePlugin 实现 contract.Plugin 接口。
type StorePlugin struct{}

func (p *StorePlugin) Name() string { return "store" }

func (p *StorePlugin) Init(pc *contract.PluginContext) error {
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

	pc.Logger.Info("store plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	// ── 商店主菜单 ──
	pc.Commands.Register(contract.Command{
		Name:        "商店",
		Description: "打开商店菜单",
		Handler: func(ctx contract.CommandContext) error {
			return handleStoreMenu(ctx, pc)
		},
	})

	// ── 权限商店 ──
	pc.Commands.Register(contract.Command{
		Name:        "权限商店",
		Description: "打开权限商店",
		Handler: func(ctx contract.CommandContext) error {
			return handlePermShop(ctx, pc)
		},
	})

	// ── 物品商店 ──
	pc.Commands.Register(contract.Command{
		Name:        "物品商店",
		Description: "打开物品商店",
		Handler: func(ctx contract.CommandContext) error {
			return handleItemShop(ctx, pc)
		},
	})

	// ── 购买（展示确认） ──
	pc.Commands.Register(contract.Command{
		Name:        "购买",
		Description: "购买商品",
		Usage:       "购买 <分类> <商品名>",
		Handler: func(ctx contract.CommandContext) error {
			return handleBuy(ctx, pc)
		},
	})

	// ── 确认购买（执行扣款+调用MC API） ──
	pc.Commands.Register(contract.Command{
		Name:        "确认购买",
		Description: "确认购买商品",
		Usage:       "确认购买 <分类> <商品名>",
		Handler: func(ctx contract.CommandContext) error {
			return handleConfirmBuy(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// ── 数据库辅助 ──

// bankAccount 与 bank 插件共用 bank_accounts 表。
type bankAccount struct {
	AuthorID string `gorm:"primaryKey;size:512"`
	Gold     int    `gorm:"default:0;not null"`
	Exp      int    `gorm:"default:0;not null"`
	ExpLevel int    `gorm:"default:0;not null"`
}

func (bankAccount) TableName() string { return "bank_accounts" }

func getDB(pc *contract.PluginContext) *gorm.DB {
	if store, ok := pc.Storage.(*storage.Storage); ok {
		return store.DB()
	}
	return nil
}

func getAccount(db *gorm.DB, authorID string) bankAccount {
	var acc bankAccount
	if err := db.First(&acc, "author_id = ?", authorID).Error; err != nil {
		return bankAccount{AuthorID: authorID}
	}
	return acc
}

func saveAccount(db *gorm.DB, acc *bankAccount) {
	db.Save(acc)
}

func getPlayerName(db *gorm.DB, authorID string) (string, error) {
	var record struct {
		PlayerName  string `gorm:"column:player_name"`
		DisplayName string `gorm:"column:display_name"`
	}
	if err := db.Table("whitelist_records").
		Where("applied_by = ?", authorID).
		First(&record).Error; err != nil {
		return "", fmt.Errorf("未找到绑定的玩家名，请先使用「申请白名单」绑定")
	}
	if record.DisplayName != "" {
		return record.DisplayName, nil
	}
	return record.PlayerName, nil
}

// ── HTTP 辅助 ──

func postRequest(serverURL, token, path string, body interface{}) (string, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+path, bytes.NewReader(jsonBody))
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
		return "", fmt.Errorf("请求服务器失败: %w", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// ── 商品匹配 ──

// findProduct 根据分类和商品名查找商品。
func findProduct(category, label string) *product {
	for _, p := range products {
		if strings.EqualFold(p.Category, category) && strings.EqualFold(p.Label, label) {
			return &p
		}
	}
	return nil
}

// priceParts 返回价格描述片段列表（只含非零项）。
func priceParts(p *product) []string {
	var parts []string
	if p.GoldCost > 0 {
		parts = append(parts, fmt.Sprintf("%d金币", p.GoldCost))
	}
	if p.ExpCost > 0 {
		parts = append(parts, fmt.Sprintf("%d经验值", p.ExpCost))
	}
	if p.ExpLCost > 0 {
		parts = append(parts, fmt.Sprintf("%d经验等级", p.ExpLCost))
	}
	return parts
}

// ── 处理器 ──

func handleStoreMenu(ctx contract.CommandContext, pc *contract.PluginContext) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640",
		pc.QQAPI.AppID(), ctx.AuthorID())

	md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
		"## 商店菜单\n" +
		"- " + contract.CmdInput("权限商店", "权限商店", false) + "\n" +
		"- " + contract.CmdInput("物品商店", "物品商店", false)

	return ctx.ReplyMarkdown(md)
}

func handlePermShop(ctx contract.CommandContext, pc *contract.PluginContext) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640",
		pc.QQAPI.AppID(), ctx.AuthorID())

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(ctx.AuthorID())))
	buf.WriteString("## 权限商店\n\n")

	for _, p := range products {
		if p.Category != "权限" {
			continue
		}
		buf.WriteString(fmt.Sprintf("- **%s**\n", p.Label))
		buf.WriteString(fmt.Sprintf("- - **价格**：%s ",
			strings.Join(priceParts(&p), " + ")))
		buf.WriteString(contract.CmdInput(fmt.Sprintf("购买 %s %s", p.Category, p.Label), "购买", false))
		buf.WriteString("\n\n")
	}

	return ctx.ReplyMarkdown(buf.String())
}

func handleItemShop(ctx contract.CommandContext, pc *contract.PluginContext) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640",
		pc.QQAPI.AppID(), ctx.AuthorID())

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## 物品商店\n"+
		"- 暂未开发", avatarURL, contract.MentionUser(ctx.AuthorID()))

	return ctx.ReplyMarkdown(md)
}

// handleBuy 展示商品价格确认信息。
func handleBuy(ctx contract.CommandContext, pc *contract.PluginContext) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640",
		pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.ArgCount() < 2 {
		return ctx.Reply("用法：购买 <分类> <商品名>，例如：购买 权限 飞行")
	}

	category := strings.TrimSpace(ctx.Arg(0))
	label := strings.TrimSpace(ctx.Arg(1))

	p := findProduct(category, label)
	if p == nil {
		return ctx.Reply(fmt.Sprintf("未找到商品「%s %s」", category, label))
	}

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(ctx.AuthorID())) +
		fmt.Sprintf("## 确认购买\n\n") +
		fmt.Sprintf("- **%s**\n", p.Label) +
		fmt.Sprintf("- - **价格**：%s\n\n", strings.Join(priceParts(p), " + "))
	buttons := [][]contract.MessageButton{
		{{ID: "btn_buy", Label: "确认购买", Data: fmt.Sprintf("确认购买 %s %s", p.Category, p.Label), Style: 1, ActionType: 2}},
	}

	return ctx.ReplyWithButtonRows(md, buttons)
}

// handleConfirmBuy 执行扣款并调用 MC API。
func handleConfirmBuy(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640",
		pc.QQAPI.AppID(), ctx.AuthorID())
	authorID := ctx.AuthorID()

	if ctx.ArgCount() < 2 {
		return ctx.Reply("用法：确认购买 <分类> <商品名>")
	}

	category := strings.TrimSpace(ctx.Arg(0))
	label := strings.TrimSpace(ctx.Arg(1))

	p := findProduct(category, label)
	if p == nil {
		return ctx.Reply(fmt.Sprintf("未找到商品「%s %s」", category, label))
	}

	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	// 获取用户绑定的玩家名
	playerName, err := getPlayerName(db, authorID)
	if err != nil {
		return ctx.Reply(err.Error())
	}

	// 获取账户余额
	acc := getAccount(db, authorID)

	// 检查余额（只检查非零价格项）
	var shortages []string
	if p.GoldCost > 0 && acc.Gold < p.GoldCost {
		shortages = append(shortages, fmt.Sprintf("金币不足（需要 %d，当前 %d）", p.GoldCost, acc.Gold))
	}
	if p.ExpCost > 0 && acc.Exp < p.ExpCost {
		shortages = append(shortages, fmt.Sprintf("经验值不足（需要 %d，当前 %d）", p.ExpCost, acc.Exp))
	}
	if p.ExpLCost > 0 && acc.ExpLevel < p.ExpLCost {
		shortages = append(shortages, fmt.Sprintf("经验等级不足（需要 %d，当前 %d）", p.ExpLCost, acc.ExpLevel))
	}
	if len(shortages) > 0 {
		md := fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(authorID)) +
			"## ❌ 余额不足\n" +
			strings.Join(shortages, "\n")
		return ctx.ReplyMarkdown(md)
	}

	// 根据商品类型执行购买动作（先执行，成功才扣款）
	switch {
	case strings.EqualFold(p.Category, "权限") && strings.EqualFold(p.Label, "一次性飞行"):
		// 调用 MC 飞行 API
		flyResp, err := postRequest(mcServerURL, mcToken, "/api/flight", map[string]interface{}{
			"playerName": playerName,
			"action":     "enable",
		})
		if err != nil {
			return ctx.ReplyMarkdown(
				fmt.Sprintf("![img #30px #30px](%s) | %s\n## ❌ 操作失败\n启用飞行请求失败：%v",
					avatarURL, contract.MentionUser(authorID), err))
		}

		// 解析响应
		var fResp struct {
			Status  bool   `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(flyResp), &fResp); err != nil {
			return ctx.ReplyMarkdown(
				fmt.Sprintf("![img #30px #30px](%s) | %s\n## ❌ 操作失败\n解析服务器响应失败",
					avatarURL, contract.MentionUser(authorID)))
		}

		if !fResp.Status {
			if fResp.Code == 4301 {
				return ctx.ReplyMarkdown(
					fmt.Sprintf("![img #30px #30px](%s) | %s\n## ❌ 购买失败\n玩家 `%s` 不在线，无法开启飞行",
						avatarURL, contract.MentionUser(authorID), playerName))
			}
			return ctx.ReplyMarkdown(
				fmt.Sprintf("![img #30px #30px](%s) | %s\n## ❌ 操作失败\nMC 服务端返回：%s",
					avatarURL, contract.MentionUser(authorID), fResp.Message))
		}

		// 动作成功，执行扣款
		var deducted []string
		if p.GoldCost > 0 {
			acc.Gold -= p.GoldCost
			deducted = append(deducted, fmt.Sprintf("金币：-%d（剩余 %d）", p.GoldCost, acc.Gold))
		}
		if p.ExpCost > 0 {
			acc.Exp -= p.ExpCost
			deducted = append(deducted, fmt.Sprintf("经验值：-%d（剩余 %d）", p.ExpCost, acc.Exp))
		}
		if p.ExpLCost > 0 {
			acc.ExpLevel -= p.ExpLCost
			deducted = append(deducted, fmt.Sprintf("经验等级：-%d（剩余 %d）", p.ExpLCost, acc.ExpLevel))
		}
		saveAccount(db, &acc)

		md := fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(authorID)) +
			fmt.Sprintf("## ✅ 购买成功\n") +
			fmt.Sprintf("- **商品**：%s\n", p.Label) +
			fmt.Sprintf("- **玩家**：`%s`\n", playerName) +
			strings.Join(deducted, "\n") +
			"\n✅ 飞行已开启"

		return ctx.ReplyMarkdown(md)

	default:
		return ctx.Reply("暂不支持该商品")
	}
}
