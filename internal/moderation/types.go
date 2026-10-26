// Package moderation 是审核管线中的纯函数部分：归一化、视图抽取、匹配结果的
// 等级计算。它不碰网络与数据库，因此可以用表驱动测试钉住"判得稳定、可解释"。
//
// 管线的形状：原文 → 归一化 → 视图（raw / acrostic）→ literal | regex。
package moderation

// View 是规则实际进行匹配的那份文本。
type View string

const (
	// ViewRaw 是归一化后的消息本身。
	ViewRaw View = "raw"
	// ViewAcrostic 是抽取出的每句首字。
	ViewAcrostic View = "acrostic"
)

// Views 是全部视图，顺序即匹配顺序（稳定输出用）。
var Views = []View{ViewRaw, ViewAcrostic}

// Valid 报告视图名是否合法。
func (v View) Valid() bool {
	return v == ViewRaw || v == ViewAcrostic
}

// Matcher 是匹配器种类。没有第三种。
type Matcher string

const (
	MatcherLiteral Matcher = "literal"
	MatcherRegex   Matcher = "regex"
)

// Valid 报告匹配器名是否合法。
func (m Matcher) Valid() bool {
	return m == MatcherLiteral || m == MatcherRegex
}

// Rule 是语料库中的一条可取证的违规模式。
type Rule struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	View        View     `json:"view"`
	Matcher     Matcher  `json:"matcher"`
	Pattern     string   `json:"pattern"`
	Severity    int      `json:"severity"`
	Enabled     bool     `json:"enabled"`
	Examples    []string `json:"examples"`
}

// Mapping 是把某一种书写变形归约到标准形式的一条对应关系。
type Mapping struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Note string `json:"note"`
}

// Hit 是一条判定的规则快照：记录"当时"的规则内容，而不是规则 id 的引用
// （ADR 0003）。语料库可变，判定不可变。
type Hit struct {
	RuleID   string `json:"ruleId"`
	Severity int    `json:"severity"`
	Pattern  string `json:"pattern"`
	View     View   `json:"view"`
}

// Verdict 是针对一条消息得出的审核结论。产生后不再修改。
type Verdict struct {
	Hits           []Hit  `json:"hits"`
	Severity       int    `json:"severity"`
	IsViolation    bool   `json:"isViolation"`
	NormalizedText string `json:"normalizedText"`
	RawView        string `json:"rawView"`
	AcrosticView   string `json:"acrosticView"`
}
