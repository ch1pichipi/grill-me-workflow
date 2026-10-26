// Package corpus 是语料库的深模块：规则与映射的集合、内存快照的编译与匹配、
// 变更操作（add/update/delete）的校验与应用。
//
// 匹配不查数据库：Snapshot 由 atomic.Pointer 持有，提交修订后重建并原子替换，
// 因此不存在重载窗口期，也不会出现"半新半旧"的匹配结果。
package corpus

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"chatmod/internal/moderation"
)

// Corpus 是一份语料库的完整内容（当前版本或某个历史快照）。
type Corpus struct {
	Rules    []moderation.Rule    `json:"rules"`
	Mappings []moderation.Mapping `json:"mappings"`
}

// Clone 返回深拷贝，避免调用方改到快照内部切片。
func (c Corpus) Clone() Corpus {
	out := Corpus{
		Rules:    make([]moderation.Rule, len(c.Rules)),
		Mappings: make([]moderation.Mapping, len(c.Mappings)),
	}
	for i, r := range c.Rules {
		r.Examples = append([]string(nil), r.Examples...)
		out.Rules[i] = r
	}
	copy(out.Mappings, c.Mappings)
	return out
}

// RuleByID 查规则。
func (c Corpus) RuleByID(id string) (moderation.Rule, bool) {
	for _, r := range c.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return moderation.Rule{}, false
}

// MappingByID 查映射。
func (c Corpus) MappingByID(id string) (moderation.Mapping, bool) {
	for _, m := range c.Mappings {
		if m.ID == id {
			return m, true
		}
	}
	return moderation.Mapping{}, false
}

type compiledRule struct {
	rule moderation.Rule
	re   *regexp.Regexp
}

// Snapshot 是语料库的编译产物：排好序的映射表 + 已编译的正则。
type Snapshot struct {
	c        Corpus
	mappings []moderation.Mapping
	rules    []compiledRule
}

// Build 编译一份语料库。任一正则编译失败则整次构建失败——不会出现"半套语料库上线"。
func Build(c Corpus) (*Snapshot, error) {
	c = c.Clone()
	sort.SliceStable(c.Rules, func(i, j int) bool { return c.Rules[i].ID < c.Rules[j].ID })
	sort.SliceStable(c.Mappings, func(i, j int) bool { return c.Mappings[i].ID < c.Mappings[j].ID })

	s := &Snapshot{c: c.Clone(), mappings: moderation.SortedMappings(c.Mappings)}
	for _, r := range c.Rules {
		cr := compiledRule{rule: r}
		if r.Matcher == moderation.MatcherRegex {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				return nil, fmt.Errorf("规则 %s 的正则无法编译: %w", r.ID, err)
			}
			cr.re = re
		}
		s.rules = append(s.rules, cr)
	}
	return s, nil
}

// MustBuild 用于已知合法的语料库（初始化数据、测试）。编译失败会 panic。
func MustBuild(c Corpus) *Snapshot {
	s, err := Build(c)
	if err != nil {
		panic(err)
	}
	return s
}

// Corpus 返回快照对应的语料库副本。
func (s *Snapshot) Corpus() Corpus { return s.c.Clone() }

// Normalize 应用快照内的映射表。
func (s *Snapshot) Normalize(text string) string {
	return moderation.Normalize(text, s.mappings)
}

// Views 返回归一化文本在各视图下的文本。
func (s *Snapshot) Views(normalized string) map[moderation.View]string {
	return moderation.BuildViews(normalized)
}

// Match 返回在指定视图上命中的规则快照。规则只在与自己 view 一致的文本上匹配。
func (s *Snapshot) Match(view moderation.View, text string) []moderation.Hit {
	var hits []moderation.Hit
	for _, cr := range s.rules {
		if !cr.rule.Enabled || cr.rule.View != view {
			continue
		}
		ok := false
		switch cr.rule.Matcher {
		case moderation.MatcherLiteral:
			ok = cr.rule.Pattern != "" && strings.Contains(text, cr.rule.Pattern)
		case moderation.MatcherRegex:
			ok = cr.re != nil && cr.re.MatchString(text)
		}
		if ok {
			hits = append(hits, moderation.Hit{
				RuleID:   cr.rule.ID,
				Severity: cr.rule.Severity,
				Pattern:  cr.rule.Pattern,
				View:     cr.rule.View,
			})
		}
	}
	return hits
}

// Analyze 用本快照跑完整审核管线（单条消息只使用这一个快照版本）。
func (s *Snapshot) Analyze(text string) moderation.Verdict {
	return moderation.Analyze(text, s)
}

// HitsForRule 报告某条规则是否命中给定文本（用真实管线，含归一化与视图抽取）。
func HitsForRule(s *Snapshot, ruleID, text string) bool {
	v := s.Analyze(text)
	for _, h := range v.Hits {
		if h.RuleID == ruleID {
			return true
		}
	}
	return false
}
