package corpus

import (
	"strings"
	"testing"

	"chatmod/internal/moderation"
)

func baseCorpus() Corpus {
	return Corpus{
		Mappings: []moderation.Mapping{
			{ID: "m01", From: "亻尔", To: "你"},
			{ID: "m02", From: "尔", To: "你"},
		},
		Rules: []moderation.Rule{
			{
				ID: "r001", Description: "加微信广告", View: moderation.ViewRaw,
				Matcher: moderation.MatcherLiteral, Pattern: "加微信", Severity: 2, Enabled: true,
				Examples: []string{"加微信领皮肤"},
			},
			{
				ID: "r002", Description: "辱骂", View: moderation.ViewRaw,
				Matcher: moderation.MatcherLiteral, Pattern: "你是傻逼", Severity: 3, Enabled: true,
				Examples: []string{"亻尔是傻逼"},
			},
			{
				ID: "r003", Description: "藏头辱骂", View: moderation.ViewAcrostic,
				Matcher: moderation.MatcherLiteral, Pattern: "你是傻逼", Severity: 4, Enabled: true,
				Examples: []string{"你真行\n是个人才\n傻得可爱\n逼样百出"},
			},
		},
	}
}

func hitIDs(v moderation.Verdict) []string {
	out := make([]string, 0, len(v.Hits))
	for _, h := range v.Hits {
		out = append(out, h.RuleID)
	}
	return out
}

func TestSnapshotMatching(t *testing.T) {
	c := baseCorpus()
	c.Rules = append(c.Rules, moderation.Rule{
		ID: "r004", Description: "手机号", View: moderation.ViewRaw,
		Matcher: moderation.MatcherRegex, Pattern: `1[3-9]\d{9}`, Severity: 2, Enabled: true,
		Examples: []string{"电话13800138000"},
	})
	c.Rules = append(c.Rules, moderation.Rule{
		ID: "r005", Description: "停用的规则", View: moderation.ViewRaw,
		Matcher: moderation.MatcherLiteral, Pattern: "停用词", Severity: 5, Enabled: false,
		Examples: []string{"停用词"},
	})
	snap := MustBuild(c)

	cases := []struct {
		name string
		text string
		want []string
	}{
		{"literal 子串命中", "低价代充，加微信详谈", []string{"r001"}},
		{"归一化后命中", "亻尔是傻逼", []string{"r002"}},
		{"藏头视图命中", "你真行\n是个人才\n傻得可爱\n逼样百出", []string{"r003"}},
		{"一行连说三句的藏头", "你真傻。今天天气不错。", nil},
		{"raw 规则不在藏头视图上匹配", "你\n是\n傻\n逼", []string{"r003"}},
		{"停用的规则永不命中", "这里有停用词", nil},
		{"regex 命中", "有事打我电话13800138000", []string{"r004"}},
		{"无命中", "今天天气不错", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hitIDs(snap.Analyze(tc.text))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Analyze(%q) hits = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSnapshotSeverityAggregation(t *testing.T) {
	snap := MustBuild(baseCorpus())
	v := snap.Analyze("加微信，你是傻逼")
	if len(v.Hits) != 2 {
		t.Fatalf("应命中 2 条规则，实际 %v", hitIDs(v))
	}
	// max(2,3) + 1 = 4
	if v.Severity != 4 {
		t.Fatalf("severity = %d, want 4", v.Severity)
	}
	if v.NormalizedText != "加微信，你是傻逼" {
		t.Fatalf("归一化文本未被保存: %q", v.NormalizedText)
	}
}

func TestSnapshotSingleRuleCountsOnce(t *testing.T) {
	c := Corpus{Rules: []moderation.Rule{{
		ID: "r001", View: moderation.ViewRaw, Matcher: moderation.MatcherRegex,
		Pattern: "傻", Severity: 3, Enabled: true, Examples: []string{"傻"},
	}}}
	snap := MustBuild(c)
	v := snap.Analyze("傻傻傻")
	if len(v.Hits) != 1 {
		t.Fatalf("同一条规则应只记一次，实际 %v", hitIDs(v))
	}
	if v.Severity != 3 {
		t.Fatalf("severity = %d, want 3", v.Severity)
	}
}

func TestBuildRejectsBadRegex(t *testing.T) {
	c := Corpus{Rules: []moderation.Rule{{
		ID: "bad", View: moderation.ViewRaw, Matcher: moderation.MatcherRegex,
		Pattern: "(", Severity: 1, Enabled: true, Examples: []string{"x"},
	}}}
	if _, err := Build(c); err == nil {
		t.Fatal("非法正则应让整次构建失败（不会出现半套语料库上线）")
	}
}

func TestNormalizeOrderIsVisibleInSnapshot(t *testing.T) {
	c := Corpus{
		Mappings: []moderation.Mapping{
			{ID: "m02", From: "尔", To: "你"},
			{ID: "m01", From: "亻尔", To: "你"},
		},
		Rules: []moderation.Rule{{
			ID: "r001", View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "你好", Severity: 1, Enabled: true, Examples: []string{"亻尔好"},
		}},
	}
	snap := MustBuild(c)
	if got := snap.Normalize("亻尔好"); got != "你好" {
		t.Fatalf("Normalize(亻尔好) = %q, want 你好", got)
	}
	if len(snap.Analyze("亻尔好").Hits) != 1 {
		t.Fatal("归一化后应命中 r001")
	}
}

func TestValidateOp(t *testing.T) {
	base := baseCorpus()

	valid := func() *moderation.Rule {
		return &moderation.Rule{
			ID: "r010", Description: "新规则", View: moderation.ViewRaw,
			Matcher: moderation.MatcherLiteral, Pattern: "刷屏", Severity: 2, Enabled: true,
			Examples: []string{"刷屏了"},
		}
	}

	cases := []struct {
		name       string
		op         Operation
		wantSubstr string // 空表示应通过
	}{
		{
			name: "新增合法规则",
			op:   Operation{Op: OpAdd, Target: TargetRule, RuleAfter: valid()},
		},
		{
			name: "severity 为 0",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Severity = 0
				return r
			}()},
			wantSubstr: "severity 必须为 1–5",
		},
		{
			name: "severity 为 6",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Severity = 6
				return r
			}()},
			wantSubstr: "severity 必须为 1–5",
		},
		{
			name: "regex 编译失败",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Matcher = moderation.MatcherRegex
				r.Pattern = "(["
				r.Examples = []string{"["}
				return r
			}()},
			wantSubstr: "正则编译失败",
		},
		{
			name: "literal pattern 为空",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Pattern = "   "
				return r
			}()},
			wantSubstr: "pattern 不能为空",
		},
		{
			name: "没有 examples",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Examples = nil
				return r
			}()},
			wantSubstr: "至少带 1 条非空 example",
		},
		{
			name: "规则命中不了自己的 examples",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Pattern = "完全不同的词"
				r.Examples = []string{"正常聊天内容"}
				return r
			}()},
			wantSubstr: "命中不了自己的任何一条 examples",
		},
		{
			name: "examples 经归一化后才命中（用真实管线验证）",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Pattern = "你是傻逼"
				r.Examples = []string{"亻尔是傻逼"}
				return r
			}()},
		},
		{
			name: "藏头规则用藏头样例自证",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.View = moderation.ViewAcrostic
				r.Pattern = "你是傻逼"
				r.Examples = []string{"你真行\n是个人才\n傻得可爱\n逼样百出"}
				return r
			}()},
		},
		{
			name: "藏头规则只用首行自证则失败",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.View = moderation.ViewAcrostic
				r.Pattern = "你是傻逼"
				r.Examples = []string{"你真行"}
				return r
			}()},
			wantSubstr: "命中不了自己的任何一条 examples",
		},
		{
			name: "view 非法",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.View = "acrostic-line"
				return r
			}()},
			wantSubstr: "view 必须为 raw 或 acrostic",
		},
		{
			name: "matcher 非法",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.Matcher = "sequence"
				return r
			}()},
			wantSubstr: "matcher 必须为 literal 或 regex",
		},
		{
			name: "新增时 id 冲突",
			op: Operation{Op: OpAdd, Target: TargetRule, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.ID = "r001"
				return r
			}()},
			wantSubstr: "已存在",
		},
		{
			name: "修改不存在的规则",
			op: Operation{Op: OpUpdate, Target: TargetRule, RuleBefore: &moderation.Rule{ID: "r999"}, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.ID = "r999"
				return r
			}()},
			wantSubstr: "不存在",
		},
		{
			name:       "删除不存在的规则",
			op:         Operation{Op: OpDelete, Target: TargetRule, RuleBefore: &moderation.Rule{ID: "r999"}},
			wantSubstr: "不存在",
		},
		{
			name: "不允许改规则 id",
			op: Operation{Op: OpUpdate, Target: TargetRule, RuleBefore: &moderation.Rule{ID: "r001"}, RuleAfter: func() *moderation.Rule {
				r := valid()
				r.ID = "r011"
				return r
			}()},
			wantSubstr: "不允许修改规则 id",
		},
		{
			name:       "映射 from 为空",
			op:         Operation{Op: OpAdd, Target: TargetMapping, MappingAfter: &moderation.Mapping{ID: "m10", From: "", To: "你"}},
			wantSubstr: "from 不能为空",
		},
		{
			name:       "映射 from 与 to 相同",
			op:         Operation{Op: OpAdd, Target: TargetMapping, MappingAfter: &moderation.Mapping{ID: "m10", From: "尔", To: "尔"}},
			wantSubstr: "不会产生任何效果",
		},
		{
			name:       "新增映射 id 冲突",
			op:         Operation{Op: OpAdd, Target: TargetMapping, MappingAfter: &moderation.Mapping{ID: "m01", From: "x", To: "y"}},
			wantSubstr: "已存在",
		},
		{
			name:       "删除不存在的映射",
			op:         Operation{Op: OpDelete, Target: TargetMapping, MappingBefore: &moderation.Mapping{ID: "m99"}},
			wantSubstr: "不存在",
		},
		{
			name: "新增合法映射",
			op:   Operation{Op: OpAdd, Target: TargetMapping, MappingAfter: &moderation.Mapping{ID: "m10", From: "嘚", To: "的"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := ValidateOp(base, tc.op)
			if tc.wantSubstr == "" {
				if len(msgs) > 0 {
					t.Fatalf("应通过校验，实际: %v", msgs)
				}
				return
			}
			joined := strings.Join(msgs, " | ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("期望错误包含 %q，实际: %v", tc.wantSubstr, msgs)
			}
		})
	}
}

func TestCheckOpsIsSequential(t *testing.T) {
	base := baseCorpus()
	rule := func(id string) *moderation.Rule {
		return &moderation.Rule{
			ID: id, View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "同一个词", Severity: 1, Enabled: true, Examples: []string{"同一个词"},
		}
	}
	ops := []Operation{
		{Op: OpAdd, Target: TargetRule, RuleAfter: rule("r020")},
		{Op: OpAdd, Target: TargetRule, RuleAfter: rule("r020")},
	}
	problems := CheckOps(base, ops)
	if !problems[0].OK() {
		t.Fatalf("第一条应通过: %v", problems[0].Messages)
	}
	if problems[1].OK() {
		t.Fatal("同一批次内重复 id 应被第二条抓住")
	}
}

func TestApplyCheckedSkipsInvalid(t *testing.T) {
	base := baseCorpus()
	ops := []Operation{
		{Op: OpAdd, Target: TargetRule, RuleAfter: &moderation.Rule{
			ID: "r030", View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "刷屏", Severity: 2, Enabled: true, Examples: []string{"刷屏"},
		}},
		{Op: OpAdd, Target: TargetRule, RuleAfter: &moderation.Rule{
			ID: "r031", View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "不存在的词", Severity: 2, Enabled: true, Examples: []string{"正常内容"},
		}},
	}
	next, problems, applied := ApplyChecked(base, ops)
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if problems[1].OK() {
		t.Fatal("第二条应校验失败")
	}
	if _, ok := next.RuleByID("r030"); !ok {
		t.Fatal("通过校验的操作应被应用")
	}
	if _, ok := next.RuleByID("r031"); ok {
		t.Fatal("未通过校验的操作不应被应用")
	}
}

// 删除噪声符号只能靠"to 留空"的映射表达，必须算合法操作。
func TestValidateEmptyToMappingIsDeletion(t *testing.T) {
	cur := Corpus{}
	op := Operation{Op: OpAdd, Target: TargetMapping, MappingAfter: &moderation.Mapping{ID: "m06", From: "@", To: "", Note: "去噪"}}
	if problems := ValidateOp(cur, op); len(problems) != 0 {
		t.Fatalf("to 留空应合法（表示删除），实际: %v", problems)
	}
}
