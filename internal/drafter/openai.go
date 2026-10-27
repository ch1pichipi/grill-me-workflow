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
	"strconv"
	"strings"
	"time"

	"chatmod/internal/corpus"
)

// 默认的中转站配置（可用环境变量覆盖）。API key 没有内置默认值：
// 必须由环境变量注入，把 AGENT_API_KEY 显式设为空则禁用 agent。
const (
	DefaultAgentName    = "opencode-go"
	DefaultAgentBaseURL = "https://opencode.ai/zen/go/v1"
	DefaultAgentModel   = "deepseek-v4.1-flash"
	// DefaultAgentMaxTokens 给足输出预算：这个模型会先输出很长的 reasoning_content，
	// 8k 的预算在"一次加十几条映射"这种请求上会被思考吃光，正文因此被截断。
	DefaultAgentMaxTokens = 16384
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
	// MaxTokens 是单次输出的 token 预算（<=0 用 DefaultAgentMaxTokens）。
	MaxTokens int

	// OnExchange（可选）在每次往返结束后被调用，用于记录请求体/响应体。
	// agent 调用发生在服务端，浏览器 devtools 的 Network 面板看不到它；
	// 排障需要的请求与响应都走这个口子。
	OnExchange func(Exchange)
}

// ExchangeRecorder 是 Drafter 的可选能力：把每次往返交给回调，用于日志与界面排障。
type ExchangeRecorder interface {
	ObserveExchange(func(Exchange))
}

// ObserveExchange 注册回调（含失败往返）。
func (a *OpenAICompatible) ObserveExchange(f func(Exchange)) { a.OnExchange = f }

// ModelName 暴露模型名，界面上用于提示"请求发给了哪个模型"。
func (a *OpenAICompatible) ModelName() string { return a.Model }

// Exchange 是一次 agent 往返的完整记录。只用于排障，不参与判定、不写库。
type Exchange struct {
	StartedAt   time.Time `json:"startedAt"`
	ElapsedMS   int64     `json:"elapsedMs"`
	URL         string    `json:"url"`
	Model       string    `json:"model"`
	Instruction string    `json:"instruction"`
	Samples     int       `json:"samples"`
	Status      int       `json:"status"`
	Request     string    `json:"request"`
	Response    string    `json:"response"`
	Operations  int       `json:"operations"`
	Error       string    `json:"error,omitempty"`
	// Warning 是"成功但有保留"的说明，例如输出被截断、只恢复了前 N 条。
	Warning string `json:"warning,omitempty"`
}

// NewAgentFromEnv 读取 agent 配置。
//
// API key 只从 AGENT_API_KEY 读取，没有内置默认值：仓库里永远不该出现 key，
// 未注入时 agent 保持禁用（按钮禁用并提示手工编辑语料库）。
func NewAgentFromEnv() *OpenAICompatible {
	c := &OpenAICompatible{
		Name:    strings.TrimSpace(envOr("AGENT_NAME", DefaultAgentName)),
		BaseURL: strings.TrimRight(envOr("AGENT_BASE_URL", DefaultAgentBaseURL), "/"),
		APIKey:  strings.TrimSpace(os.Getenv("AGENT_API_KEY")),
		Model:   envOr("AGENT_MODEL", DefaultAgentModel),
		Session: os.Getenv("AGENT_SESSION"),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
	if v := strings.TrimSpace(os.Getenv("AGENT_MAX_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxTokens = n
		}
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
func (a *OpenAICompatible) Draft(ctx context.Context, req Request) (ops []corpus.Operation, err error) {
	started := time.Now()
	ex := Exchange{
		StartedAt:   started,
		URL:         a.BaseURL + "/chat/completions",
		Model:       a.Model,
		Instruction: req.Instruction,
		Samples:     len(req.Samples),
	}
	maxTokens := a.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultAgentMaxTokens
	}
	defer func() {
		ex.ElapsedMS = time.Since(started).Milliseconds()
		ex.Operations = len(ops)
		if err != nil {
			ex.Error = err.Error()
		}
		if a.OnExchange != nil {
			a.OnExchange(ex)
		}
	}()

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
		MaxTokens:   maxTokens,
		Stream:      false,
	})
	if err != nil {
		return nil, err
	}
	ex.Request = string(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ex.URL, bytes.NewReader(body))
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
	ex.Status = resp.StatusCode

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	ex.Response = string(raw)
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
	truncated := choice.FinishReason == "length"
	if text == "" {
		if truncated {
			return nil, fmt.Errorf("agent 输出被 max_tokens 截断，且没有返回正文，请缩小变更范围后重试")
		}
		return nil, fmt.Errorf("agent 没有返回文本内容")
	}

	ops, err = ParseOperations(text)
	if err == nil {
		return ops, nil
	}
	// 输出在半截处断掉：能救回来的完整条目照旧交给管理员审批，
	// 剩下的让他再点一次生成（比整批作废好得多）。
	if salvaged, serr := RecoverOperations(text); serr == nil {
		if truncated {
			ex.Warning = fmt.Sprintf("输出在 max_tokens 处被截断，只保留了前 %d 条完整建议；把要求拆小或再点一次生成可以继续", len(salvaged))
		} else {
			ex.Warning = fmt.Sprintf("模型输出尾部有损坏，只保留了前 %d 条完整建议", len(salvaged))
		}
		return salvaged, nil
	}
	if truncated {
		return nil, fmt.Errorf("agent 输出在 max_tokens 处被截断，且没有一条建议是完整的；请把要求拆成几步再试")
	}
	return nil, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
