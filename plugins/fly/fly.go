// Package fly 提供 MC 服务器飞行状态查询与控制功能。
package fly

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
)

const (
	defaultMCURL   = "http://localhost:25566"
	defaultMCToken = ""
)

// FlyPlugin 实现 contract.Plugin 接口。
type FlyPlugin struct{}

func (p *FlyPlugin) Name() string { return "fly" }

func (p *FlyPlugin) Init(pc *contract.PluginContext) error {
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

	pc.Logger.Info("fly plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	pc.Commands.Register(contract.Command{
		Name:        "飞行",
		Aliases:     []string{"fly"},
		Description: "查询或控制玩家飞行状态",
		Usage:       "飞行 <玩家名> <动作>",
		Permission:  "owner_exact",
		Handler: func(ctx contract.CommandContext) error {
			return handleFly(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// ── 响应结构 ──

type flyResponse struct {
	Status  bool    `json:"status"`
	Code    int     `json:"code"`
	Message string  `json:"message"`
	Data    flyData `json:"data,omitempty"`
}

type flyData struct {
	IsFlying    bool `json:"isFlying"`
	AllowFlight bool `json:"allowFlight"`
}

// handleFly 处理飞行命令。
func handleFly(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	playerName := strings.TrimSpace(ctx.Arg(0))
	if playerName == "" {
		md := "## 飞行控制用法\n" +
			"- **查询状态**：`飞行 <玩家名>`\n" +
			"- **开启飞行**：`飞行 <玩家名> 开启`\n" +
			"- **关闭飞行**：`飞行 <玩家名> 关闭`\n" +
			"- **切换飞行**：`飞行 <玩家名> 切换`\n" +
			"\n**示例**：`飞行 Steve 开启`"
		return ctx.ReplyMarkdown(md)
	}

	action := "query"
	if ctx.ArgCount() >= 2 {
		a := strings.TrimSpace(ctx.Arg(1))
		switch a {
		case "开启", "enable", "开":
			action = "enable"
		case "关闭", "disable", "关":
			action = "disable"
		case "切换", "toggle":
			action = "toggle"
		default:
			action = "query"
		}
	}

	body, err := queryFly(mcServerURL, mcToken, playerName, action)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("飞行操作失败：%v", err))
	}

	var resp flyResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return ctx.Reply("解析服务器响应失败")
	}

	var msg string
	switch resp.Code {
	case 200:
		if action == "enable" {
			msg = fmt.Sprintf("✅ 已为 `%s` 开启飞行", playerName)
		} else if action == "disable" {
			msg = fmt.Sprintf("✅ 已为 `%s` 关闭飞行", playerName)
		} else if action == "toggle" {
			if resp.Data.IsFlying {
				msg = fmt.Sprintf("✅ `%s` 已切换为飞行状态", playerName)
			} else {
				msg = fmt.Sprintf("✅ `%s` 已切换为非飞行状态", playerName)
			}
		}
	case 201:
		var status string
		if resp.Data.IsFlying {
			status = "🟢 飞行中"
		} else {
			status = "🔴 未飞行"
		}
		var allowed string
		if resp.Data.AllowFlight {
			allowed = "✅ 允许"
		} else {
			allowed = "❌ 不允许"
		}
		msg = fmt.Sprintf("## `%s` 飞行状态\n- **飞行状态**：%s\n- **飞行权限**：%s", playerName, status, allowed)
	default:
		if !resp.Status {
			msg = fmt.Sprintf("操作失败（%d）：%s", resp.Code, resp.Message)
		} else {
			msg = fmt.Sprintf("操作完成（%d）：%s", resp.Code, resp.Message)
		}
	}

	return ctx.ReplyMarkdown(msg)
}

// queryFly 向 MC 服务器发送 POST 请求查询/控制飞行状态。
func queryFly(serverURL, token, playerName, action string) (string, error) {
	payload := map[string]string{
		"playerName": playerName,
		"action":     action,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+"/api/flight", bytes.NewReader(jsonBody))
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

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}
