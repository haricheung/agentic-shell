package auditor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/types"
)

// persistedStats mirrors the window stats fields that survive process restarts.
type persistedStats struct {
	WindowStart        time.Time          `json:"window_start"`
	TasksObserved      int                `json:"tasks_observed"`
	TotalCorrections   int                `json:"total_corrections"`
	GapTrends          types.GapTrendDist `json:"gap_trends"`
	BoundaryViolations []string           `json:"boundary_violations"`
	DriftAlerts        []string           `json:"drift_alerts"`
	Anomalies          []string           `json:"anomalies"`
}

// Auditor (R6) taps the message bus read-only for passive observation, and also
// subscribes to MsgAuditQuery to respond to on-demand report requests.
// It detects boundary violations, convergence failures, and thrashing, accumulates
// window statistics, and publishes AuditReport messages either on-demand or periodically.
type Auditor struct {
	b         *bus.Bus
	tap       <-chan types.Message
	logPath   string
	statsPath string
	mu        sync.Mutex
	logFile   *os.File
	interval  time.Duration // 0 = periodic reports disabled

	// convergence tracking (per task, reset on MsgFinalResult)
	correctionCounts map[string]int
	replanCounts     map[string]int

	// GGS thrashing detection
	breakSymCount map[string]int     // consecutive break_symmetry count per task_id
	lastBreakSymD map[string]float64 // D from previous break_symmetry per task_id

	// window stats — reset after each report
	windowStart        time.Time
	tasksObserved      int
	totalCorrections   int
	gapTrends          types.GapTrendDist
	boundaryViolations []string
	driftAlerts        []string
	anomalies          []string
}

// New creates an Auditor. tap must be a dedicated bus tap (NewTap()).
// statsPath is the path to the JSON file used to persist window stats across restarts.
// interval sets the periodic report cadence; pass 0 to disable periodic reports.
func New(b *bus.Bus, tap <-chan types.Message, logPath string, statsPath string, interval time.Duration) *Auditor {
	a := &Auditor{
		b:                b,
		tap:              tap,
		logPath:          logPath,
		statsPath:        statsPath,
		interval:         interval,
		correctionCounts: make(map[string]int),
		replanCounts:     make(map[string]int),
		breakSymCount:    make(map[string]int),
		lastBreakSymD:    make(map[string]float64),
		windowStart:      time.Now().UTC(),
	}
	a.loadStats()
	return a
}

// loadStats reads persisted window stats from statsPath. Safe to call before Run().
func (a *Auditor) loadStats() {
	data, err := os.ReadFile(a.statsPath)
	if err != nil {
		return // absent on first run — start fresh
	}
	var ps persistedStats
	if err := json.Unmarshal(data, &ps); err != nil {
		slog.Warn("[R6] could not load persisted stats", "error", err)
		return
	}
	a.windowStart = ps.WindowStart
	a.tasksObserved = ps.TasksObserved
	a.totalCorrections = ps.TotalCorrections
	a.gapTrends = ps.GapTrends
	a.boundaryViolations = ps.BoundaryViolations
	a.driftAlerts = ps.DriftAlerts
	a.anomalies = ps.Anomalies
	slog.Info("[R6] loaded persisted stats", "tasks", ps.TasksObserved, "corrections", ps.TotalCorrections, "window_start", ps.WindowStart.Format(time.RFC3339))
}

// saveStats writes current window stats to statsPath. Called from the auditor goroutine.
func (a *Auditor) saveStats() {
	a.mu.Lock()
	ps := persistedStats{
		WindowStart:        a.windowStart,
		TasksObserved:      a.tasksObserved,
		TotalCorrections:   a.totalCorrections,
		GapTrends:          a.gapTrends,
		BoundaryViolations: a.boundaryViolations,
		DriftAlerts:        a.driftAlerts,
		Anomalies:          a.anomalies,
	}
	a.mu.Unlock()
	data, err := json.Marshal(ps)
	if err != nil {
		slog.Warn("[R6] could not marshal stats", "error", err)
		return
	}
	if err := os.WriteFile(a.statsPath, data, 0o644); err != nil {
		slog.Warn("[R6] could not save stats", "error", err)
	}
}

// Run starts the auditor loop. It blocks until ctx is cancelled.
func (a *Auditor) Run(ctx context.Context) {
	if err := os.MkdirAll(filepath.Dir(a.logPath), 0o755); err != nil {
		slog.Error("[R6] create log dir", "error", err)
		return
	}

	f, err := os.OpenFile(a.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("[R6] open audit log file", "error", err)
		return
	}
	a.logFile = f
	defer f.Close()

	slog.Info("[R6] auditor started", "log", a.logPath)

	queryCh := a.b.Subscribe(types.MsgAuditQuery)

	var ticker *time.Ticker
	var tickC <-chan time.Time
	if a.interval > 0 {
		ticker = time.NewTicker(a.interval)
		tickC = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-tickC:
			a.mu.Lock()
			idle := a.tasksObserved == 0 && len(a.boundaryViolations) == 0 && len(a.driftAlerts) == 0
			a.mu.Unlock()
			if idle {
				// Nothing happened this window — reset silently, don't spam the terminal.
				a.mu.Lock()
				a.windowStart = time.Now().UTC()
				a.mu.Unlock()
				continue
			}
			a.publishReport("periodic")

		case msg, ok := <-queryCh:
			if !ok {
				return
			}
			slog.Debug("[R6] received AuditQuery", "from", msg.From)
			a.publishReport("on-demand")

		case msg, ok := <-a.tap:
			if !ok {
				return
			}
			a.process(msg)
		}
	}
}

// allowed sender→receiver pairs per message type (enforces "Does NOT" boundaries).
// v0.7: ReplanRequest now goes R4b→R7 (GGS), PlanDirective goes R7→R2.
// OutcomeSummary closes the loop on the happy path: R4b→R7 (GGS delivers FinalResult).
// FinalResult: R7 on accept or abandon; R4b only for the maxReplans safety net.
var allowedPaths = map[types.MessageType][]struct {
	from types.Role
	to   types.Role
}{
	types.MsgTaskSpec:         {{types.RolePerceiver, types.RolePlanner}},
	types.MsgSubTask:          {{types.RolePlanner, types.RoleExecutor}},
	types.MsgDispatchManifest: {{types.RolePlanner, types.RoleMetaVal}},
	types.MsgExecutionResult:  {{types.RoleExecutor, types.RoleAgentVal}},
	types.MsgCorrectionSignal: {{types.RoleAgentVal, types.RoleExecutor}},
	types.MsgSubTaskOutcome:   {{types.RoleAgentVal, types.RoleMetaVal}},
	types.MsgReplanRequest:    {{types.RoleMetaVal, types.RoleGGS}},
	types.MsgPlanDirective:    {{types.RoleGGS, types.RolePlanner}},
	types.MsgOutcomeSummary:   {{types.RoleMetaVal, types.RoleGGS}},
	types.MsgMemoryWrite:      {{types.RoleMetaVal, types.RoleMemory}},
	types.MsgMemoryRead:       {{types.RolePlanner, types.RoleMemory}},
	types.MsgMemoryResponse:   {{types.RoleMemory, types.RolePlanner}},
	types.MsgFinalResult:      {{types.RoleMetaVal, types.RoleUser}, {types.RoleGGS, types.RoleUser}},
}

func (a *Auditor) process(msg types.Message) {
	anomaly := "none"
	var detail *string

	// 1. Boundary violation check (any allowed path for this message type matches)
	if allowedList, ok := allowedPaths[msg.Type]; ok {
		matched := false
		for _, allowed := range allowedList {
			if msg.From == allowed.from && msg.To == allowed.to {
				matched = true
				break
			}
		}
		if !matched {
			anomaly = "boundary_violation"
			d := fmt.Sprintf("unexpected %s→%s for %s", msg.From, msg.To, msg.Type)
			detail = &d
			slog.Warn("[R6] boundary violation", "detail", d)
			a.mu.Lock()
			a.boundaryViolations = append(a.boundaryViolations, d)
			a.anomalies = append(a.anomalies, "boundary_violation: "+d)
			a.mu.Unlock()
		}
	}

	// 2. Track tasks dispatched
	if msg.Type == types.MsgDispatchManifest {
		a.mu.Lock()
		a.tasksObserved++
		a.mu.Unlock()
	}

	// 3. Track correction counts from ReplanRequest (R4b → R7).
	//    Gradient tracking now happens from PlanDirective (R7 → R2) in step 4.
	if msg.Type == types.MsgReplanRequest {
		rr, err := toReplanRequest(msg.Payload)
		if err == nil {
			a.mu.Lock()
			a.replanCounts[rr.TaskID]++
			a.correctionCounts[rr.TaskID] += rr.CorrectionCount
			a.totalCorrections += rr.CorrectionCount
			a.mu.Unlock()

			const thrashThreshold = 5
			if rr.CorrectionCount >= thrashThreshold {
				anomaly = "drift"
				a.mu.Lock()
				count := a.replanCounts[rr.TaskID]
				a.mu.Unlock()
				d := fmt.Sprintf("task %s correction_count=%d (thrashing threshold=%d) replan#%d",
					rr.TaskID, rr.CorrectionCount, thrashThreshold, count)
				detail = &d
				slog.Warn("[R6] thrashing detected", "detail", d)
				a.mu.Lock()
				a.driftAlerts = append(a.driftAlerts, d)
				a.anomalies = append(a.anomalies, "drift: "+d)
				a.mu.Unlock()
			}
		}
	}

	// 4. Track gradient and detect convergence failures from PlanDirective (R7 → R2).
	//    In v0.8 the gradient label is removed from PlanDirective; trend is derived from GradL.
	const gradLEpsilon = 0.1 // mirrors GGS epsilon constant
	if msg.Type == types.MsgPlanDirective {
		pd, err := toPlanDirective(msg.Payload)
		if err == nil {
			a.mu.Lock()
			switch {
			case pd.GradL < -gradLEpsilon:
				a.gapTrends.Improving++
			case pd.GradL > gradLEpsilon:
				a.gapTrends.Worsening++
			default:
				a.gapTrends.Stable++
			}
			a.mu.Unlock()

			if pd.GradL > gradLEpsilon {
				anomaly = "convergence_failure"
				d := fmt.Sprintf("task %s ∇L=%.3f (worsening) directive=%s L=%.3f",
					pd.TaskID, pd.GradL, pd.Directive, pd.Loss.L)
				detail = &d
				slog.Warn("[R6] convergence failure", "detail", d)
				a.mu.Lock()
				a.anomalies = append(a.anomalies, "convergence_failure: "+d)
				a.mu.Unlock()
			}

			// GGS thrashing: consecutive break_symmetry without D decreasing.
			const breakSymThrashThreshold = 2
			if pd.Directive == "break_symmetry" {
				a.mu.Lock()
				prevD, hasPrev := a.lastBreakSymD[pd.TaskID]
				a.lastBreakSymD[pd.TaskID] = pd.Loss.D
				if hasPrev && pd.Loss.D >= prevD-1e-9 {
					a.breakSymCount[pd.TaskID]++
				} else {
					a.breakSymCount[pd.TaskID] = 1
				}
				count := a.breakSymCount[pd.TaskID]
				a.mu.Unlock()

				if count >= breakSymThrashThreshold {
					anomaly = "ggs_thrashing"
					d := fmt.Sprintf("task %s: %d consecutive break_symmetry without D decreasing (D=%.3f)",
						pd.TaskID, count, pd.Loss.D)
					detail = &d
					slog.Warn("[R6] GGS thrashing", "detail", d)
					a.mu.Lock()
					a.anomalies = append(a.anomalies, "ggs_thrashing: "+d)
					a.mu.Unlock()
				}
			} else {
				a.mu.Lock()
				delete(a.breakSymCount, pd.TaskID)
				delete(a.lastBreakSymD, pd.TaskID)
				a.mu.Unlock()
			}
		}
	}

	event := types.AuditEvent{
		EventID:     uuid.New().String(),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		FromRole:    msg.From,
		ToRole:      msg.To,
		MessageType: string(msg.Type),
		Anomaly:     anomaly,
		Detail:      detail,
	}

	a.writeEvent(event)

	// Persist stats only on messages that mutate them, not on every tap message.
	if msg.Type == types.MsgDispatchManifest || msg.Type == types.MsgReplanRequest ||
		msg.Type == types.MsgPlanDirective || anomaly != "none" {
		a.saveStats()
	}
}

func (a *Auditor) publishReport(trigger string) {
	a.mu.Lock()
	now := time.Now().UTC()
	tasks := a.tasksObserved
	corrections := a.totalCorrections
	trends := a.gapTrends
	violations := append([]string(nil), a.boundaryViolations...)
	drifts := append([]string(nil), a.driftAlerts...)
	anomalies := append([]string(nil), a.anomalies...)
	windowFrom := a.windowStart.Format(time.RFC3339)

	// Reset window
	a.windowStart = now
	a.tasksObserved = 0
	a.totalCorrections = 0
	a.gapTrends = types.GapTrendDist{}
	a.boundaryViolations = nil
	a.driftAlerts = nil
	a.anomalies = nil
	a.mu.Unlock()

	// Persist zeroed window immediately so a crash after reset doesn't replay old stats.
	a.saveStats()

	avgCorrections := 0.0
	if tasks > 0 {
		avgCorrections = float64(corrections) / float64(tasks)
	}

	report := types.AuditReport{
		ReportID: uuid.New().String(),
		Period: types.AuditPeriod{
			From: windowFrom,
			To:   now.Format(time.RFC3339),
		},
		TasksObserved:      tasks,
		BoundaryViolations: violations,
		ConvergenceHealth: types.ConvergenceHealth{
			AvgCorrectionCount:   avgCorrections,
			GapTrendDistribution: trends,
		},
		DriftAlerts: drifts,
		Anomalies:   anomalies,
	}

	slog.Info("[R6] publishing audit report", "trigger", trigger, "tasks", tasks, "avg_corrections", avgCorrections, "violations", len(violations), "drifts", len(drifts))

	a.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: now,
		From:      types.RoleAuditor,
		To:        types.RoleUser,
		Type:      types.MsgAuditReport,
		Payload:   report,
	})
}

func (a *Auditor) writeEvent(e types.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	data, err := json.Marshal(e)
	if err != nil {
		slog.Error("[R6] marshal audit event", "error", err)
		return
	}
	if _, err := fmt.Fprintf(a.logFile, "%s\n", data); err != nil {
		slog.Error("[R6] write audit event", "error", err)
	}
}

func toReplanRequest(payload any) (types.ReplanRequest, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.ReplanRequest{}, err
	}
	var rr types.ReplanRequest
	return rr, json.Unmarshal(b, &rr)
}

func toPlanDirective(payload any) (types.PlanDirective, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.PlanDirective{}, err
	}
	var pd types.PlanDirective
	return pd, json.Unmarshal(b, &pd)
}
