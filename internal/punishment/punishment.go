// Package punishment 是纯函数包：由违规等级与违规历史算出处置与禁言时长。
//
// 阶梯 [1min, 1h, 1d, 3d] 是本包内的具名常量，前端只读展示（数据从后端读）。
package punishment

import (
	"fmt"
	"time"
)

// Durations 是四个处置档位对应的禁言时长。
var Durations = [4]time.Duration{
	time.Minute,
	time.Hour,
	24 * time.Hour,
	72 * time.Hour,
}

// PermanentLifetimeThreshold 是触发永久禁言的终身违规次数门槛（含本次）。
const PermanentLifetimeThreshold = 5

// Disposition 是判定所导致的动作。
type Disposition string

const (
	DispositionAllow Disposition = "allow"
	DispositionMute  Disposition = "mute"
)

// Decision 是一次处置决定。它不记录任何持久化状态。
type Decision struct {
	Disposition Disposition
	// Rung 是处置档位 0–3；永久禁言时取最高档 3 以便展示。
	Rung      int
	Duration  time.Duration
	Permanent bool
	Severity  int
}

// Rung 计算处置档位：clamp(severity + count7d - 2, 0, 3)。
// count7d 是滚动 7 天内的违规次数，并且**包含本条**。
func Rung(severity, count7d int) int {
	r := severity + count7d - 2
	if r < 0 {
		r = 0
	}
	if r > 3 {
		r = 3
	}
	return r
}

// Decide 由违规等级、滚动 7 天计数、终身计数得出处置。三个计数都包含本次违规。
//
// 永久禁言由终身计数独立触发，优先级高于档位公式：语义上"永久"应该是"屡教不改"，
// 而不是"一次说得很脏"。
func Decide(severity, count7d, lifetime int) Decision {
	if severity <= 0 {
		return Decision{Disposition: DispositionAllow, Rung: 0, Severity: 0}
	}
	if lifetime >= PermanentLifetimeThreshold {
		return Decision{
			Disposition: DispositionMute,
			Rung:        3,
			Duration:    Durations[3],
			Permanent:   true,
			Severity:    severity,
		}
	}
	r := Rung(severity, count7d)
	return Decision{
		Disposition: DispositionMute,
		Rung:        r,
		Duration:    Durations[r],
		Severity:    severity,
	}
}

// RungLabel 返回档位的可读标签，用于界面与判定说明。
func RungLabel(rung int) string {
	if rung < 0 || rung >= len(Durations) {
		return "—"
	}
	return DurationLabel(Durations[rung])
}

// DurationLabel 把时长格式化成中文可读文本。
func DurationLabel(d time.Duration) string {
	switch {
	case d <= 0:
		return "无"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		if d%time.Minute == 0 {
			return fmt.Sprintf("%d 分钟", int(d.Minutes()))
		}
		return fmt.Sprintf("%d 分钟 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		if d%time.Hour == 0 {
			return fmt.Sprintf("%d 小时", int(d.Hours()))
		}
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	default:
		if d%(24*time.Hour) == 0 {
			return fmt.Sprintf("%d 天", int(d.Hours()/24))
		}
		return fmt.Sprintf("%d 天 %d 小时", int(d.Hours()/24), int(d.Hours())%24)
	}
}
