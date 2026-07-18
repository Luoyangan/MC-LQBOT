package checkin

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	"gorm.io/gorm"
)

// CheckinRecord 签到记录表（GORM 模型）
type CheckinRecord struct {
	AuthorID        string    `gorm:"primaryKey;size:512"`
	LastCheckinDate string    `gorm:"column:last_checkin_date;size:16;not null"` // 最后签到日期，格式 "2006-01-02"
	ConsecutiveDays int       `gorm:"column:consecutive_days;default:0;not null"`
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// CheckinPlugin 实现 contract.Plugin 接口。
type CheckinPlugin struct{}

func (p *CheckinPlugin) Name() string { return "checkin" }

func (p *CheckinPlugin) Init(pc *contract.PluginContext) error {
	// Auto-migrate checkin table
	db, ok := pc.RawDB.(*gorm.DB)
	if !ok || db == nil {
		return fmt.Errorf("checkin plugin: RawDB is not available")
	}
	if err := db.AutoMigrate(&CheckinRecord{}); err != nil {
		return fmt.Errorf("auto migrate checkin table: %w", err)
	}

	pc.Logger.Info("checkin plugin initialized")

	pc.Commands.Register(contract.Command{
		Name:        "签到",
		Aliases:     []string{"signin"},
		Description: "每日签到，获取连续签到奖励",
		Usage:       "签到",
		Handler: func(ctx contract.CommandContext) error {
			return handleCheckin(ctx, pc)
		},
	})

	pc.Commands.Register(contract.Command{
		Name:        "签到状态",
		Aliases:     []string{"signinstatus", "我的签到"},
		Description: "查询签到状态（连续天数等）",
		Usage:       "签到状态",
		Handler: func(ctx contract.CommandContext) error {
			return handleCheckinStatus(ctx, pc)
		},
	})

	return nil
}

// getDB 从 PluginContext 中获取 GORM DB 实例。
func getDB(pc *contract.PluginContext) *gorm.DB {
	if db, ok := pc.RawDB.(*gorm.DB); ok {
		return db
	}
	return nil
}

// getRecord 获取用户签到记录，不存在则返回默认零值。
func getRecord(db *gorm.DB, authorID string) CheckinRecord {
	var rec CheckinRecord
	if err := db.First(&rec, "author_id = ?", authorID).Error; err != nil {
		return CheckinRecord{AuthorID: authorID}
	}
	return rec
}

// bankAccount 银行账户表映射（与 bank 插件共用同一张表）
type bankAccount struct {
	AuthorID string `gorm:"primaryKey;size:512"`
	Gold     int    `gorm:"default:0;not null"`
	Exp      int    `gorm:"default:0;not null"`
	ExpLevel int    `gorm:"default:0;not null"`
}

// getBankAccount 获取用户银行账户，不存在则返回默认零值。
func getBankAccount(db *gorm.DB, authorID string) bankAccount {
	var acc bankAccount
	if err := db.First(&acc, "author_id = ?", authorID).Error; err != nil {
		return bankAccount{AuthorID: authorID}
	}
	return acc
}

// saveRecord 保存签到记录。
func saveRecord(db *gorm.DB, rec *CheckinRecord) {
	db.Save(rec)
}

// randRange 返回 [min, max] 范围内的随机整数。
func randRange(min, max int) int {
	if min >= max {
		return min
	}
	return rand.Intn(max-min+1) + min
}

// todayDate 返回今天的日期字符串，格式 "2006-01-02"。
func todayDate() string {
	return time.Now().Format("2006-01-02")
}

// yesterdayDate 返回昨天的日期字符串，格式 "2006-01-02"。
func yesterdayDate() string {
	return time.Now().AddDate(0, 0, -1).Format("2006-01-02")
}

// handleCheckin 处理签到命令。
func handleCheckin(ctx contract.CommandContext, pc *contract.PluginContext) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	rec := getRecord(db, ctx.AuthorID())
	today := todayDate()

	// 已签到过
	if rec.LastCheckinDate == today {
		return ctx.Reply(fmt.Sprintf("你今天已签到过了！当前连续签到：%d 天", rec.ConsecutiveDays))
	}

	// 判断是否是连续签到（昨天签到了）
	if rec.LastCheckinDate == yesterdayDate() {
		rec.ConsecutiveDays++
	} else {
		// 断签或首次签到
		rec.ConsecutiveDays = 1
	}

	rec.LastCheckinDate = today
	saveRecord(db, &rec)

	// 随机奖励
	goldReward := randRange(30, 500)  // 金币奖励
	expReward := randRange(30, 500)   // 经验值奖励
	expLevelReward := randRange(1, 4) // 经验等级奖励

	// 更新银行账户
	acc := getBankAccount(db, ctx.AuthorID())
	acc.Gold += goldReward
	acc.Exp += expReward
	acc.ExpLevel += expLevelReward
	db.Save(&acc)

	var md strings.Builder
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())
	md.WriteString(fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(ctx.AuthorID())))
	md.WriteString("## ✅ 签到成功\n")
	md.WriteString(fmt.Sprintf("- **连续签到**：%d 天\n", rec.ConsecutiveDays))
	md.WriteString(fmt.Sprintf("- 🎁 **金币**：+%d\n", goldReward))
	md.WriteString(fmt.Sprintf("- ⭐ **经验值**：+%d\n", expReward))
	md.WriteString(fmt.Sprintf("- 🏆 **经验等级**：+%d\n", expLevelReward))

	return ctx.ReplyMarkdown(md.String())
}

// handleCheckinStatus 查询签到状态。
func handleCheckinStatus(ctx contract.CommandContext, pc *contract.PluginContext) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	db := getDB(pc)
	if db == nil {
		return ctx.Reply("数据库不可用")
	}

	rec := getRecord(db, ctx.AuthorID())
	today := todayDate()

	var md strings.Builder
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())
	md.WriteString(fmt.Sprintf("![img #30px #30px](%s) | %s\n", avatarURL, contract.MentionUser(ctx.AuthorID())))
	md.WriteString("## 签到状态\n")
	md.WriteString(fmt.Sprintf("- **连续签到**：%d 天\n", rec.ConsecutiveDays))

	if rec.LastCheckinDate == today {
		md.WriteString("- **今日签到**：✅ 已签到\n")
	} else {
		md.WriteString("- **今日签到**：❌ 未签到\n")
		md.WriteString("  发送「" + contract.CmdInput("签到", "签到", false) + "」即可签到\n")
	}

	if rec.LastCheckinDate != "" {
		md.WriteString(fmt.Sprintf("- **上次签到**：%s\n", rec.LastCheckinDate))
	}

	if rec.LastCheckinDate != today {
		buttons := [][]contract.MessageButton{
			{{ID: "btn_signin", Label: "去签到", Data: "签到", Style: 1, ActionType: 2}},
		}
		return ctx.ReplyWithButtonRows(md.String(), buttons)
	}
	return ctx.ReplyMarkdown(md.String())
}
