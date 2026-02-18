package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/haricheung/agentic-shell/internal/types"
)

// ANSI codes
const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiCyan    = "\033[36m"
	ansiYellow  = "\033[33m"
	ansiGreen   = "\033[32m"
	ansiRed     = "\033[31m"
	ansiMagenta = "\033[35m"
	ansiBlue    = "\033[34m"
)

var roleEmoji = map[types.Role]string{
	types.RolePerceiver: "🧠",
	types.RolePlanner:   "📐",
	types.RoleExecutor:  "⚙️ ",
	types.RoleAgentVal:  "🔍",
	types.RoleMetaVal:   "🔮",
	types.RoleMemory:    "💾",
	types.RoleAuditor:   "📡",
	types.RoleUser:      "👤",
}

var msgColor = map[types.MessageType]string{
	types.MsgTaskSpec:         ansiCyan,
	types.MsgSubTask:          ansiBlue,
	types.MsgDispatchManifest: ansiDim + ansiBlue,
	types.MsgExecutionResult:  ansiYellow,
	types.MsgCorrectionSignal: ansiRed,
	types.MsgSubTaskOutcome:   ansiMagenta,
	types.MsgReplanRequest:    ansiRed,
	types.MsgMemoryWrite:      ansiDim,
	types.MsgMemoryRead:       ansiDim,
	types.MsgMemoryResponse:   ansiDim,
	types.MsgFinalResult:      ansiGreen,
}

var msgStatus = map[types.MessageType]string{
	types.MsgTaskSpec:         "🧠 perceiving...",
	types.MsgSubTask:          "📐 scheduling subtasks...",
	types.MsgDispatchManifest: "📐 dispatching...",
	types.MsgExecutionResult:  "🔍 evaluating result...",
	types.MsgCorrectionSignal: "⚙️  retrying...",
	types.MsgSubTaskOutcome:   "🔮 evaluating outcomes...",
	types.MsgReplanRequest:    "🔮 replanning...",
	types.MsgMemoryWrite:      "💾 saving memory...",
	types.MsgMemoryRead:       "💾 recalling...",
	types.MsgMemoryResponse:   "📐 planning...",
}

// dynamicStatus returns a spinner label for msg, enriched with payload detail
// for message types where the static label alone is not informative enough.
func dynamicStatus(msg types.Message) string {
	switch msg.Type {
	case types.MsgCorrectionSignal:
		var c types.CorrectionSignal
		if remarshal(msg.Payload, &c) == nil && c.WhatToDo != "" {
			return fmt.Sprintf("⚙️  retry %d — %s", c.AttemptNumber, clip(c.WhatToDo, 55))
		}
	case types.MsgSubTaskOutcome:
		var o types.SubTaskOutcome
		if remarshal(msg.Payload, &o) == nil {
			switch o.Status {
			case "matched":
				return "🔮 subtask matched — merging..."
			case "failed":
				return "🔮 subtask failed — assessing..."
			}
		}
	}
	if s := msgStatus[msg.Type]; s != "" {
		return s
	}
	return ""
}

var spinRunes = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Display renders a sci-fi inter-role flow visualization to stdout.
// It reads from a bus tap channel and animates a live pipeline view.
type Display struct {
	tap        <-chan types.Message
	abortCh    chan struct{}
	resumeCh   chan struct{}
	mu         sync.Mutex
	status     string
	started    time.Time
	inTask     bool
	spinIdx    int
	suppressed bool // true after Abort(); blocks new pipeline boxes until Resume()
}

// New creates a Display reading from tap.
func New(tap <-chan types.Message) *Display {
	return &Display{tap: tap, abortCh: make(chan struct{}, 1), resumeCh: make(chan struct{}, 1)}
}

// Abort signals the display to immediately close the current pipeline box
// and suppress any subsequent stale messages until Resume() is called.
// Safe to call from any goroutine.
func (d *Display) Abort() {
	select {
	case d.abortCh <- struct{}{}:
	default:
	}
}

// Resume lifts the post-abort suppression so the next task can open a pipeline box.
// Call this right before starting a new perceiver/task.
// Safe to call from any goroutine.
func (d *Display) Resume() {
	select {
	case d.resumeCh <- struct{}{}:
	default:
	}
}

// Run is the main goroutine. It renders flow lines and animates the spinner.
// Safe to run concurrently with other goroutines; all terminal writes are
// within this single goroutine so no extra locking is needed for I/O.
func (d *Display) Run(ctx context.Context) {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Print("\r\033[K")
			return

		case <-d.abortCh:
			if d.inTask {
				fmt.Print("\r\033[K")
				d.endTask(false)
			}
			d.mu.Lock()
			d.suppressed = true
			d.mu.Unlock()

		case <-d.resumeCh:
			d.mu.Lock()
			d.suppressed = false
			d.mu.Unlock()

		case msg, ok := <-d.tap:
			if !ok {
				return
			}
			if !d.inTask {
				d.mu.Lock()
				sup := d.suppressed
				d.mu.Unlock()
				if sup {
					// Drain stale post-abort messages silently; don't open a new box.
					continue
				}
				d.startTask()
			}
			// Clear spinner line before printing a new flow line.
			fmt.Print("\r\033[K")
			d.printFlow(msg)
			d.setStatus(dynamicStatus(msg))
			if msg.Type == types.MsgFinalResult {
				d.endTask(true)
			}

		case <-ticker.C:
			if !d.inTask {
				continue
			}
			frame := spinRunes[d.spinIdx%len(spinRunes)]
			d.spinIdx++
			d.mu.Lock()
			status := d.status
			d.mu.Unlock()
			fmt.Printf("\r%s%s%s %s", ansiCyan, string(frame), ansiReset, status)
		}
	}
}

func (d *Display) startTask() {
	d.started = time.Now()
	d.inTask = true
	d.setStatus("initializing...")
	fmt.Printf("\n%s┌─── ⚡ agsh pipeline %s%s\n", ansiDim, strings.Repeat("─", 40), ansiReset)
}

func (d *Display) endTask(success bool) {
	d.inTask = false
	elapsed := time.Since(d.started).Round(time.Millisecond)
	icon := "✅"
	if !success {
		icon = "❌"
	}
	fmt.Printf("\r\033[K%s└─── %s  %v %s%s\n", ansiDim, icon, elapsed, strings.Repeat("─", 35), ansiReset)
}

func (d *Display) setStatus(s string) {
	d.mu.Lock()
	d.status = s
	d.mu.Unlock()
}

func (d *Display) printFlow(msg types.Message) {
	// FinalResult is surfaced via endTask; skip its flow line.
	if msg.Type == types.MsgFinalResult {
		return
	}

	from := roleLabel(msg.From)
	to := roleLabel(msg.To)

	label := string(msg.Type)
	if det := msgDetail(msg); det != "" {
		label += ": " + det
	}

	color := msgColor[msg.Type]
	if color == "" {
		color = ansiDim
	}

	// Infrastructure messages (memory, auditor) are rendered dim.
	isDim := msg.Type == types.MsgMemoryRead ||
		msg.Type == types.MsgMemoryWrite ||
		msg.Type == types.MsgMemoryResponse

	var line string
	if isDim {
		line = fmt.Sprintf("%s  %s ──[%s]──► %s%s", ansiDim, from, label, to, ansiReset)
	} else {
		line = fmt.Sprintf("  %s ──[%s%s%s]──► %s", from, color, label, ansiReset, to)
	}
	fmt.Println(line)
}

func roleLabel(r types.Role) string {
	emoji, ok := roleEmoji[r]
	if !ok {
		emoji = "•"
	}
	return emoji + " " + string(r)
}

func msgDetail(msg types.Message) string {
	switch msg.Type {
	case types.MsgTaskSpec:
		var s types.TaskSpec
		if remarshal(msg.Payload, &s) == nil && s.Intent != "" {
			return clip(s.Intent, 55)
		}
	case types.MsgSubTask:
		var s types.SubTask
		if remarshal(msg.Payload, &s) == nil && s.Intent != "" {
			return fmt.Sprintf("#%d %s", s.Sequence, clip(s.Intent, 50))
		}
	case types.MsgExecutionResult:
		var r types.ExecutionResult
		if remarshal(msg.Payload, &r) == nil && r.Status != "" {
			return r.Status
		}
	case types.MsgSubTaskOutcome:
		var o types.SubTaskOutcome
		if remarshal(msg.Payload, &o) == nil && o.Status != "" {
			return o.Status
		}
	case types.MsgCorrectionSignal:
		var c types.CorrectionSignal
		if remarshal(msg.Payload, &c) == nil {
			return fmt.Sprintf("attempt %d — %s", c.AttemptNumber, clip(c.WhatWasWrong, 40))
		}
	case types.MsgDispatchManifest:
		var m types.DispatchManifest
		if remarshal(msg.Payload, &m) == nil {
			n := len(m.SubTaskIDs)
			if n == 1 {
				return "1 subtask"
			}
			return fmt.Sprintf("%d subtasks", n)
		}
	case types.MsgReplanRequest:
		var r types.ReplanRequest
		if remarshal(msg.Payload, &r) == nil && r.GapSummary != "" {
			return clip(r.GapSummary, 45)
		}
	}
	return ""
}

// clip truncates s to at most n characters, appending "…" if trimmed.
func clip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func remarshal(src, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// Unused — satisfies Go's "declared and not used" check for ansiBold.
var _ = ansiBold
