package auditor

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/types"
)

// newTestAuditor builds a minimal Auditor for unit tests.
// Opens /dev/null as the log file so writeEvent doesn't panic.
func newTestAuditor() (*Auditor, *bus.Bus) {
	b := bus.New()
	tap := b.NewTap()
	f, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	a := &Auditor{
		b:                b,
		tap:              tap,
		logPath:          os.DevNull,
		statsPath:        os.DevNull,
		logFile:          f,
		correctionCounts: make(map[string]int),
		replanCounts:     make(map[string]int),
		breakSymCount:    make(map[string]int),
		lastBreakSymD:    make(map[string]float64),
		windowStart:      time.Now().UTC(),
	}
	return a, b
}

func makePlanDirectiveMsg(taskID, directive string, D float64) types.Message {
	return types.Message{
		From: types.RoleGGS,
		To:   types.RolePlanner,
		Type: types.MsgPlanDirective,
		Payload: types.PlanDirective{
			TaskID:    taskID,
			Directive: directive,
			Gradient:  "plateau",
			Loss:      types.LossBreakdown{D: D},
		},
	}
}

func TestDetectGGSThrashing_FiredAfterTwoConsecutiveWithNoDDecrease(t *testing.T) {
	// call process() twice with MsgPlanDirective{break_symmetry, D=0.8}
	// second call should set anomaly="ggs_thrashing"
	a, _ := newTestAuditor()

	a.process(makePlanDirectiveMsg("t1", "break_symmetry", 0.8))
	// first call: count=1, no thrashing yet

	// Capture anomalies after first call
	a.mu.Lock()
	anomaliesBefore := len(a.anomalies)
	a.mu.Unlock()

	a.process(makePlanDirectiveMsg("t1", "break_symmetry", 0.8))
	// second call: D didn't decrease → count=2 ≥ threshold=2 → thrashing

	a.mu.Lock()
	anomaliesAfter := len(a.anomalies)
	a.mu.Unlock()

	if anomaliesAfter <= anomaliesBefore {
		t.Error("expected ggs_thrashing anomaly after two consecutive break_symmetry without D decrease")
	}

	a.mu.Lock()
	found := false
	for _, an := range a.anomalies {
		if strings.HasPrefix(an, "ggs_thrashing") {
			found = true
			break
		}
	}
	a.mu.Unlock()
	if !found {
		t.Errorf("expected ggs_thrashing anomaly in list, got %v", a.anomalies)
	}
}

func TestDetectGGSThrashing_NotFiredWhenDDecreases(t *testing.T) {
	// D=0.8 first call, D=0.4 second call → no thrashing (D improved)
	a, _ := newTestAuditor()

	a.process(makePlanDirectiveMsg("t2", "break_symmetry", 0.8))
	a.process(makePlanDirectiveMsg("t2", "break_symmetry", 0.4))

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, an := range a.anomalies {
		if strings.HasPrefix(an, "ggs_thrashing") {
			t.Errorf("unexpected ggs_thrashing when D decreased: %v", a.anomalies)
		}
	}
}

func TestDetectGGSThrashing_ResetOnNonBreakSymmetryDirective(t *testing.T) {
	// break_symmetry D=0.8 → change_path → break_symmetry D=0.8 again → no thrashing (reset)
	a, _ := newTestAuditor()

	a.process(makePlanDirectiveMsg("t3", "break_symmetry", 0.8))
	a.process(makePlanDirectiveMsg("t3", "change_path", 0.7)) // resets counter
	a.process(makePlanDirectiveMsg("t3", "break_symmetry", 0.8))
	// After reset, count=1 for the third message → below threshold=2

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, an := range a.anomalies {
		if strings.HasPrefix(an, "ggs_thrashing") {
			t.Errorf("unexpected ggs_thrashing after reset via non-break_symmetry directive: %v", a.anomalies)
		}
	}
}
