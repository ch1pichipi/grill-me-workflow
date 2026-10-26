package drafter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatmod/internal/corpus"
)

const draftJSON = `{"operations":[
  {"op":"add","target":"rule","reason":"加藏头规则",
   "ruleAfter":{"id":"r010","description":"藏头辱骂","view":"acrostic","matcher":"literal",
                "pattern":"你是傻逼","severity":4,"examples":["你真行\n是个人才\n傻得可爱\n逼样百出"]}}
]}`

func TestOpenAICompatibleDraft(t *testing.T) {
	var (
		gotPath, gotAuth, gotSession, gotModel, gotPrompt string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSession = r.Header.Get("x-opencode-session")

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		gotModel = req.Model
		if len(req.Messages) == 2 {
			gotPrompt = req.Messages[1].Content
		}

		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message": map[string]any{
					"content":           draftJSON,
					"reasoning_content": "先看看语料库……",
				},
			}},
		})
	}))
	defer srv.Close()

	agent := &OpenAICompatible{
		Name:    "opencode-go",
		BaseURL: srv.URL + "/v1",
		APIKey:  "test-key",
		Model:   DefaultAgentModel,
		Session: "chatmod-test-session",
		HTTP:    srv.Client(),
	}

	ops, err := agent.Draft(context.Background(), Request{
		Instruction: "加一条藏头规则",
		Corpus:      corpus.Corpus{},
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if len(ops) != 1 || ops[0].RuleAfter == nil || ops[0].RuleAfter.ID != "r010" {
		t.Fatalf("解析结果不对: %+v", ops)
	}

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("请求路径 = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotSession != "chatmod-test-session" {
		t.Fatalf("x-opencode-session = %q", gotSession)
	}
	if gotModel != DefaultAgentModel {
		t.Fatalf("model = %q", gotModel)
	}
	if !strings.Contains(gotPrompt, "加一条藏头规则") {
		t.Fatalf("prompt 应包含管理员指令: %s", gotPrompt)
	}
}

func TestOpenAICompatibleErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantSubstr string
	}{
		{
			name:       "非 200",
			status:     http.StatusBadRequest,
			body:       `{"error":{"type":"MissingSessionID","message":"request is missing x-opencode-session"}}`,
			wantSubstr: "400",
		},
		{
			name:       "被 max_tokens 截断",
			status:     http.StatusOK,
			body:       `{"choices":[{"finish_reason":"length","message":{"content":"","reasoning_content":"想了很久"}}]}`,
			wantSubstr: "截断",
		},
		{
			name:       "没有内容",
			status:     http.StatusOK,
			body:       `{"choices":[{"finish_reason":"stop","message":{"content":"   "}}]}`,
			wantSubstr: "没有返回文本内容",
		},
		{
			name:       "没有候选",
			status:     http.StatusOK,
			body:       `{"choices":[]}`,
			wantSubstr: "候选",
		},
		{
			name:       "返回了非 JSON 文本",
			status:     http.StatusOK,
			body:       `{"choices":[{"finish_reason":"stop","message":{"content":"抱歉，我做不到"}}]}`,
			wantSubstr: "JSON",
		},
		{
			name:       "业务报错",
			status:     http.StatusOK,
			body:       `{"error":{"type":"quota","message":"余额不足"}}`,
			wantSubstr: "余额不足",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			agent := &OpenAICompatible{
				BaseURL: srv.URL,
				APIKey:  "k",
				Model:   "m",
				HTTP:    srv.Client(),
			}
			_, err := agent.Draft(context.Background(), Request{Instruction: "x"})
			if err == nil {
				t.Fatal("应报错")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("错误应包含 %q，实际: %v", tc.wantSubstr, err)
			}
		})
	}
}

func TestNewAgentFromEnv(t *testing.T) {
	t.Setenv("AGENT_BASE_URL", "https://example.test/v1/")
	t.Setenv("AGENT_API_KEY", "k-123")
	t.Setenv("AGENT_MODEL", "some-model")
	t.Setenv("AGENT_SESSION", "")

	agent := NewAgentFromEnv()
	if agent.BaseURL != "https://example.test/v1" {
		t.Fatalf("base url 应去掉尾部斜杠: %q", agent.BaseURL)
	}
	if agent.APIKey != "k-123" || agent.Model != "some-model" {
		t.Fatalf("环境变量覆盖失败: %+v", agent)
	}
	if !agent.Enabled() {
		t.Fatal("配了 key 应启用")
	}
	if !strings.HasPrefix(agent.Session, "chatmod-") {
		t.Fatalf("未指定 session 时应自动生成: %q", agent.Session)
	}

	// 显式置空 key 表示禁用 agent。
	t.Setenv("AGENT_API_KEY", "")
	if NewAgentFromEnv().Enabled() {
		t.Fatal("显式置空 AGENT_API_KEY 应禁用 agent")
	}
}

// 截断仍然算"部分成功"：返回能救回的完整条目，并在 Exchange 里留下警告。
func TestOpenAICompatibleDraftSalvagesTruncatedOutput(t *testing.T) {
	truncated := `{"operations":[
{"op":"add","target":"mapping","reason":"去掉@","mappingAfter":{"id":"m90","from":"@","to":""}},
{"op":"add","target":"mapping","reason":"去掉#","mappingAfter":{"id":"m91","from":"#","to":""}},
{"op":"add","target":"mapping","reason":"去`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "length",
				"message":       map[string]any{"content": truncated},
			}},
		})
	}))
	defer srv.Close()

	var got Exchange
	agent := &OpenAICompatible{
		BaseURL: srv.URL,
		APIKey:  "k",
		Model:   "m",
		HTTP:    srv.Client(),
		OnExchange: func(ex Exchange) {
			got = ex
		},
	}
	ops, err := agent.Draft(context.Background(), Request{Instruction: "去掉符号"})
	if err != nil {
		t.Fatalf("截断时应返回救回的条目而不是报错: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("应救回 2 条，实际 %d: %+v", len(ops), ops)
	}
	if !strings.Contains(got.Warning, "截断") || !strings.Contains(got.Warning, "2") {
		t.Fatalf("Exchange 应带上截断警告: %+v", got.Warning)
	}
	if got.Error != "" {
		t.Fatalf("救回条目时不应记为失败: %q", got.Error)
	}
}

// 请求体里要带够输出预算（这个模型会先用大量 token 思考）。
func TestOpenAICompatibleSendsMaxTokensBudget(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":`+
			`"{\"operations\":[]}"}}]}`)
	}))
	defer srv.Close()

	agent := &OpenAICompatible{BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client()}
	if _, err := agent.Draft(context.Background(), Request{Instruction: "x"}); err == nil {
		t.Fatal("空操作应报错")
	}
	if got.MaxTokens != DefaultAgentMaxTokens || DefaultAgentMaxTokens < 16384 {
		t.Fatalf("max_tokens = %d，默认预算过小会被思考吃光", got.MaxTokens)
	}
}
