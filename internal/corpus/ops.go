package corpus

import (
	"fmt"
	"regexp"
	"strings"

	"chatmod/internal/moderation"
)

// 变更操作的名字。
const (
	OpAdd      = "add"
	OpUpdate   = "update"
	OpDelete   = "delete"
	OpRollback = "rollback"
)

// 变更操作的标的。
const (
	TargetRule    = "rule"
	TargetMapping = "mapping"
	TargetCorpus  = "corpus"
)

// Operation 是变更草案/修订里的一条结构化操作。管理员逐条勾选、逐条编辑 after，
// 因此前后内容都完整携带，而不是文本 diff（技术选型 §20）。
type Operation struct {
	Op     string `json:"op"`
	Target string `json:"target"`

	RuleBefore    *moderation.Rule    `json:"ruleBefore,omitempty"`
	RuleAfter     *moderation.Rule    `json:"ruleAfter,omitempty"`
	MappingBefore *moderation.Mapping `json:"mappingBefore,omitempty"`
	MappingAfter  *moderation.Mapping `json:"mappingAfter,omitempty"`

	Reason  string  `json:"reason"`
	Samples []int64 `json:"samples,omitempty"`
	// FromRevision 仅用于回滚操作：把哪一条历史修订的语料库作为新修订提交。
	FromRevision int64 `json:"fromRevision,omitempty"`
}

// Summary 返回一行可读摘要，用于修订列表。
func (o Operation) Summary() string {
	if o.Op == OpRollback {
		return fmt.Sprintf("回滚语料库到修订 #%d", o.FromRevision)
	}
	name, id, severity := "", "", 0
	switch o.Target {
	case TargetRule:
		r := o.RuleAfter
		if r == nil {
			r = o.RuleBefore
		}
		if r != nil {
			name, id, severity = "规则", r.ID, r.Severity
		}
	case TargetMapping:
		m := o.MappingAfter
		if m == nil {
			m = o.MappingBefore
		}
		if m != nil {
			name, id = "映射", m.ID
		}
	}
	verb := map[string]string{OpAdd: "新增", OpUpdate: "修改", OpDelete: "删除"}[o.Op]
	if verb == "" {
		verb = o.Op
	}
	if name == "" {
		return fmt.Sprintf("%s %s", verb, o.Target)
	}
	if o.Target == TargetRule {
		return fmt.Sprintf("%s%s %s（L%d）", verb, name, id, severity)
	}
	return fmt.Sprintf("%s%s %s", verb, name, id)
}

// Problem 是某一条操作未通过校验的原因。Index 与提交数组下标对齐。
type Problem struct {
	Index    int      `json:"index"`
	Messages []string `json:"messages"`
}

// OK 报告这条操作是否通过校验。
func (p Problem) OK() bool { return len(p.Messages) == 0 }

func ruleOf(op Operation) *moderation.Rule {
	if op.Op == OpDelete {
		if op.RuleBefore != nil {
			return op.RuleBefore
		}
		return op.RuleAfter
	}
	if op.RuleAfter != nil {
		return op.RuleAfter
	}
	return op.RuleBefore
}

func mappingOf(op Operation) *moderation.Mapping {
	if op.Op == OpDelete {
		if op.MappingBefore != nil {
			return op.MappingBefore
		}
		return op.MappingAfter
	}
	if op.MappingAfter != nil {
		return op.MappingAfter
	}
	return op.MappingBefore
}

// ValidateOp 针对当前语料库逐项校验一条操作。返回空切片表示通过。
//
// 校验项（规格 3.8）：severity 1–5 整数、regex 必须编译通过、literal/映射不得为空、
// id 不得冲突、目标必须存在、规则必须能命中自己 examples 中至少一条。
func ValidateOp(cur Corpus, op Operation) []string {
	var msgs []string
	add := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }

	if op.Target == TargetCorpus {
		if op.Op != OpRollback {
			return []string{"语料库级操作只支持 rollback"}
		}
		if op.FromRevision <= 0 {
			return []string{"回滚必须指定来源修订"}
		}
		return nil
	}

	switch op.Op {
	case OpAdd, OpUpdate, OpDelete:
	default:
		return []string{fmt.Sprintf("未知操作 %q", op.Op)}
	}

	switch op.Target {
	case TargetRule:
		r := ruleOf(op)
		if r == nil {
			return []string{"规则操作缺少内容"}
		}
		if strings.TrimSpace(r.ID) == "" {
			add("规则 id 不能为空")
			return msgs
		}
		_, exists := cur.RuleByID(r.ID)
		switch op.Op {
		case OpAdd:
			if exists {
				add("规则 id %q 已存在，不能重复新增", r.ID)
			}
		case OpUpdate:
			if !exists {
				add("规则 %q 不存在，无法修改", r.ID)
			}
			if op.RuleBefore != nil && op.RuleBefore.ID != "" && op.RuleBefore.ID != r.ID {
				add("不允许修改规则 id（%q → %q）", op.RuleBefore.ID, r.ID)
			}
		case OpDelete:
			if !exists {
				add("规则 %q 不存在，无法删除", r.ID)
			}
		}
		if op.Op == OpDelete {
			return msgs
		}
		msgs = append(msgs, validateRuleFields(*r)...)
		if len(msgs) > 0 {
			return msgs
		}
		if !ruleHitsOwnExamples(cur, *r) {
			add("规则 %q 命中不了自己的任何一条 examples（用真实管线验证）", r.ID)
		}
		return msgs

	case TargetMapping:
		m := mappingOf(op)
		if m == nil {
			return []string{"映射操作缺少内容"}
		}
		if strings.TrimSpace(m.ID) == "" {
			add("映射 id 不能为空")
			return msgs
		}
		_, exists := cur.MappingByID(m.ID)
		switch op.Op {
		case OpAdd:
			if exists {
				add("映射 id %q 已存在，不能重复新增", m.ID)
			}
		case OpUpdate:
			if !exists {
				add("映射 %q 不存在，无法修改", m.ID)
			}
			if op.MappingBefore != nil && op.MappingBefore.ID != "" && op.MappingBefore.ID != m.ID {
				add("不允许修改映射 id（%q → %q）", op.MappingBefore.ID, m.ID)
			}
		case OpDelete:
			if !exists {
				add("映射 %q 不存在，无法删除", m.ID)
			}
		}
		if op.Op == OpDelete {
			return msgs
		}
		if strings.TrimSpace(m.From) == "" {
			add("映射的 from 不能为空")
		}
		if strings.TrimSpace(m.To) == "" {
			add("映射的 to 不能为空")
		}
		if m.From == m.To {
			add("映射的 from 与 to 相同，这条映射不会产生任何效果")
		}
		return msgs

	default:
		return []string{fmt.Sprintf("未知标的 %q", op.Target)}
	}
}

func validateRuleFields(r moderation.Rule) []string {
	var msgs []string
	add := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }

	if r.Severity < 1 || r.Severity > 5 {
		add("severity 必须为 1–5 的整数，当前为 %d", r.Severity)
	}
	if !r.View.Valid() {
		add("view 必须为 raw 或 acrostic，当前为 %q", r.View)
	}
	if !r.Matcher.Valid() {
		add("matcher 必须为 literal 或 regex，当前为 %q", r.Matcher)
	}
	switch r.Matcher {
	case moderation.MatcherLiteral:
		if strings.TrimSpace(r.Pattern) == "" {
			add("literal 规则的 pattern 不能为空")
		}
	case moderation.MatcherRegex:
		if strings.TrimSpace(r.Pattern) == "" {
			add("regex 规则的 pattern 不能为空")
		} else if _, err := regexp.Compile(r.Pattern); err != nil {
			add("正则编译失败：%v", err)
		}
	}
	nonEmpty := 0
	for _, ex := range r.Examples {
		if strings.TrimSpace(ex) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		add("规则必须至少带 1 条非空 example")
	}
	return msgs
}

// ruleHitsOwnExamples 用服务端真实管线验证"这条规则真的能命中它自己声称的样例"。
// 校验时强制 enabled = true，否则一条新增的停用规则永远无法自证。
func ruleHitsOwnExamples(cur Corpus, r moderation.Rule) bool {
	probe := r
	probe.Enabled = true
	snap, err := Build(upsertRule(cur, probe))
	if err != nil {
		return false
	}
	for _, ex := range r.Examples {
		if strings.TrimSpace(ex) == "" {
			continue
		}
		if HitsForRule(snap, r.ID, ex) {
			return true
		}
	}
	return false
}

func upsertRule(c Corpus, r moderation.Rule) Corpus {
	out := c.Clone()
	for i := range out.Rules {
		if out.Rules[i].ID == r.ID {
			out.Rules[i] = r
			return out
		}
	}
	out.Rules = append(out.Rules, r)
	return out
}

// ApplyOp 应用一条**已通过校验**的操作。调用方应先跑 ValidateOp。
func ApplyOp(cur Corpus, op Operation) (Corpus, error) {
	if op.Target == TargetCorpus {
		return cur, fmt.Errorf("语料库级操作不能由 ApplyOp 应用")
	}
	out := cur.Clone()
	switch op.Target {
	case TargetRule:
		r := ruleOf(op)
		if r == nil {
			return cur, fmt.Errorf("规则操作缺少内容")
		}
		switch op.Op {
		case OpAdd:
			out.Rules = append(out.Rules, *r)
		case OpUpdate:
			found := false
			for i := range out.Rules {
				if out.Rules[i].ID == r.ID {
					out.Rules[i] = *r
					found = true
					break
				}
			}
			if !found {
				return cur, fmt.Errorf("规则 %q 不存在", r.ID)
			}
		case OpDelete:
			found := false
			for i := range out.Rules {
				if out.Rules[i].ID == r.ID {
					out.Rules = append(out.Rules[:i], out.Rules[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				return cur, fmt.Errorf("规则 %q 不存在", r.ID)
			}
		default:
			return cur, fmt.Errorf("未知操作 %q", op.Op)
		}
	case TargetMapping:
		m := mappingOf(op)
		if m == nil {
			return cur, fmt.Errorf("映射操作缺少内容")
		}
		switch op.Op {
		case OpAdd:
			out.Mappings = append(out.Mappings, *m)
		case OpUpdate:
			found := false
			for i := range out.Mappings {
				if out.Mappings[i].ID == m.ID {
					out.Mappings[i] = *m
					found = true
					break
				}
			}
			if !found {
				return cur, fmt.Errorf("映射 %q 不存在", m.ID)
			}
		case OpDelete:
			found := false
			for i := range out.Mappings {
				if out.Mappings[i].ID == m.ID {
					out.Mappings = append(out.Mappings[:i], out.Mappings[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				return cur, fmt.Errorf("映射 %q 不存在", m.ID)
			}
		default:
			return cur, fmt.Errorf("未知操作 %q", op.Op)
		}
	default:
		return cur, fmt.Errorf("未知标的 %q", op.Target)
	}
	return out, nil
}

// EnrichBefore 用当前语料库补齐 before，使修订记录里"改之前是什么"是服务端权威事实，
// 而不是调用方随手写的一个 id。
func EnrichBefore(c Corpus, ops []Operation) []Operation {
	out := make([]Operation, len(ops))
	copy(out, ops)
	for i := range out {
		op := &out[i]
		if op.Op == OpAdd {
			continue
		}
		switch op.Target {
		case TargetRule:
			r := ruleOf(*op)
			if r == nil {
				continue
			}
			cur, ok := c.RuleByID(r.ID)
			if !ok {
				continue
			}
			cur.Examples = append([]string(nil), cur.Examples...)
			op.RuleBefore = &cur
		case TargetMapping:
			m := mappingOf(*op)
			if m == nil {
				continue
			}
			cur, ok := c.MappingByID(m.ID)
			if !ok {
				continue
			}
			op.MappingBefore = &cur
		}
	}
	return out
}

// CheckOps 按顺序逐条校验，并把通过的操作应用到一个工作副本上，
// 使后续操作的 id 冲突/存在性判断基于"前面的操作也会被提交"这一前提。
// 返回的 Problem 切片与 ops 一一对应（通过的操作 Messages 为空）。
func CheckOps(cur Corpus, ops []Operation) []Problem {
	problems := make([]Problem, len(ops))
	working := cur.Clone()
	for i, op := range ops {
		msgs := ValidateOp(working, op)
		problems[i] = Problem{Index: i, Messages: msgs}
		if len(msgs) > 0 {
			continue
		}
		next, err := ApplyOp(working, op)
		if err != nil {
			problems[i] = Problem{Index: i, Messages: []string{err.Error()}}
			continue
		}
		working = next
	}
	return problems
}

// ApplyChecked 返回"只应用通过校验的那些操作"之后的语料库，以及逐条问题。
// applied 是通过校验的条数。
func ApplyChecked(cur Corpus, ops []Operation) (Corpus, []Problem, int) {
	problems := CheckOps(cur, ops)
	working := cur.Clone()
	applied := 0
	for i, op := range ops {
		if !problems[i].OK() {
			continue
		}
		next, err := ApplyOp(working, op)
		if err != nil {
			problems[i] = Problem{Index: i, Messages: []string{err.Error()}}
			continue
		}
		working = next
		applied++
	}
	return working, problems, applied
}
