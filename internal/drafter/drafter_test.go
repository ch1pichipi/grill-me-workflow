package drafter

import (
	"strings"
	"testing"

	"chatmod/internal/corpus"
	"chatmod/internal/moderation"
)

func TestParseOperationsHappyPath(t *testing.T) {
	body := `{"operations":[
  {"op":"add","target":"rule","reason":"加藏头规则","samples":[12],
   "ruleAfter":{"id":"r010","description":"藏头辱骂","view":"acrostic","matcher":"literal",
                "pattern":"你是傻逼","severity":4,"examples":["你真行\n是个人才\n傻得可爱\n逼样百出"]}},
  {"op":"update","target":"rule","reason":"调重","ruleBefore":{"id":"r001"},
   "ruleAfter":{"id":"r001","description":"广告","view":"raw","matcher":"literal",
                "pattern":"加微信","severity":3,"enabled":false,"examples":["加微信"]}},
  {"op":"delete","target":"rule","reason":"过时","ruleBefore":{"id":"r002"}},
  {"op":"add","target":"mapping","reason":"补火星文","mappingAfter":{"id":"m10","from":"嘚","to":"的","note":"火星文"}},
  {"op":"delete","target":"mapping","reason":"无效映射","mappingBefore":{"id":"m02"}}
]}`
	// 模型偶尔会包一层 Markdown 围栏，解析要能容错。
	raw := "```json\n" + body + "\n```"

	ops, err := ParseOperations(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(ops) != 5 {
		t.Fatalf("应有 5 条操作，实际 %d", len(ops))
	}

	first := ops[0]
	if first.Op != corpus.OpAdd || first.Target != corpus.TargetRule {
		t.Fatalf("第一条不对: %+v", first)
	}
	if first.RuleAfter == nil || first.RuleAfter.Severity != 4 || first.RuleAfter.View != moderation.ViewAcrostic {
		t.Fatalf("规则内容不对: %+v", first.RuleAfter)
	}
	if !first.RuleAfter.Enabled {
		t.Fatal("未显式给出 enabled 时应默认为启用")
	}
	if len(first.Samples) != 1 || first.Samples[0] != 12 {
		t.Fatalf("样本 id 应保留: %+v", first.Samples)
	}

	if ops[1].RuleBefore == nil || ops[1].RuleBefore.ID != "r001" {
		t.Fatalf("update 应带 before: %+v", ops[1].RuleBefore)
	}
	if ops[1].RuleAfter.Enabled {
		t.Fatal("显式 enabled=false 应保留")
	}
	if ops[2].RuleBefore == nil || ops[2].RuleBefore.ID != "r002" {
		t.Fatalf("delete 应带 before: %+v", ops[2].RuleBefore)
	}
	if ops[3].MappingAfter == nil || ops[3].MappingAfter.From != "嘚" {
		t.Fatalf("映射内容不对: %+v", ops[3].MappingAfter)
	}
	if ops[4].MappingBefore == nil || ops[4].MappingBefore.ID != "m02" {
		t.Fatalf("映射删除应带 before: %+v", ops[4].MappingBefore)
	}
}

func TestParseOperationsRejectsBadStructure(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantSubstr string
	}{
		{
			name:       "非法 JSON",
			raw:        `{"operations":[`,
			wantSubstr: "JSON",
		},
		{
			name:       "没有操作",
			raw:        `{"operations":[]}`,
			wantSubstr: "没有提出任何变更",
		},
		{
			name:       "缺少 severity",
			raw:        `{"operations":[{"op":"add","target":"rule","ruleAfter":{"id":"r010","view":"raw","matcher":"literal","pattern":"x","examples":["x"]}}]}`,
			wantSubstr: "severity",
		},
		{
			name:       "缺少 examples",
			raw:        `{"operations":[{"op":"add","target":"rule","ruleAfter":{"id":"r010","view":"raw","matcher":"literal","pattern":"x","severity":2}}]}`,
			wantSubstr: "examples",
		},
		{
			name:       "映射缺少 from",
			raw:        `{"operations":[{"op":"add","target":"mapping","mappingAfter":{"id":"m10","to":"的"}}]}`,
			wantSubstr: "from",
		},
		{
			name:       "未知 op",
			raw:        `{"operations":[{"op":"upsert","target":"rule","ruleAfter":{"id":"r010","view":"raw","matcher":"literal","pattern":"x","severity":2,"examples":["x"]}}]}`,
			wantSubstr: "未知 op",
		},
		{
			name:       "未知 target",
			raw:        `{"operations":[{"op":"add","target":"view","ruleAfter":{"id":"r010"}}]}`,
			wantSubstr: "未知 target",
		},
		{
			name:       "删除规则缺少 id",
			raw:        `{"operations":[{"op":"delete","target":"rule","reason":"x"}]}`,
			wantSubstr: "ruleBefore",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOperations(tc.raw)
			if err == nil {
				t.Fatal("应报错")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("错误应包含 %q，实际: %v", tc.wantSubstr, err)
			}
		})
	}
}

func TestBuildPromptOmitsSamplesWhenNone(t *testing.T) {
	req := Request{
		Instruction: "加一条规则",
		Corpus:      corpus.Corpus{},
	}
	prompt := BuildPrompt(req)
	if !strings.Contains(prompt, "未附带任何聊天消息样本") {
		t.Fatalf("未附带样本时应明确说明: %s", prompt)
	}

	req.Samples = []Sample{{MessageID: 1, Player: "alice", Text: "加微信领皮肤"}}
	prompt = BuildPrompt(req)
	if !strings.Contains(prompt, "加微信领皮肤") {
		t.Fatalf("附带样本时应传入文本: %s", prompt)
	}
}

// 模型在 max_tokens 处戛然而止时，前面完整的条目要能救回来——
// 否则管理员只能对着"JSON 不完整"再等一次往返。
func TestRecoverOperationsSalvagesTruncatedOutput(t *testing.T) {
	truncated := `{"operations":[
{"op":"add","target":"mapping","reason":"去掉@","mappingAfter":{"id":"m90","from":"@","to":"","note":"噪声"}},
{"op":"add","target":"mapping","reason":"去掉#","mappingAfter":{"id":"m91","from":"#","to":"","note":"噪声"}},
{"op":"add","target":"mapping","reason":"去掉`

	if _, err := ParseOperations(truncated); err == nil {
		t.Fatal("严格解析应失败")
	}
	ops, err := RecoverOperations(truncated)
	if err != nil {
		t.Fatalf("应能救回完整条目: %v", err)
	}
	if len(ops) != 2 || ops[0].MappingAfter.ID != "m90" || ops[1].MappingAfter.ID != "m91" {
		t.Fatalf("救回的条目不对: %+v", ops)
	}

	// 没有任何一条完整时不能假装成功。
	if _, err := RecoverOperations(`{"operations":[{"op":"add","target":"mapping","mappingAft`); err == nil {
		t.Fatal("没有完整条目时应报错")
	}
	// 完全不是 JSON 也不能瞎猜。
	if _, err := RecoverOperations("抱歉，我做不到"); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}
