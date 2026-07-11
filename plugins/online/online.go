// Package online 提供 MC 服务器在线玩家查询功能。
package online

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

// OnlinePlugin 实现 contract.Plugin 接口。
type OnlinePlugin struct{}

func (p *OnlinePlugin) Name() string { return "online" }

func (p *OnlinePlugin) Init(pc *contract.PluginContext) error {
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

	pc.Logger.Info("online plugin initialized",
		"mc_server_url", mcServerURL,
		"mc_token_set", mcToken != "",
	)

	pc.Commands.Register(contract.Command{
		Name:        "在线玩家",
		Description: "查询 Minecraft 服务器在线玩家",
		Usage:       "在线玩家",
		Handler: func(ctx contract.CommandContext) error {
			return handleOnline(ctx, pc, mcServerURL, mcToken)
		},
	})

	return nil
}

// ── 响应结构 ──

type onlineResponse struct {
	Status  bool   `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Online struct {
			Count   int      `json:"count"`
			Players []string `json:"players"`
		} `json:"online"`
	} `json:"data"`
}

// handleOnline 查询 MC 服务器在线玩家。
func handleOnline(ctx contract.CommandContext, pc *contract.PluginContext, mcServerURL, mcToken string) error {
	avatarURL := fmt.Sprintf("https://q.qlogo.cn/qqapp/%s/%s/640", pc.QQAPI.AppID(), ctx.AuthorID())
	if ctx.Scene() != contract.SceneGroup {
		return ctx.Reply("该命令仅支持群聊使用")
	}

	body, err := queryOnline(mcServerURL, mcToken)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("查询在线玩家失败：%v", err))
	}

	var resp onlineResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return ctx.Reply("解析服务器响应失败")
	}

	if !resp.Status {
		return ctx.Reply(fmt.Sprintf("查询失败：%s", resp.Message))
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("![img #30px #30px]("+avatarURL+") | "+contract.MentionUser(ctx.AuthorID())+"\n"+
		"## 在线玩家 (%d)\n", resp.Data.Online.Count))
	if resp.Data.Online.Count == 0 {
		buf.WriteString("\n当前没有玩家在线")
	} else {
		for i, name := range resp.Data.Online.Players {
			cmdText := fmt.Sprintf("在线时长 %s", name)
			buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, contract.CmdInput(cmdText, name, false)))
		}
	}
	return ctx.ReplyMarkdown(buf.String())
}

// queryOnline 向 MC 服务器发送 POST 请求查询在线玩家。
func queryOnline(serverURL, token string) (string, error) {
	body := map[string]string{"action": "query"}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", serverURL+"/api/online", bytes.NewReader(jsonBody))
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
