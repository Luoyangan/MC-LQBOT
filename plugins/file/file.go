package file

import (
	"github.com/Luoyangan/LQBOT/internal/contract"
)

// FilePlugin 实现 contract.Plugin 接口。
type FilePlugin struct{}

func (p *FilePlugin) Name() string { return "file" }

func (p *FilePlugin) Init(pc *contract.PluginContext) error {
	pc.Logger.Info("file plugin initialized")

	pc.Commands.Register(contract.Command{
		Name:        "材质包",
		Description: "发送材质包",
		Usage:       "材质包",
		Handler: func(ctx contract.CommandContext) error {
			fileURL := "https://wd.lilei007.cn/Luoyangan/mcyszl_sf2.0_resource.zip"

			switch ctx.Scene() {
			case contract.SceneGroup:
				return pc.QQAPI.SendGroupRichMedia(ctx.GroupID(), &contract.RichMedia{
					FileType: 4, // file
					URL:      fileURL,
					Content:  "原生之旅-粘液科技材质包",
					MsgID:    ctx.MessageID(),
				})
			case contract.SceneC2C:
				return pc.QQAPI.SendC2CRichMedia(ctx.AuthorID(), &contract.RichMedia{
					FileType: 4,
					URL:      fileURL,
					MsgID:    ctx.MessageID(),
				})
			default:
				return ctx.Reply("该命令仅支持群聊使用")
			}
		},
	})

	return nil
}
