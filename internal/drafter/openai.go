package drafter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"chatmod/internal/corpus"
)

// 默认的中转站配置（可用环境变量覆盖；把 AGENT_API_KEY 显式设为空则禁用 agent）。
const (
	DefaultAgentName    = "opencode-go"
	DefaultAgentBaseURL = "https://opencode.ai/zen/go/v1"
	DefaultAgentAPIKey  = "oc_sk_43205309b565_-jhE0mv1gzfQiTwxCQZAmLj1CfNEucrW"
	DefaultAgentModel   = "deepseek-v4.1-flash"
)

// OpenAICompatible 是一个 OpenAI 兼容的中转站实现：POST {base}/chat/completions。
// 换 provider 就是换 base/model 或加一个 Drafter 实现（技术选型 §28）。
type OpenAICompatible struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	// Session 会作为 x-opencode-session 头发出（部分中转站用它做路由）。
	Session string
	HTTP    *http.Client
}

// NewAgentFromEnv 读取 agent 配置。环境变量未设置时使用上面的默认值；
// 显式设置 AGENT_API_KEY="" 表示禁用 agent（按钮禁用并提示手工编辑语料库）。
func NewAgentFromEnv() *OpenAICompatible {
	c := &OpenAICompatible{
		Name:    strings.TrimSpace(envOr("AGENT_NAME", DefaultAgentName)),
		BaseURL: strings.TrimRight(envOr("AGENT_BASE_URL", DefaultAgentBaseURL), "/"),
		APIKey:  DefaultAgentAPIKey,
		Model:   envOr("AGENT_MODEL", DefaultAgentModel),
		Session: os.Getenv("AGENT_SESSION"),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
	if v, ok := os.LookupEnv("AGENT_API_KEY"); ok {
		c.APIKey = strings.TrimSpace(v)
	}
	if c.Session == "" {
		c.Session = "chatmod-" + randomHex(8)
	}
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "local"
	}
	return hex.EncodeToString(buf)
}

// Enabled 报告是否配置了 key。
func (a *OpenAICompatible) Enabled() bool { return a != nil && a.APIKey != "" }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Draft 调一次中转站并把输出解析成结构化操作。
func (a *OpenAICompatible) Draft(ctx context.Context, req Request) ([]corpus.Operation, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("未配置 AGENT_API_KEY")
	}

	body, err := json.Marshal(chatRequest{
		Model: a.Model,
		Messages: []chatMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: BuildPrompt(req)},
		},
		Temperature: 0,
		MaxTokens:   8192,
		Stream:      false,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+a.APIKey)
	if a.Session != "" {
		httpReq.Header.Set("x-opencode-session", a.Session)
	}

	resp, err := a.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用 agent 中转站失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("中转站返回 %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("中转站响应不是合法 JSON: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("中转站报错: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("中转站没有返回任何候选结果")
	}

	choice := parsed.Choices[0]
	text := strings.TrimSpace(choice.Message.Content)
	if text == "" {
		if choice.FinishReason == "length" {
			return nil, fmt.Errorf("agent 输出被 max_tokens 截断，请缩小变更范围后重试")
		}
		return nil, fmt.Errorf("agent 没有返回文本内容")
	}
	return ParseOperations(text)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
