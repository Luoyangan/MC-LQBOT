// Package onlinetime 提供 MC 服务器玩家在线时长查询功能。
package onlinetime

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

// OnlineTimePlugin 实现 contract.Plugin 接口。
type OnlineTimePlugin struct{}

func (p *OnlineTimePlugin) Name() string { return "onlinetime" }

func (p *OnlineTimePlugin) Init(pc *contract.PluginContext) error {
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

	pc.Logger.Info("onlinetime plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	// Register chat command for query
	pc.Commands.Register(contract.Command{
		Name:        "在线时长",
		Description: "查询玩家游戏在线时长",
		Usage:       "在线时长 <玩家名>",
		Handler: func(ctx contract.CommandContext) error {
			return handleTimeQuery(ctx, pc, mcServerURL, mcToken)
		},
	})

	// Register HTTP API endpoint
	if pc.HTTPServer != nil {
		pc.HTTPServer.Handle("/api/onlinetime", func(w http.ResponseWriter, r *http.Request) {
			handleHTTPQuery(w, r, pc, mcServerURL, mcToken)
		})
	}

	return nil
}

// ── API 请求/响应结构 ──

// onlineTimeRequest 是发送给 MC 服务器和接收客户端请求的统一结构。
type onlineTimeRequest struct {
	PlayerName string `json:"playerName"`
	Action     string `json:"action"` // query/list/top
	Limit      int    `json:"limit"`
}

// onlineTimeResponse 是统一的 API 响应结构。
type onlineTimeResponse struct {
	Status  bool        `json:"status"`
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// mcQueryResponse 对应 MC 服务器返回的 data 结构（data.onlineTime）。
type mcQueryResponse struct {
	OnlineTime onlineTimeData `json:"onlineTime"`
}

// onlineTimeData 是在线时长数据主体。
type onlineTimeData struct {
	PlayerName         string `json:"playerName"`
	TotalTime          int64  `json:"totalTime"`
	TodayTime          int64  `json:"todayTime"`
	TotalTimeFormatted string `json:"totalTimeFormatted"`
	TodayTimeFormatted string `json:"todayTimeFormatted"`
	IsOnline           bool   `json:"isOnline"`
}

// ── 指令处理 ──

// handleTimeQuery 处理群聊中的在线时长查询指令。
func handleTimeQuery(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	if ctx.ArgCount() == 0 {
		md := "## 在线时长用法\n" +
			"- **用法**：在线时长 <玩家名>\n" +
			"- **示例**：在线时长 Steve"
		buttons := [][]contract.MessageButton{
			{{ID: "btn_baiming", Label: "继续查询", Data: "在线时长 ", Style: 1, ActionType: 2}},
		}
		return ctx.ReplyWithButtonRows(md, buttons)
	}

	playerName := strings.TrimSpace(ctx.Arg(0))
	if playerName == "" {
		return ctx.Reply("玩家名不能为空")
	}

	req := onlineTimeRequest{
		PlayerName: playerName,
		Action:     "query",
	}

	body, err := queryMCServer(mcServerURL, mcToken, req)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("查询在线时长失败：%v", err))
	}

	var resp onlineTimeResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return ctx.Reply("解析服务器响应失败")
	}

	if !resp.Status {
		return ctx.Reply(fmt.Sprintf("查询失败：%s", resp.Message))
	}

	// 解析 data.onlineTime 字段
	dataBytes, _ := json.Marshal(resp.Data)
	var mcResp mcQueryResponse
	if err := json.Unmarshal(dataBytes, &mcResp); err != nil {
		return ctx.Reply("解析数据失败")
	}
	data := mcResp.OnlineTime

	// 若 MC 服务器未返回格式化字符串，则本地格式化
	totalFormatted := data.TotalTimeFormatted
	if totalFormatted == "" {
		totalFormatted = formatDuration(data.TotalTime)
	}
	todayFormatted := data.TodayTimeFormatted
	if todayFormatted == "" {
		todayFormatted = formatDuration(data.TodayTime)
	}

	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())
	onlineStatus := "❌ 离线"
	if data.IsOnline {
		onlineStatus = "✅ 在线"
	}

	md := fmt.Sprintf("![img #30px #30px](%s) | %s\n"+
		"## 玩家在线时长\n"+
		"- **玩家名**: %s\n"+
		"- **总在线**: %s\n"+
		"- **今日在线**: %s\n"+
		"- **状态**: %s",
		avatarURL, contract.MentionUser(ctx.AuthorID()),
		playerName,
		totalFormatted,
		todayFormatted,
		onlineStatus,
	)
	return ctx.ReplyMarkdown(md)
}

// ── HTTP API 处理 ──

// handleHTTPQuery 处理 HTTP API 请求。
func handleHTTPQuery(w http.ResponseWriter, r *http.Request, pc *contract.PluginContext, mcServerURL, mcToken string) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, onlineTimeResponse{
			Status:  false,
			Code:    405,
			Message: "仅支持 POST 请求",
		})
		return
	}

	var req onlineTimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, onlineTimeResponse{
			Status:  false,
			Code:    400,
			Message: "请求参数解析失败",
		})
		return
	}

	// 设置默认值
	if req.Action == "" {
		req.Action = "query"
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}

	// 参数校验
	if req.Action == "query" || req.Action == "top" {
		if req.PlayerName == "" {
			writeJSON(w, http.StatusBadRequest, onlineTimeResponse{
				Status:  false,
				Code:    400,
				Message: "playerName 不能为空",
			})
			return
		}
	}

	body, err := queryMCServer(mcServerURL, mcToken, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, onlineTimeResponse{
			Status:  false,
			Code:    500,
			Message: fmt.Sprintf("查询失败：%v", err),
		})
		return
	}

	// 验证 MC 服务器响应为合法 JSON
	var mcResp onlineTimeResponse
	if err := json.Unmarshal([]byte(body), &mcResp); err != nil {
		writeJSON(w, http.StatusInternalServerError, onlineTimeResponse{
			Status:  false,
			Code:    500,
			Message: "MC 服务器响应解析失败",
		})
		return
	}

	// 若 MC 服务器未返回格式化字符串，则本地格式化
	if mcResp.Status && mcResp.Data != nil {
		dataBytes, _ := json.Marshal(mcResp.Data)
		var mq mcQueryResponse
		if json.Unmarshal(dataBytes, &mq) == nil {
			ot := mq.OnlineTime
			if ot.TotalTimeFormatted == "" {
				ot.TotalTimeFormatted = formatDuration(ot.TotalTime)
			}
			if ot.TodayTimeFormatted == "" {
				ot.TodayTimeFormatted = formatDuration(ot.TodayTime)
			}
			mq.OnlineTime = ot
			mcResp.Data = mq
			patched, _ := json.Marshal(mcResp)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(patched)
			return
		}
	}

	// 转发 MC 服务器响应
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// writeJSON 写入 JSON 响应。
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

// ── MC 服务器通信 ──

// queryMCServer 向 MC 服务器发送 POST 请求查询在线时长。
func queryMCServer(serverURL, token string, req onlineTimeRequest) (string, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequest("POST", serverURL+"/api/onlinetime", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
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

// formatDuration 将秒数格式化为可读字符串。
func formatDuration(seconds int64) string {
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}
