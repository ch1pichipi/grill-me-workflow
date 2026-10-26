package moderation

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SortedMappings 返回按应用顺序排好的映射表副本：from 长度降序，等长按 id 升序。
//
// 这是归一化管线的核心不变量。若先应用 `尔 → 你`，`亻尔` 会被拆成 `亻你`，
// 那条映射就永远不生效——而且不会报错。
func SortedMappings(ms []Mapping) []Mapping {
	out := append([]Mapping(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := utf8.RuneCountInString(out[i].From), utf8.RuneCountInString(out[j].From)
		if li != lj {
			return li > lj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Normalize 按给定顺序把映射依次应用到文本上。sorted 必须是 SortedMappings 的结果。
func Normalize(text string, sorted []Mapping) string {
	for _, m := range sorted {
		if m.From == "" {
			continue
		}
		text = strings.ReplaceAll(text, m.From, m.To)
	}
	return text
}

// acrosticDelims 是藏头视图的句子边界：换行 + 中文标点。
var acrosticDelims = map[rune]bool{
	'\n': true,
	'\r': true,
	'。':  true,
	'！':  true,
	'？':  true,
	'；':  true,
	'，':  true,
	'、':  true,
	'…':  true,
}

// Acrostic 抽取藏头视图：按换行与中文标点切句，取每段首个非空白字符。
func Acrostic(text string) string {
	var b strings.Builder
	atStart := true
	for _, r := range text {
		if acrosticDelims[r] {
			atStart = true
			continue
		}
		if atStart && !unicode.IsSpace(r) {
			b.WriteRune(r)
			atStart = false
		}
	}
	return b.String()
}

// BuildViews 返回归一化文本在各视图下的文本。
func BuildViews(normalized string) map[View]string {
	return map[View]string{
		ViewRaw:      normalized,
		ViewAcrostic: Acrostic(normalized),
	}
}

// Severity 计算一条消息的违规等级（ADR 0001）：取命中规则的最高 severity；
// 命中 ≥2 条不同规则时 +1；封顶 5。无命中为 0。
//
// 同一条规则命中多次只算一次（调用方传入的 Hit 已按规则去重，这里再兜一次底）。
func Severity(hits []Hit) int {
	best := 0
	seen := make(map[string]bool, len(hits))
	distinct := 0
	for _, h := range hits {
		if seen[h.RuleID] {
			continue
		}
		seen[h.RuleID] = true
		distinct++
		if h.Severity > best {
			best = h.Severity
		}
	}
	if distinct == 0 {
		return 0
	}
	if distinct >= 2 {
		best++
	}
	if best > 5 {
		best = 5
	}
	return best
}

// Analyze 对一条消息跑完整管线。analyzer 提供语料库侧的归一化与匹配能力，
// 使本函数保持纯函数（不依赖数据库、不在匹配中途换语料库）。
func Analyze(text string, a Analyzer) Verdict {
	normalized := a.Normalize(text)
	views := a.Views(normalized)

	hits := make([]Hit, 0, 4)
	seen := make(map[string]bool, 4)
	for _, v := range Views {
		for _, h := range a.Match(v, views[v]) {
			if seen[h.RuleID] {
				continue
			}
			seen[h.RuleID] = true
			hits = append(hits, h)
		}
	}

	return Verdict{
		Hits:           hits,
		Severity:       Severity(hits),
		IsViolation:    len(hits) > 0,
		NormalizedText: normalized,
		RawView:        views[ViewRaw],
		AcrosticView:   views[ViewAcrostic],
	}
}

// Analyzer 是语料库快照对审核管线暴露的窄接口。
type Analyzer interface {
	Normalize(text string) string
	Views(normalized string) map[View]string
	Match(view View, text string) []Hit
}
