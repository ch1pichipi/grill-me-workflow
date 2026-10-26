package moderation

import "testing"

func TestNormalizeAppliesLongestFirst(t *testing.T) {
	cases := []struct {
		name     string
		mappings []Mapping
		in       string
		want     string
	}{
		{
			name: "亻尔 先于 尔 应用",
			mappings: []Mapping{
				{ID: "m02", From: "尔", To: "你"},
				{ID: "m01", From: "亻尔", To: "你"},
			},
			in:   "亻尔好",
			want: "你好",
		},
		{
			name: "等长按 id 升序应用",
			mappings: []Mapping{
				{ID: "b", From: "BC", To: "Z"},
				{ID: "a", From: "AB", To: "X"},
			},
			in:   "ABC",
			want: "XC",
		},
		{
			name:     "空 from 被跳过",
			mappings: []Mapping{{ID: "m01", From: "", To: "x"}},
			in:       "原样",
			want:     "原样",
		},
		{
			name: "依次应用（前一条的产物可被后一条再处理）",
			mappings: []Mapping{
				{ID: "m01", From: "aa", To: "a"},
				{ID: "m02", From: "ab", To: "b"},
			},
			in:   "aab",
			want: "b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Normalize(tc.in, SortedMappings(tc.mappings))
			if got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAcrostic(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"中文标点切句", "你真傻。今天天气不错。", "你今"},
		{"换行切句", "你真\n是个\n傻逼", "你是傻"},
		{"混合切句", "你真行，是个人才！傻得可爱；逼样百出、……", "你是傻逼"},
		{"跳过段首空白", "  你真\n\t 是", "你是"},
		{"空文本", "", ""},
		{"只有标点", "。。。！！", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Acrostic(tc.in); got != tc.want {
				t.Fatalf("Acrostic(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSeverity(t *testing.T) {
	cases := []struct {
		name string
		hits []Hit
		want int
	}{
		{"无命中", nil, 0},
		{"单条规则取自身等级", []Hit{{RuleID: "r1", Severity: 3}}, 3},
		{"两条不同规则最高值 +1", []Hit{{RuleID: "r1", Severity: 3}, {RuleID: "r2", Severity: 1}}, 4},
		{"同一条规则多次命中不加 1", []Hit{{RuleID: "r1", Severity: 3}, {RuleID: "r1", Severity: 3}}, 3},
		{"封顶 5", []Hit{{RuleID: "r1", Severity: 5}, {RuleID: "r2", Severity: 4}}, 5},
		{"三条以上规则仍封顶 5", []Hit{{RuleID: "a", Severity: 5}, {RuleID: "b", Severity: 5}, {RuleID: "c", Severity: 5}}, 5},
		{"两条规则 L1 变为 2", []Hit{{RuleID: "a", Severity: 1}, {RuleID: "b", Severity: 1}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Severity(tc.hits); got != tc.want {
				t.Fatalf("Severity(%v) = %d, want %d", tc.hits, got, tc.want)
			}
		})
	}
}

// fakeAnalyzer 用固定命中表验证 Analyze 的组装逻辑（视图顺序、去重、快照文本）。
type fakeAnalyzer struct {
	hits map[View][]Hit
	norm string
}

func (f fakeAnalyzer) Normalize(string) string { return f.norm }
func (f fakeAnalyzer) Views(normalized string) map[View]string {
	return BuildViews(normalized)
}
func (f fakeAnalyzer) Match(v View, _ string) []Hit { return f.hits[v] }

func TestAnalyzeUsesOneSnapshotAndDedupes(t *testing.T) {
	a := fakeAnalyzer{
		norm: "归一化后",
		hits: map[View][]Hit{
			ViewRaw:      {{RuleID: "r1", Severity: 2, View: ViewRaw}},
			ViewAcrostic: {{RuleID: "r1", Severity: 2, View: ViewAcrostic}, {RuleID: "r2", Severity: 4, View: ViewAcrostic}},
		},
	}
	v := Analyze("原文", a)
	if v.NormalizedText != "归一化后" {
		t.Fatalf("NormalizedText = %q", v.NormalizedText)
	}
	if len(v.Hits) != 2 {
		t.Fatalf("应去重到 2 条命中，实际 %d", len(v.Hits))
	}
	if v.Severity != 5 {
		t.Fatalf("severity = %d, want 5", v.Severity)
	}
	if !v.IsViolation {
		t.Fatal("有命中时应为违规")
	}
}
