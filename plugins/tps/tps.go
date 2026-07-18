// Package tps 提供 MC 服务器 TPS/MSPT 性能数据查询功能。
package tps

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

// TPSPlugin 实现 contract.Plugin 接口。
type TPSPlugin struct{}

func (p *TPSPlugin) Name() string { return "tps" }

func (p *TPSPlugin) Init(pc *contract.PluginContext) error {
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

	pc.Logger.Info("tps plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	pc.Commands.Register(contract.Command{
		Name:        "服务器状态",
		Aliases:     []string{"TPS", "MSPT"},
		Description: "查询 Minecraft 服务器 TPS/MSPT 等性能数据",
		Usage:       "服务器状态",
		Handler: func(ctx contract.CommandContext) error {
			return handleTPS(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// ── 响应结构 ──

type tpsResponse struct {
	Status  bool   `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Server struct {
			TPS     tpsData    `json:"tps"`
			MSPT    float64    `json:"mspt"`
			Players playerData `json:"players"`
			Worlds  worldData  `json:"worlds"`
			Memory  memoryData `json:"memory"`
			Uptime  int64      `json:"uptime_ms"`
		} `json:"server"`
	} `json:"data"`
}

type tpsData struct {
	M1  float64 `json:"1m"`
	M5  float64 `json:"5m"`
	M15 float64 `json:"15m"`
}

type playerData struct {
	Online int `json:"online"`
	Max    int `json:"max"`
}

type worldData struct {
	Count        int `json:"count"`
	LoadedChunks int `json:"loaded_chunks"`
}

type memoryData struct {
	UsedMB  int64 `json:"used_mb"`
	FreeMB  int64 `json:"free_mb"`
	TotalMB int64 `json:"total_mb"`
	MaxMB   int64 `json:"max_mb"`
}

// handleTPS 查询 MC 服务器 TPS/MSPT 性能数据。
func handleTPS(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	body, err := queryTPS(mcServerURL, mcToken)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("查询服务器状态失败：%v", err))
	}

	var resp tpsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return ctx.Reply("解析服务器响应失败")
	}

	if !resp.Status {
		return ctx.Reply(fmt.Sprintf("查询失败：%s", resp.Message))
	}

	server := resp.Data.Server

	// 格式化运行时长
	uptime := formatUptime(server.Uptime)

	// 构建 TPS 状态指示
	tps1mStatus := tpsStatus(server.TPS.M1)
	tps5mStatus := tpsStatus(server.TPS.M5)
	tps15mStatus := tpsStatus(server.TPS.M15)
	msptStatus := msptStatusMsg(server.MSPT)

	// 根据 MSPT 推算理论最大 TPS（MSPT × TPS ≤ 1000）
	expectedTPS := 20.0
	if server.MSPT > 50 {
		expectedTPS = 1000.0 / server.MSPT
	}

	// 内存使用率
	memUsedPercent := float64(0)
	if server.Memory.TotalMB > 0 {
		memUsedPercent = float64(server.Memory.UsedMB) / float64(server.Memory.TotalMB) * 100
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("## 服务器性能状态\n\n"))
	buf.WriteString(fmt.Sprintf("### TPS\n"))
	buf.WriteString(fmt.Sprintf("1分钟：`%.2f` %s\n", server.TPS.M1, tps1mStatus))
	buf.WriteString(fmt.Sprintf("5分钟：`%.2f` %s\n", server.TPS.M5, tps5mStatus))
	buf.WriteString(fmt.Sprintf("15分钟：`%.2f` %s\n\n", server.TPS.M15, tps15mStatus))
	buf.WriteString(fmt.Sprintf("### MSPT（每刻毫秒数）\n"))
	buf.WriteString(fmt.Sprintf("`%.1f ms` %s\n", server.MSPT, msptStatus))
	buf.WriteString(fmt.Sprintf("理论最大 TPS：`%.2f`（MSPT × TPS ≤ 1000）\n\n", expectedTPS))
	buf.WriteString(fmt.Sprintf("### 玩家\n"))
	buf.WriteString(fmt.Sprintf("在线 `%d` / 最大 `%d`\n\n", server.Players.Online, server.Players.Max))
	buf.WriteString(fmt.Sprintf("### 世界\n"))
	buf.WriteString(fmt.Sprintf("数量 `%d`，加载区块 `%d`\n\n", server.Worlds.Count, server.Worlds.LoadedChunks))
	buf.WriteString(fmt.Sprintf("### 内存\n"))
	buf.WriteString(fmt.Sprintf("已用 `%d MB` / 总计 `%d MB` / 最大 `%d MB`（`%.1f%%`）\n", server.Memory.UsedMB, server.Memory.TotalMB, server.Memory.MaxMB, memUsedPercent))
	buf.WriteString(fmt.Sprintf("空闲 `%d MB`\n\n", server.Memory.FreeMB))
	buf.WriteString(fmt.Sprintf("### 运行时间\n"))
	buf.WriteString(fmt.Sprintf("`%s`", uptime))

	return ctx.ReplyMarkdown(buf.String())
}

// tpsStatus 根据 TPS 值返回状态指示。
func tpsStatus(tps float64) string {
	if tps >= 19.0 {
		return "✅"
	} else if tps >= 17.0 {
		return "⚠️"
	}
	return "❌"
}

// msptStatusMsg 根据 MSPT 值返回状态描述。
// MSPT ≤ 50 → TPS 可维持 20（不掉刻）
// MSPT > 50 → TPS 开始下降，游戏卡顿
func msptStatusMsg(mspt float64) string {
	if mspt < 30 {
		return "🟢 流畅（TPS = 20）"
	} else if mspt <= 50 {
		return "🟡 正常（TPS = 20，未超游戏刻间隔）"
	}
	return "🔴 卡顿（TPS < 20，超游戏刻间隔）"
}

// formatUptime 将毫秒数格式化为可读的时长字符串。
func formatUptime(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d天", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d小时", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d分", minutes))
	}
	parts = append(parts, fmt.Sprintf("%d秒", seconds))
	return strings.Join(parts, " ")
}

// queryTPS 向 MC 服务器发送 POST 请求查询性能数据。
func queryTPS(serverURL, token string) (string, error) {
	body := map[string]string{"action": "query"}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+"/api/tps", bytes.NewReader(jsonBody))
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
