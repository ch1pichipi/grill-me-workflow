package punishment

import (
	"testing"
	"time"
)

// TestDispositionTable 逐行覆盖规格 3.5 的断言表。
func TestDispositionTable(t *testing.T) {
	cases := []struct {
		severity int
		count7d  int
		want     time.Duration
	}{
		{1, 1, time.Minute},
		{1, 2, time.Hour},
		{1, 3, 24 * time.Hour},
		{1, 4, 72 * time.Hour},
		{1, 5, 72 * time.Hour}, // 封顶
		{2, 1, time.Hour},
		{2, 2, 24 * time.Hour},
		{3, 1, 24 * time.Hour},
		{3, 2, 72 * time.Hour},
		{4, 1, 72 * time.Hour},
		{5, 1, 72 * time.Hour},
	}

	for _, tc := range cases {
		// 终身计数保持低于永久门槛（本次不计入 5 次）。
		d := Decide(tc.severity, tc.count7d, 1)
		if d.Disposition != DispositionMute {
			t.Fatalf("severity=%d count7d=%d: disposition = %s, want mute", tc.severity, tc.count7d, d.Disposition)
		}
		if d.Duration != tc.want {
			t.Fatalf("severity=%d count7d=%d: duration = %s, want %s", tc.severity, tc.count7d, d.Duration, tc.want)
		}
		if d.Permanent {
			t.Fatalf("severity=%d count7d=%d: 不应为永久", tc.severity, tc.count7d)
		}
	}
}

func TestRungFormula(t *testing.T) {
	cases := []struct {
		severity, count7d, want int
	}{
		{1, 1, 0},
		{1, 2, 1},
		{1, 3, 2},
		{1, 4, 3},
		{1, 9, 3},
		{2, 1, 1},
		{5, 1, 3},
		{5, 5, 3},
		{1, 0, 0}, // 下界封顶
	}
	for _, tc := range cases {
		if got := Rung(tc.severity, tc.count7d); got != tc.want {
			t.Fatalf("Rung(%d,%d) = %d, want %d", tc.severity, tc.count7d, got, tc.want)
		}
	}
}

func TestPermanentOverridesRung(t *testing.T) {
	// 终身第 4 次：仍按档位。
	d := Decide(1, 1, 4)
	if d.Permanent {
		t.Fatal("终身 4 次不应永久")
	}
	if d.Duration != time.Minute {
		t.Fatalf("终身 4 次、7 天第 1 次、L1 应为 1min，实际 %s", d.Duration)
	}

	// 终身第 5 次：永久，且无视档位公式。
	d = Decide(1, 1, 5)
	if !d.Permanent || d.Disposition != DispositionMute {
		t.Fatal("终身第 5 次必须永久禁言")
	}
	if d.Rung != 3 {
		t.Fatalf("永久禁言展示档位应为 3，实际 %d", d.Rung)
	}

	// 严重度再高，终身未到 5 也不永久。
	if d := Decide(5, 5, 4); d.Permanent {
		t.Fatal("终身未到 5 次不应永久")
	}
}

func TestNoViolationAllows(t *testing.T) {
	d := Decide(0, 0, 0)
	if d.Disposition != DispositionAllow {
		t.Fatalf("severity 0 应放行，实际 %s", d.Disposition)
	}
	if d.Duration != 0 || d.Permanent {
		t.Fatal("放行不应带时长")
	}
}

func TestDurationLabel(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Minute, "1 分钟"},
		{time.Hour, "1 小时"},
		{24 * time.Hour, "1 天"},
		{72 * time.Hour, "3 天"},
		{90 * time.Minute, "1 小时 30 分"},
		{0, "无"},
	}
	for _, tc := range cases {
		if got := DurationLabel(tc.in); got != tc.want {
			t.Fatalf("DurationLabel(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
