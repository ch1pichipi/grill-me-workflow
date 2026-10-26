// Package drafter 是"谁来提出语料库变更"的边界。agent 只产出变更草案，
// 永不写库（ADR 0002）。
package drafter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"chatmod/internal/corpus"
	"chatmod/internal/moderation"
)

// Sample 是一条可以给 agent 看的消息样本（含判定结论）。
type Sample struct {
	MessageID   int64            `json:"messageId"`
	Player      string           `json:"player"`
	Text        string           `json:"text"`
	Severity    int              `json:"severity"`
	Disposition string           `json:"disposition"`
	Hits        []moderation.Hit `json:"hits,omitempty"`
}

// Request 是 agent 的输入边界：当前语料库 + 管理员指令，样本是可选项。
type Request struct {
	Instruction string
	Corpus      corpus.Corpus
	Samples     []Sample
}

// Drafter 只做一件事：把自然语言要求变成结构化操作列表。
type Drafter interface {
	// Enabled 报告这个实现是否可用（无 API key 时为 false，前端禁用按钮）。
	Enabled() bool
	Draft(ctx context.Context, req Request) ([]corpus.Operation, error)
}

// SystemPrompt 约束 agent 的输出形态与三条硬约束。
const SystemPrompt = `你是一个聊天审核系统的语料库助手。你只能提出"变更草案"，永远不会直接修改语料库。

审核管线：原文 → 归一化（映射表） → 视图（raw / acrostic） → literal 或 regex。
- view = "raw"：在归一化后的整条消息上匹配。
- view = "acrostic"：按换行与中文标点（。！？；，、…）切句，取每段首个非空白字符后匹配。
- matcher = "literal" 是子串匹配；"regex" 是 Go/RE2 正则。
- severity 是 1–5 的整数。examples 是这条规则必须能命中的示例文本（至少 1 条），
  必须用你自己的规则真正命中，否则服务端会拒绝这条建议。
- 每条规则的 examples 必须真实可命中：若用 acrostic 视图，example 的藏头必须等于 pattern
  （literal）或能被 pattern 正则匹配。

只输出一个 JSON 对象，不要输出解释文字、不要用 Markdown 代码块。结构：

{"operations":[
  {"op":"add","target":"rule","reason":"为什么加这条","samples":[12,15],
   "ruleAfter":{"id":"r010","description":"一句话说明","view":"raw","matcher":"literal",
                "pattern":"加微信","severity":2,"enabled":true,"examples":["加微信领皮肤"]}},
  {"op":"update","target":"rule","reason":"...","ruleBefore":{"id":"r003"}, "ruleAfter":{ ...完整规则... }},
  {"op":"delete","target":"rule","reason":"...","ruleBefore":{"id":"r003"}},
  {"op":"add","target":"mapping","reason":"...","mappingAfter":{"id":"m10","from":"嘚","to":"的","note":"火星文"}},
  {"op":"update","target":"mapping","reason":"...","mappingBefore":{"id":"m10"},"mappingAfter":{...完整映射...}},
  {"op":"delete","target":"mapping","reason":"...","mappingBefore":{"id":"m10"}}
]}

规则 id 用形如 r010 的人可读短 id，映射 id 用 m10 的形式，且不要与当前语料库冲突。
samples 只填你实际依据的消息 id；没有附带样本时不要填 samples。`

// BuildPrompt 拼出 user 消息。
func BuildPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("当前语料库（JSON）：\n")
	cur, err := json.MarshalIndent(req.Corpus, "", "  ")
	if err == nil {
		b.Write(cur)
	}
	b.WriteString("\n\n管理员的要求：\n")
	b.WriteString(strings.TrimSpace(req.Instruction))
	if len(req.Samples) > 0 {
		b.WriteString("\n\n最近消息样本（含已有判定结论，JSON）：\n")
		if s, err := json.MarshalIndent(req.Samples, "", "  "); err == nil {
			b.Write(s)
		}
	} else {
		b.WriteString("\n\n（本次未附带任何聊天消息样本，请不要引用 samples。）")
	}
	return b.String()
}

// rawOperation 用于严格检查结构：缺字段要报错，而不是悄悄变成零值。
type rawOperation struct {
	Op            string          `json:"op"`
	Target        string          `json:"target"`
	Reason        string          `json:"reason"`
	Samples       []int64         `json:"samples"`
	RuleBefore    json.RawMessage `json:"ruleBefore"`
	RuleAfter     json.RawMessage `json:"ruleAfter"`
	MappingBefore json.RawMessage `json:"mappingBefore"`
	MappingAfter  json.RawMessage `json:"mappingAfter"`
}

type rulePatch struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	View        string   `json:"view"`
	Matcher     string   `json:"matcher"`
	Pattern     string   `json:"pattern"`
	Severity    *int     `json:"severity"`
	Enabled     *bool    `json:"enabled"`
	Examples    []string `json:"examples"`
}

type mappingPatch struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Note string `json:"note"`
}

// ParseOperations 解析模型输出并做结构校验。非法 JSON 或缺少必需字段
// （如 severity）时不产生草案，返回明确错误。
func ParseOperations(output string) ([]corpus.Operation, error) {
	body, err := extractJSONObject(output)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("模型返回的不是合法 JSON：%w", err)
	}
	if len(envelope.Operations) == 0 {
		return nil, fmt.Errorf("模型没有提出任何变更（原始输出：%s）", snippet(output, 400))
	}

	ops := make([]corpus.Operation, 0, len(envelope.Operations))
	for i, raw := range envelope.Operations {
		op, err := parseOperation(raw)
		if err != nil {
			return nil, fmt.Errorf("第 %d 条建议不合法：%w", i+1, err)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func parseOperation(raw json.RawMessage) (corpus.Operation, error) {
	var r rawOperation
	if err := json.Unmarshal(raw, &r); err != nil {
		return corpus.Operation{}, err
	}
	op := corpus.Operation{
		Op:      strings.TrimSpace(r.Op),
		Target:  strings.TrimSpace(r.Target),
		Reason:  r.Reason,
		Samples: r.Samples,
	}
	switch op.Op {
	case corpus.OpAdd, corpus.OpUpdate, corpus.OpDelete:
	default:
		return corpus.Operation{}, fmt.Errorf("未知 op %q", op.Op)
	}

	switch op.Target {
	case corpus.TargetRule:
		if op.Op == corpus.OpDelete {
			id, err := requiredID(r.RuleBefore)
			if err != nil {
				return corpus.Operation{}, fmt.Errorf("删除规则需要 ruleBefore.id：%w", err)
			}
			op.RuleBefore = &moderation.Rule{ID: id}
			return op, nil
		}
		rule, err := parseRule(r.RuleAfter)
		if err != nil {
			return corpus.Operation{}, err
		}
		op.RuleAfter = rule
		if op.Op == corpus.OpUpdate {
			if id, err := requiredID(r.RuleBefore); err == nil {
				op.RuleBefore = &moderation.Rule{ID: id}
			} else {
				op.RuleBefore = &moderation.Rule{ID: rule.ID}
			}
		}
		return op, nil

	case corpus.TargetMapping:
		if op.Op == corpus.OpDelete {
			id, err := requiredID(r.MappingBefore)
			if err != nil {
				return corpus.Operation{}, fmt.Errorf("删除映射需要 mappingBefore.id：%w", err)
			}
			op.MappingBefore = &moderation.Mapping{ID: id}
			return op, nil
		}
		m, err := parseMapping(r.MappingAfter)
		if err != nil {
			return corpus.Operation{}, err
		}
		op.MappingAfter = m
		if op.Op == corpus.OpUpdate {
			op.MappingBefore = &moderation.Mapping{ID: m.ID}
		}
		return op, nil

	default:
		return corpus.Operation{}, fmt.Errorf("未知 target %q", op.Target)
	}
}

func requiredID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("缺少对象")
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.ID) == "" {
		return "", fmt.Errorf("id 不能为空")
	}
	return strings.TrimSpace(p.ID), nil
}

func parseRule(raw json.RawMessage) (*moderation.Rule, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("缺少 ruleAfter")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}
	for _, k := range []string{"id", "view", "matcher", "pattern", "severity", "examples"} {
		if _, ok := keys[k]; !ok {
			return nil, fmt.Errorf("规则缺少必需字段 %q", k)
		}
	}
	var p rulePatch
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Severity == nil {
		return nil, fmt.Errorf("规则缺少必需字段 \"severity\"")
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	return &moderation.Rule{
		ID:          strings.TrimSpace(p.ID),
		Description: p.Description,
		View:        moderation.View(strings.TrimSpace(p.View)),
		Matcher:     moderation.Matcher(strings.TrimSpace(p.Matcher)),
		Pattern:     p.Pattern,
		Severity:    *p.Severity,
		Enabled:     enabled,
		Examples:    p.Examples,
	}, nil
}

func parseMapping(raw json.RawMessage) (*moderation.Mapping, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("缺少 mappingAfter")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}
	for _, k := range []string{"id", "from", "to"} {
		if _, ok := keys[k]; !ok {
			return nil, fmt.Errorf("映射缺少必需字段 %q", k)
		}
	}
	var p mappingPatch
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &moderation.Mapping{
		ID:   strings.TrimSpace(p.ID),
		From: p.From,
		To:   p.To,
		Note: p.Note,
	}, nil
}

// extractJSONObject 容错地从模型输出里抠出第一个完整的 JSON 对象
// （模型偶尔会加一句解释或 Markdown 代码围栏）。
func snippet(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "（空）"
	}
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func extractJSONObject(s string) ([]byte, error) {
	start := strings.Index(s, "{")
	if start < 0 {
		return nil, fmt.Errorf("模型输出中没有 JSON 对象")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("模型输出的 JSON 不完整")
}

// Fake 是测试用的 Drafter。
type Fake struct {
	Ops []corpus.Operation
	Err error
	// Requests 记录收到的请求，便于断言"未勾选时不传消息内容"。
	Requests []Request
}

// Enabled 恒为 true。
func (f *Fake) Enabled() bool { return true }

// Draft 返回预置操作。
func (f *Fake) Draft(_ context.Context, req Request) ([]corpus.Operation, error) {
	f.Requests = append(f.Requests, req)
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Ops, nil
}
