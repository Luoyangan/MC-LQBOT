// Package worldinfo 提供 MC 服务器世界信息查询功能。
package worldinfo

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

// WorldInfoPlugin 实现 contract.Plugin 接口。
type WorldInfoPlugin struct{}

func (p *WorldInfoPlugin) Name() string { return "worldinfo" }

func (p *WorldInfoPlugin) Init(pc *contract.PluginContext) error {
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

	pc.Logger.Info("worldinfo plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	pc.Commands.Register(contract.Command{
		Name:        "世界信息",
		Description: "查询 Minecraft 服务器所有世界及其玩家、时间、天气等信息",
		Usage:       "世界信息",
		Handler: func(ctx contract.CommandContext) error {
			return handleWorldInfo(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// ── 响应结构 ──

type worldInfoResponse struct {
	Status  bool          `json:"status"`
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    worldInfoData `json:"data"`
}

type worldInfoData struct {
	WorldInfo worldInfoBody `json:"worldInfo"`
}

type worldInfoBody struct {
	TotalWorlds  int     `json:"totalWorlds"`
	TotalPlayers int     `json:"totalPlayers"`
	Worlds       []world `json:"worlds"`
}

type world struct {
	Name          string   `json:"name"`
	PlayerCount   int      `json:"playerCount"`
	Players       []string `json:"players"`
	Time          int64    `json:"time"`
	TimeFormatted string   `json:"timeFormatted"`
	Weather       string   `json:"weather"`
	Environment   string   `json:"environment"`
}

// weatherIcon 返回天气对应的图标。
func weatherIcon(w string) string {
	switch strings.ToUpper(w) {
	case "CLEAR":
		return "☀️"
	case "RAIN":
		return "🌧️"
	case "THUNDER":
		return "⛈️"
	case "SNOW":
		return "❄️"
	case "FOG":
		return "🌫️"
	default:
		return "☁️"
	}
}

// envIcon 返回环境对应的图标。
func envIcon(e string) string {
	switch strings.ToUpper(e) {
	case "NORMAL":
		return "🌍"
	case "NETHER":
		return "🔥"
	case "THE_END":
		return "🌌"
	default:
		return "🌏"
	}
}

// handleWorldInfo 查询 MC 服务器世界信息。
func handleWorldInfo(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	body, err := queryWorldInfo(mcServerURL, mcToken)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("查询世界信息失败：%v", err))
	}

	var resp worldInfoResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return ctx.Reply("解析服务器响应失败")
	}

	if !resp.Status {
		return ctx.Reply(fmt.Sprintf("查询失败：%s", resp.Message))
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("## 世界信息\n"))
	buf.WriteString(fmt.Sprintf("世界总数：`%d`，玩家总数：`%d`\n\n", resp.Data.WorldInfo.TotalWorlds, resp.Data.WorldInfo.TotalPlayers))

	for i, w := range resp.Data.WorldInfo.Worlds {
		if i > 0 {
			buf.WriteString("---\n")
		}

		buf.WriteString(fmt.Sprintf("### %s %s\n", envIcon(w.Environment), w.Name))
		buf.WriteString(fmt.Sprintf("- 环境：`%s`\n", w.Environment))
		buf.WriteString(fmt.Sprintf("- 时间：`%s`（游戏刻 `%d`）\n", w.TimeFormatted, w.Time))
		buf.WriteString(fmt.Sprintf("- 天气：%s `%s`\n", weatherIcon(w.Weather), w.Weather))
		buf.WriteString(fmt.Sprintf("- 玩家：`%d` 人在线\n", w.PlayerCount))

		if w.PlayerCount > 0 && len(w.Players) > 0 {
			buf.WriteString(fmt.Sprintf("- 玩家列表：`%s`\n", strings.Join(w.Players, "`、`")))
		}
	}

	return ctx.ReplyMarkdown(buf.String())
}

// queryWorldInfo 向 MC 服务器发送 POST 请求查询世界信息。
func queryWorldInfo(serverURL, token string) (string, error) {
	payload := map[string]string{"action": "query"}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+"/api/worldinfo", bytes.NewReader(jsonBody))
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
