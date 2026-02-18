package auditor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/types"
)

// Auditor taps the message bus read-only and writes structured AuditEvents to a JSONL file.
// It detects boundary violations, convergence failures, and thrashing.
type Auditor struct {
	tap     <-chan types.Message
	logPath string
	mu      sync.Mutex
	logFile *os.File

	// convergence tracking
	correctionCounts map[string]int // taskID -> correction_count
	replanCounts     map[string]int // taskID -> replan count
}

// New creates an Auditor.
func New(tap <-chan types.Message, logPath string) *Auditor {
	return &Auditor{
		tap:              tap,
		logPath:          logPath,
		correctionCounts: make(map[string]int),
		replanCounts:     make(map[string]int),
	}
}

// Run starts the auditor loop. It blocks until ctx is cancelled.
func (a *Auditor) Run(ctx context.Context) {
	if err := os.MkdirAll(filepath.Dir(a.logPath), 0o755); err != nil {
		log.Printf("[AUDIT] ERROR: create log dir: %v", err)
		return
	}

	f, err := os.OpenFile(a.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[AUDIT] ERROR: open log file: %v", err)
		return
	}
	a.logFile = f
	defer f.Close()

	log.Printf("[AUDIT] started; writing to %s", a.logPath)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-a.tap:
			if !ok {
				return
			}
			a.process(msg)
		}
	}
}

// allowed sender→receiver pairs per message type (enforces "Does NOT" boundaries)
var allowedPaths = map[types.MessageType]struct {
	from types.Role
	to   types.Role
}{
	types.MsgTaskSpec:         {types.RolePerceiver, types.RolePlanner},
	types.MsgSubTask:          {types.RolePlanner, types.RoleExecutor},
	types.MsgDispatchManifest: {types.RolePlanner, types.RoleMetaVal},
	types.MsgExecutionResult:  {types.RoleExecutor, types.RoleAgentVal},
	types.MsgCorrectionSignal: {types.RoleAgentVal, types.RoleExecutor},
	types.MsgSubTaskOutcome:   {types.RoleAgentVal, types.RoleMetaVal},
	types.MsgReplanRequest:    {types.RoleMetaVal, types.RolePlanner},
	types.MsgMemoryWrite:      {types.RoleMetaVal, types.RoleMemory},
	types.MsgMemoryRead:       {types.RolePlanner, types.RoleMemory},
	types.MsgMemoryResponse:   {types.RoleMemory, types.RolePlanner},
	types.MsgFinalResult:      {types.RoleMetaVal, types.RoleUser},
}

func (a *Auditor) process(msg types.Message) {
	anomaly := "none"
	var detail *string

	// 1. Boundary violation check
	if allowed, ok := allowedPaths[msg.Type]; ok {
		if msg.From != allowed.from || msg.To != allowed.to {
			anomaly = "boundary_violation"
			d := fmt.Sprintf("expected %s→%s for %s, got %s→%s",
				allowed.from, allowed.to, msg.Type, msg.From, msg.To)
			detail = &d
			log.Printf("[AUDIT] BOUNDARY VIOLATION: %s", d)
		}
	}

	// 2. Convergence failure checks on ReplanRequest
	if msg.Type == types.MsgReplanRequest {
		rr, err := toReplanRequest(msg.Payload)
		if err == nil {
			a.replanCounts[rr.TaskID]++
			a.correctionCounts[rr.TaskID] += rr.CorrectionCount

			if rr.GapTrend == "worsening" {
				anomaly = "convergence_failure"
				d := fmt.Sprintf("task %s gap_trend=worsening correction_count=%d replan#%d",
					rr.TaskID, rr.CorrectionCount, a.replanCounts[rr.TaskID])
				detail = &d
				log.Printf("[AUDIT] CONVERGENCE FAILURE: %s", d)
			}

			const thrashThreshold = 5
			if rr.CorrectionCount >= thrashThreshold {
				anomaly = "drift"
				d := fmt.Sprintf("task %s correction_count=%d (thrashing threshold=%d)",
					rr.TaskID, rr.CorrectionCount, thrashThreshold)
				detail = &d
				log.Printf("[AUDIT] THRASHING DETECTED: %s", d)
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
}

func (a *Auditor) writeEvent(e types.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	data, err := json.Marshal(e)
	if err != nil {
		log.Printf("[AUDIT] ERROR: marshal event: %v", err)
		return
	}
	if _, err := fmt.Fprintf(a.logFile, "%s\n", data); err != nil {
		log.Printf("[AUDIT] ERROR: write event: %v", err)
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
