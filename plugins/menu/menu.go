// Package menu 提供帮助菜单功能。
package menu

import (
	"fmt"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

// MenuPlugin 实现 contract.Plugin 接口。
type MenuPlugin struct{}

func (p *MenuPlugin) Name() string { return "menu" }

func (p *MenuPlugin) Init(pc *contract.PluginContext) error {
	pc.Logger.Info("menu plugin initialized")

	pc.Commands.Register(contract.Command{
		Name:        "menu",
		Aliases:     []string{"菜单", "帮助"},
		Description: "帮助菜单",
		Handler: func(ctx contract.CommandContext) error {
			avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640",
				pc.QQAPI.AppID(), ctx.AuthorID())

			md := "![img #30px #30px](" + avatarURL + ") | " + contract.MentionUser(ctx.AuthorID()) + "\n" +
				"## 帮助菜单\n" +
				"- " + contract.CmdInput("申请白名单", "申请白名单 <玩家名>", false) +
				"\n" +
				"- " + contract.CmdInput("在线时长", "在线时长 <玩家名>", false) +
				"\n" +
				"- " + contract.CmdInput("在线玩家", "在线玩家", false) +
				"\n" +
				"- " + contract.CmdInput("查询白名单", "查询白名单 <玩家名>|<@用户>", false) +
				"\n" +
				"- " + contract.CmdInput("银行", "银行", false) +
				"\n" +
				"- " + contract.CmdInput("签到", "签到", false) +
				"\n" +
				"- " + contract.CmdInput("我的签到", "我的签到", false)

			return ctx.ReplyMarkdown(md)
		},
	})

	return nil
}
