package bank

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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

// BankAccount 银行账户表（GORM 模型）
type BankAccount struct {
	AuthorID string `gorm:"primaryKey;size:512"`
	Gold     int    `gorm:"default:0;not null"`
	Exp      int    `gorm:"default:0;not null"`
	ExpLevel int    `gorm:"default:0;not null"`
}

// BankPlugin 实现 contract.Plugin 接口。
type BankPlugin struct{}

func (p *BankPlugin) Name() string { return "bank" }

func (p *BankPlugin) Init(pc *contract.PluginContext) error {
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

	// Auto-migrate bank account table
	if db := getDB(pc); db != nil {
		if err := db.AutoMigrate(&BankAccount{}); err != nil {
			pc.Logger.Error("failed to migrate bank account table", "error", err)
		}
	}

	pc.Logger.Info("bank plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	pc.Commands.Register(contract.Command{
		Name:        "银行",
		Description: "查询银行信息（金币和经验值）",
		Usage:       "银行",
		Handler: func(ctx contract.CommandContext) error {
			return handleBank(ctx, pc)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "提现",
		Description: "提取金币或经验值到游戏",
		Usage:       "提现 <金币|经验值> <金额>",
		Handler: func(ctx contract.CommandContext) error {
			return handleWithdraw(ctx, pc, mcServerURL, mcToken)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "give",
		Aliases:     []string{"给予"},
		Description: "给予金币或经验值（仅群主可用）",
		Usage:       "给予 <金币|经验值> <金额> [@用户]",
		Permission:  "owner_exact",
		Handler: func(ctx contract.CommandContext) error {
			return handleGive(ctx, pc, mcServerURL, mcToken)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "deduct",
		Aliases:     []string{"扣除"},
		Description: "扣除金币或经验值（仅群主可用）",
		Usage:       "扣除 <金币|经验值> <金额> [@用户]",
		Permission:  "owner_exact",
		Handler: func(ctx contract.CommandContext) error {
			return handleDeduct(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// ── API 响应结构 ──

type simpleResponse struct {
	Status  bool        `json:"status"`
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// ── GORM 工具 ──

// getDB 从 PluginContext 中获取 GORM DB 实例。
func getDB(pc *contract.PluginContext) *gorm.DB {
	if store, ok := pc.Storage.(*storage.Storage); ok {
		return store.DB()
	}
	return nil
}

// getAccount 获取用户银行账户，不存在则返回默认零值。
func getAccount(db *gorm.DB, authorID string) BankAccount {
	var acc BankAccount
	if err := db.First(&acc, "author_id = ?", authorID).Error; err != nil {
		return BankAccount{AuthorID: authorID}
	}
	return acc
}

// saveAccount 保存银行账户。
func saveAccount(db *gorm.DB, acc *BankAccount) {
	db.Save(acc)
}

// ── 核心命令处理 ──

// getPlayerName 从 whitelist_records 表中查询 QQ 用户绑定的 MC 玩家名（优先用原始大小写）。
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
	// 优先返回原始大小写，旧记录无 display_name 时回退到 player_name
	if record.DisplayName != "" {
		return record.DisplayName, nil
	}
	return record.PlayerName, nil
}

// handleBank 查询玩家的本地金币和经验值，以 Markdown 展示银行概览。
func handleBank(ctx contract.CommandContext, pc *contract.PluginContext) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	if _, err := getPlayerName(db, ctx.AuthorID()); err != nil {
		return ctx.Reply(err.Error())
	}

	acc := getAccount(db, ctx.AuthorID())

	var md strings.Builder
	md.WriteString(fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(ctx.AuthorID())))
	md.WriteString("## 银行\n")
	md.WriteString(fmt.Sprintf("- 💰 **金币**：%d %s\n",
		acc.Gold,
		contract.CmdInput("提现 金币", "提现", false)))
	md.WriteString(fmt.Sprintf("- ⭐ **经验值**：%d %s\n",
		acc.Exp,
		contract.CmdInput("提现 经验值", "提现", false)))
	md.WriteString(fmt.Sprintf("- 🏆 **经验等级**：%d %s\n",
		acc.ExpLevel,
		contract.CmdInput("提现 经验等级", "提现", false)))

	return ctx.ReplyMarkdown(md.String())
}

// ── 提现 ──

// withdrawType 提现类型
type withdrawType int

const (
	withdrawGold withdrawType = iota
	withdrawExp
	withdrawExpLevel
)

// handleWithdraw 处理提现命令：从本地提取金币或经验值到游戏中。
// 用法：提现 <金币|经验值> <金额>  或  提现 <金额>（默认为金币）
func handleWithdraw(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	// 解析参数
	var wt withdrawType
	var amount int
	var err error

	switch ctx.ArgCount() {
	case 0:
		md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
			"## 提现用法\n" +
			"- 提现 金币 <金额>\n" +
			"- 提现 经验值 <金额>\n" +
			"- 提现 经验等级 <数量>\n" +
			"- 示例：提现 金币 100\n" +
			"- 示例：提现 经验等级 1"
		return ctx.ReplyMarkdown(md)

	case 1:
		wt = withdrawGold
		amount, err = strconv.Atoi(strings.TrimSpace(ctx.Arg(0)))
		if err != nil || amount <= 0 {
			return ctx.Reply("金额必须是正整数")
		}

	case 2:
		typeStr := strings.TrimSpace(ctx.Arg(0))
		switch typeStr {
		case "金币":
			wt = withdrawGold
		case "经验值":
			wt = withdrawExp
		case "经验等级":
			wt = withdrawExpLevel
		default:
			return ctx.Reply(fmt.Sprintf("未知类型「%s」，请使用「金币」「经验值」或「经验等级」", typeStr))
		}
		amount, err = strconv.Atoi(strings.TrimSpace(ctx.Arg(1)))
		if err != nil || amount <= 0 {
			return ctx.Reply("金额必须是正整数")
		}

	default:
		return ctx.Reply("参数过多，用法：提现 <金币|经验值> <金额>")
	}

	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	playerName, err := getPlayerName(db, ctx.AuthorID())
	if err != nil {
		return ctx.Reply(err.Error())
	}

	switch wt {
	case withdrawGold:
		return withdrawGoldToGame(ctx, db, avatarURL, serverURL, token, playerName, ctx.AuthorID(), amount)
	case withdrawExp:
		return withdrawExpToGame(ctx, db, avatarURL, serverURL, token, playerName, ctx.AuthorID(), amount)
	case withdrawExpLevel:
		return withdrawExpLevelToGame(ctx, db, avatarURL, serverURL, token, playerName, ctx.AuthorID(), amount)
	}
	return nil
}

// withdrawGoldToGame 从本地扣减金币，调用游戏 API 给玩家增加金币。
func withdrawGoldToGame(ctx contract.CommandContext, db *gorm.DB, avatarURL, serverURL, token, playerName, authorID string, amount int) error {
	acc := getAccount(db, authorID)
	if amount > acc.Gold {
		return ctx.Reply(fmt.Sprintf("金币不足！当前：%d，需要：%d", acc.Gold, amount))
	}

	// 调用游戏 API give 金币
	rb, err := postRequest(serverURL, token, "/api/currency", map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     "give",
	})
	if err != nil {
		return ctx.Reply(fmt.Sprintf("提现金币失败：%v", err))
	}
	var rResp simpleResponse
	if err := json.Unmarshal([]byte(rb), &rResp); err != nil {
		return ctx.Reply("解析游戏服务器响应失败")
	}
	if !rResp.Status {
		return ctx.Reply(fmt.Sprintf("提现金币失败：%s", rResp.Message))
	}

	acc.Gold -= amount
	saveAccount(db, &acc)

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 提现金币成功\n"+
		"- **玩家**: %s\n"+
		"- **金额**: %d\n"+
		"- **当前余额**: %d\n"+
		"- **结果**: %s",
		avatarURL,
		contract.MentionUser(authorID),
		playerName,
		amount,
		acc.Gold,
		rResp.Message,
	)
	return ctx.ReplyMarkdown(md)
}

// withdrawExpToGame 从本地扣减经验值，调用游戏 API 给玩家增加经验值。
func withdrawExpToGame(ctx contract.CommandContext, db *gorm.DB, avatarURL, serverURL, token, playerName, authorID string, amount int) error {
	acc := getAccount(db, authorID)
	if amount > acc.Exp {
		return ctx.Reply(fmt.Sprintf("经验值不足！当前：%d，需要：%d", acc.Exp, amount))
	}

	// 调用游戏 API give 经验值
	rb, err := postRequest(serverURL, token, "/api/exp", map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     "give",
	})
	if err != nil {
		return ctx.Reply(fmt.Sprintf("提现经验值失败：%v\n\n说明：需玩家在线才能提现", err))
	}
	var rResp simpleResponse
	if err := json.Unmarshal([]byte(rb), &rResp); err != nil {
		return ctx.Reply("解析游戏服务器响应失败")
	}
	if !rResp.Status {
		return ctx.Reply(fmt.Sprintf("提现经验值失败：%s", rResp.Message))
	}

	acc.Exp -= amount
	saveAccount(db, &acc)

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 提现经验值成功\n"+
		"- **玩家**: %s\n"+
		"- **金额**: %d\n"+
		"- **当前余额**: %d\n"+
		"- **结果**: %s",
		avatarURL,
		contract.MentionUser(authorID),
		playerName,
		amount,
		acc.Exp,
		rResp.Message,
	)
	return ctx.ReplyMarkdown(md)
}

// withdrawExpLevelToGame 从本地扣减经验等级，调用游戏 API 给玩家增加经验等级。
func withdrawExpLevelToGame(ctx contract.CommandContext, db *gorm.DB, avatarURL, serverURL, token, playerName, authorID string, amount int) error {
	acc := getAccount(db, authorID)
	if amount > acc.ExpLevel {
		return ctx.Reply(fmt.Sprintf("经验等级不足！当前：%d，需要：%d", acc.ExpLevel, amount))
	}

	// 调用游戏 API give_level 经验等级
	rb, err := postRequest(serverURL, token, "/api/exp", map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     "give_level",
	})
	if err != nil {
		return ctx.Reply(fmt.Sprintf("提现经验等级失败：%v\n\n说明：需玩家在线才能提现", err))
	}
	var rResp simpleResponse
	if err := json.Unmarshal([]byte(rb), &rResp); err != nil {
		return ctx.Reply("解析游戏服务器响应失败")
	}
	if !rResp.Status {
		return ctx.Reply(fmt.Sprintf("提现经验等级失败：%s\n\n说明：需玩家在线才能提现", rResp.Message))
	}

	acc.ExpLevel -= amount
	saveAccount(db, &acc)

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 提现经验等级成功\n"+
		"- **玩家**: %s\n"+
		"- **等级**: %d\n"+
		"- **当前剩余**: %d\n"+
		"- **结果**: %s",
		avatarURL,
		contract.MentionUser(authorID),
		playerName,
		amount,
		acc.ExpLevel,
		rResp.Message,
	)
	return ctx.ReplyMarkdown(md)
}

// ── 给予 ──

// handleGive 处理给予命令：管理员向指定用户添加金币或经验值。
// 用法：给予 <金币|经验值> <金额> [@用户]
func handleGive(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	// 解析参数
	var wt withdrawType
	var amount int
	var err error

	switch ctx.ArgCount() {
	case 0:
		md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
			"## 给予用法\n" +
			"- 给予 金币 <金额> [@用户]\n" +
			"- 给予 经验值 <金额> [@用户]\n" +
			"- 给予 经验等级 <数量> [@用户]\n" +
			"- 示例：给予 金币 1000 @用户\n" +
			"- 不 @用户 时默认给予自己"
		return ctx.ReplyMarkdown(md)

	case 1:
		wt = withdrawGold
		amount, err = strconv.Atoi(strings.TrimSpace(ctx.Arg(0)))
		if err != nil || amount <= 0 {
			return ctx.Reply("金额必须是正整数")
		}

	case 2:
		typeStr := strings.TrimSpace(ctx.Arg(0))
		switch typeStr {
		case "金币":
			wt = withdrawGold
		case "经验值":
			wt = withdrawExp
		case "经验等级":
			wt = withdrawExpLevel
		default:
			return ctx.Reply(fmt.Sprintf("未知类型「%s」，请使用「金币」「经验值」或「经验等级」", typeStr))
		}
		amount, err = strconv.Atoi(strings.TrimSpace(ctx.Arg(1)))
		if err != nil || amount <= 0 {
			return ctx.Reply("金额必须是正整数")
		}

	default:
		return ctx.Reply("参数过多，用法：给予 <金币|经验值> <金额> [@用户]")
	}

	// 检测是否有 @用户
	mentions := ctx.Mentions()
	if len(mentions) > 0 {
		// 给予指定用户
		return handleGiveToUser(ctx, pc, serverURL, token, avatarURL, wt, amount, mentions[0])
	}

	// 默认给予自己
	return handleGiveAmount(ctx, pc, serverURL, token, avatarURL, wt, amount)
}

// handleGiveAmount 向当前用户添加金币/经验值（含远程 API 调用）。
func handleGiveAmount(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token, avatarURL string, wt withdrawType, amount int) error {
	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	playerName, err := getPlayerName(db, ctx.AuthorID())
	if err != nil {
		return ctx.Reply(err.Error())
	}

	acc := getAccount(db, ctx.AuthorID())
	actionPath := "/api/currency"
	label := "金币"
	apiAction := "give"
	switch wt {
	case withdrawGold:
		acc.Gold += amount
	case withdrawExp:
		actionPath = "/api/exp"
		label = "经验值"
		acc.Exp += amount
	case withdrawExpLevel:
		actionPath = "/api/exp"
		label = "经验等级"
		apiAction = "give_level"
		acc.ExpLevel += amount
	}

	// 调用游戏 API 同步
	rb, apiErr := postRequest(serverURL, token, actionPath, map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     apiAction,
	})
	if apiErr != nil {
		pc.Logger.Warn("give sync to game failed, local only", "player", playerName, "error", apiErr)
	} else {
		var rResp simpleResponse
		if json.Unmarshal([]byte(rb), &rResp) == nil && !rResp.Status {
			pc.Logger.Warn("give sync to game rejected", "player", playerName, "msg", rResp.Message)
		}
	}

	saveAccount(db, &acc)

	balanceField := acc.Gold
	if wt == withdrawExp {
		balanceField = acc.Exp
	} else if wt == withdrawExpLevel {
		balanceField = acc.ExpLevel
	} else if wt == withdrawExpLevel {
		balanceField = acc.ExpLevel
	}

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 给予%s成功\n"+
		"- **玩家**: %s\n"+
		"- **金额**: +%d\n"+
		"- **当前余额**: %d\n",
		avatarURL,
		contract.MentionUser(ctx.AuthorID()),
		label,
		playerName,
		amount,
		balanceField,
	)
	return ctx.ReplyMarkdown(md)
}

// handleGiveToUser 向指定 QQ 用户添加金币/经验值。
func handleGiveToUser(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token, avatarURL string, wt withdrawType, amount int, targetID string) error {
	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	// 获取目标用户的玩家名
	playerName, err := getPlayerName(db, targetID)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("目标用户 %s", err.Error()))
	}

	acc := getAccount(db, targetID)
	actionPath := "/api/currency"
	label := "金币"
	apiAction := "give"
	switch wt {
	case withdrawGold:
		acc.Gold += amount
	case withdrawExp:
		actionPath = "/api/exp"
		label = "经验值"
		acc.Exp += amount
	case withdrawExpLevel:
		actionPath = "/api/exp"
		label = "经验等级"
		apiAction = "give_level"
		acc.ExpLevel += amount
	}

	// 调用游戏 API 同步
	rb, apiErr := postRequest(serverURL, token, actionPath, map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     apiAction,
	})
	if apiErr != nil {
		pc.Logger.Warn("give sync to game failed, local only", "player", playerName, "error", apiErr)
	} else {
		var rResp simpleResponse
		if json.Unmarshal([]byte(rb), &rResp) == nil && !rResp.Status {
			pc.Logger.Warn("give sync to game rejected", "player", playerName, "msg", rResp.Message)
		}
	}

	saveAccount(db, &acc)

	balanceField := acc.Gold
	if wt == withdrawExp {
		balanceField = acc.Exp
	} else if wt == withdrawExpLevel {
		balanceField = acc.ExpLevel
	}

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 给予%s成功\n"+
		"- **操作者**: %s\n"+
		"- **目标**: %s\n"+
		"- **玩家名**: %s\n"+
		"- **金额**: +%d\n"+
		"- **对方余额**: %d\n",
		avatarURL,
		contract.MentionUser(ctx.AuthorID()),
		label,
		contract.MentionUser(ctx.AuthorID()),
		contract.MentionUser(targetID),
		playerName,
		amount,
		balanceField,
	)
	return ctx.ReplyMarkdown(md)
}

// ── 扣除 ──

// handleDeduct 处理扣除命令：管理员扣减指定用户的金币或经验值。
// 用法：扣除 <金币|经验值> <金额> [@用户]
func handleDeduct(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())

	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	// 解析参数
	var wt withdrawType
	var amount int
	var err error

	switch ctx.ArgCount() {
	case 0:
		md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
			"## 扣除用法\n" +
			"- 扣除 金币 <金额> [@用户]\n" +
			"- 扣除 经验值 <金额> [@用户]\n" +
			"- 扣除 经验等级 <数量> [@用户]\n" +
			"- 示例：扣除 金币 500 @用户\n" +
			"- 不 @用户 时默认扣除自己"
		return ctx.ReplyMarkdown(md)

	case 1:
		wt = withdrawGold
		amount, err = strconv.Atoi(strings.TrimSpace(ctx.Arg(0)))
		if err != nil || amount <= 0 {
			return ctx.Reply("金额必须是正整数")
		}

	case 2:
		typeStr := strings.TrimSpace(ctx.Arg(0))
		switch typeStr {
		case "金币":
			wt = withdrawGold
		case "经验值":
			wt = withdrawExp
		case "经验等级":
			wt = withdrawExpLevel
		default:
			return ctx.Reply(fmt.Sprintf("未知类型「%s」，请使用「金币」「经验值」或「经验等级」", typeStr))
		}
		amount, err = strconv.Atoi(strings.TrimSpace(ctx.Arg(1)))
		if err != nil || amount <= 0 {
			return ctx.Reply("金额必须是正整数")
		}

	default:
		return ctx.Reply("参数过多，用法：扣除 <金币|经验值> <金额> [@用户]")
	}

	// 检测是否有 @用户
	mentions := ctx.Mentions()
	if len(mentions) > 0 {
		return handleDeductFromUser(ctx, pc, serverURL, token, avatarURL, wt, amount, mentions[0])
	}

	return handleDeductFromSelf(ctx, pc, serverURL, token, avatarURL, wt, amount)
}

// handleDeductFromSelf 扣除当前用户的金币/经验值（含远程 API 调用）。
func handleDeductFromSelf(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token, avatarURL string, wt withdrawType, amount int) error {
	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	playerName, err := getPlayerName(db, ctx.AuthorID())
	if err != nil {
		return ctx.Reply(err.Error())
	}

	acc := getAccount(db, ctx.AuthorID())
	actionPath := "/api/currency"
	label := "金币"
	apiAction := "take"
	switch wt {
	case withdrawGold:
		if amount > acc.Gold {
			return ctx.Reply(fmt.Sprintf("金币不足！当前：%d，需要扣除：%d", acc.Gold, amount))
		}
		acc.Gold -= amount
	case withdrawExp:
		actionPath = "/api/exp"
		label = "经验值"
		if amount > acc.Exp {
			return ctx.Reply(fmt.Sprintf("经验值不足！当前：%d，需要扣除：%d", acc.Exp, amount))
		}
		acc.Exp -= amount
	case withdrawExpLevel:
		actionPath = "/api/exp"
		label = "经验等级"
		apiAction = "take_level"
		if amount > acc.ExpLevel {
			return ctx.Reply(fmt.Sprintf("经验等级不足！当前：%d，需要扣除：%d", acc.ExpLevel, amount))
		}
		acc.ExpLevel -= amount
	}

	// 调用游戏 API 同步
	rb, apiErr := postRequest(serverURL, token, actionPath, map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     apiAction,
	})
	if apiErr != nil {
		pc.Logger.Warn("deduct sync to game failed, local only", "player", playerName, "error", apiErr)
	} else {
		var rResp simpleResponse
		if json.Unmarshal([]byte(rb), &rResp) == nil && !rResp.Status {
			pc.Logger.Warn("deduct sync to game rejected", "player", playerName, "msg", rResp.Message)
		}
	}

	saveAccount(db, &acc)

	balanceField := acc.Gold
	if wt == withdrawExp {
		balanceField = acc.Exp
	} else if wt == withdrawExpLevel {
		balanceField = acc.ExpLevel
	}

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 扣除%s成功\n"+
		"- **玩家**: %s\n"+
		"- **金额**: -%d\n"+
		"- **当前余额**: %d\n",
		avatarURL,
		contract.MentionUser(ctx.AuthorID()),
		label,
		playerName,
		amount,
		balanceField,
	)
	return ctx.ReplyMarkdown(md)
}

// handleDeductFromUser 扣除指定 QQ 用户的金币/经验值。
func handleDeductFromUser(ctx contract.CommandContext, pc *contract.PluginContext, serverURL, token, avatarURL string, wt withdrawType, amount int, targetID string) error {
	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	playerName, err := getPlayerName(db, targetID)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("目标用户 %s", err.Error()))
	}

	acc := getAccount(db, targetID)
	actionPath := "/api/currency"
	label := "金币"
	apiAction := "take"
	switch wt {
	case withdrawGold:
		if amount > acc.Gold {
			return ctx.Reply(fmt.Sprintf("对方金币不足！当前：%d，需要扣除：%d", acc.Gold, amount))
		}
		acc.Gold -= amount
	case withdrawExp:
		actionPath = "/api/exp"
		label = "经验值"
		if amount > acc.Exp {
			return ctx.Reply(fmt.Sprintf("对方经验值不足！当前：%d，需要扣除：%d", acc.Exp, amount))
		}
		acc.Exp -= amount
	case withdrawExpLevel:
		actionPath = "/api/exp"
		label = "经验等级"
		apiAction = "take_level"
		if amount > acc.ExpLevel {
			return ctx.Reply(fmt.Sprintf("对方经验等级不足！当前：%d，需要扣除：%d", acc.ExpLevel, amount))
		}
		acc.ExpLevel -= amount
	}

	// 调用游戏 API 同步
	rb, apiErr := postRequest(serverURL, token, actionPath, map[string]interface{}{
		"playerName": playerName,
		"amount":     amount,
		"action":     apiAction,
	})
	if apiErr != nil {
		pc.Logger.Warn("deduct sync to game failed, local only", "player", playerName, "error", apiErr)
	} else {
		var rResp simpleResponse
		if json.Unmarshal([]byte(rb), &rResp) == nil && !rResp.Status {
			pc.Logger.Warn("deduct sync to game rejected", "player", playerName, "msg", rResp.Message)
		}
	}

	saveAccount(db, &acc)

	balanceField := acc.Gold
	if wt == withdrawExp {
		balanceField = acc.Exp
	} else if wt == withdrawExpLevel {
		balanceField = acc.ExpLevel
	}

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## ✅ 扣除%s成功\n"+
		"- **操作者**: %s\n"+
		"- **目标**: %s\n"+
		"- **玩家名**: %s\n"+
		"- **金额**: -%d\n"+
		"- **对方余额**: %d\n",
		avatarURL,
		contract.MentionUser(ctx.AuthorID()),
		label,
		contract.MentionUser(ctx.AuthorID()),
		contract.MentionUser(targetID),
		playerName,
		amount,
		balanceField,
	)
	return ctx.ReplyMarkdown(md)
}

// ── HTTP 请求工具 ──

// postRequest 向 MC 服务器发送 POST 请求，返回响应体字符串。
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

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("服务器返回错误状态码: %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}
