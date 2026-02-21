package types

import "time"

// Role identifiers
type Role string

const (
	RoleUser      Role = "User"
	RolePerceiver Role = "R1"
	RolePlanner   Role = "R2"
	RoleExecutor  Role = "R3"
	RoleAgentVal  Role = "R4a"
	RoleMetaVal   Role = "R4b"
	RoleMemory    Role = "R5"
	RoleAuditor   Role = "R6"
	RoleGGS       Role = "R7"
	RoleCC        Role = "cc"  // synthetic role: Claude Code CLI invoked by R2
)

// MessageType identifies the payload type of a bus message
type MessageType string

const (
	MsgTaskSpec         MessageType = "TaskSpec"
	MsgSubTask          MessageType = "SubTask"
	MsgDispatchManifest MessageType = "DispatchManifest"
	MsgExecutionResult  MessageType = "ExecutionResult"
	MsgCorrectionSignal MessageType = "CorrectionSignal"
	MsgSubTaskOutcome   MessageType = "SubTaskOutcome"
	MsgReplanRequest    MessageType = "ReplanRequest"
	MsgMemoryWrite      MessageType = "MemoryWrite"
	MsgMemoryRead       MessageType = "MemoryRead"
	MsgMemoryResponse   MessageType = "MemoryResponse"
	MsgFinalResult      MessageType = "FinalResult"
	MsgAuditQuery       MessageType = "AuditQuery"    // User → R6: request an on-demand report
	MsgAuditReport      MessageType = "AuditReport"   // R6 → User: generated report
	MsgPlanDirective    MessageType = "PlanDirective"    // R7 → R2: gradient-directed planning instruction
	MsgOutcomeSummary   MessageType = "OutcomeSummary"   // R4b → R7: all subtasks matched; GGS delivers final result
	MsgCCCall           MessageType = "CCCall"           // R2 → cc: R2 is about to call Claude Code CLI
	MsgCCResponse       MessageType = "CCResponse"       // cc → R2: Claude Code CLI responded
)

// Message is the envelope for all inter-role communication on the bus
type Message struct {
	ID        string      `json:"id"`
	Timestamp time.Time   `json:"timestamp"`
	From      Role        `json:"from"`
	To        Role        `json:"to"`
	Type      MessageType `json:"type"`
	Payload   any         `json:"payload"`
}

// TaskSpec is produced by R1 Perceiver and consumed by R2 Planner
type TaskSpec struct {
	TaskID          string      `json:"task_id"`
	Intent          string      `json:"intent"`
	SuccessCriteria []string    `json:"success_criteria"`
	Constraints     Constraints `json:"constraints"`
	RawInput        string      `json:"raw_input"`
}

type Constraints struct {
	Scope    *string `json:"scope"`
	Deadline *string `json:"deadline"`
}

// SubTask is produced by R2 Planner and consumed by R3 Executor
type SubTask struct {
	SubTaskID       string   `json:"subtask_id"`
	ParentTaskID    string   `json:"parent_task_id"`
	Intent          string   `json:"intent"`
	SuccessCriteria []string `json:"success_criteria"`
	Context         string   `json:"context"`
	Deadline        *string  `json:"deadline"`
	Sequence        int      `json:"sequence"`
}

// DispatchManifest is sent by R2 to R4b so it knows expected sub-task count
type DispatchManifest struct {
	TaskID      string    `json:"task_id"`
	SubTaskIDs  []string  `json:"subtask_ids"`
	TaskSpec    *TaskSpec `json:"task_spec,omitempty"`
	DispatchedAt string   `json:"dispatched_at"`
}

// ExecutionResult is produced by R3 Executor and consumed by R4a Agent-Validator
type ExecutionResult struct {
	SubTaskID   string   `json:"subtask_id"`
	Status      string   `json:"status"` // "completed" | "uncertain" | "failed"
	Output      any      `json:"output"`
	Uncertainty *string  `json:"uncertainty"`
	ToolCalls   []string `json:"tool_calls"`
}

// CorrectionSignal is produced by R4a Agent-Validator and consumed by R3 Executor
type CorrectionSignal struct {
	SubTaskID     string `json:"subtask_id"`
	AttemptNumber int    `json:"attempt_number"`
	WhatWasWrong  string `json:"what_was_wrong"`
	WhatToDo      string `json:"what_to_do"`
}

// GapTrajectoryPoint records one attempt in the fast loop
type GapTrajectoryPoint struct {
	Attempt      int      `json:"attempt"`
	Score        float64  `json:"score"`
	UnmetCriteria []string `json:"unmet_criteria"`
}

// SubTaskOutcome is produced by R4a and consumed by R4b and R7 (GGS)
type SubTaskOutcome struct {
	SubTaskID       string               `json:"subtask_id"`
	ParentTaskID    string               `json:"parent_task_id"`
	Intent          string               `json:"intent"`
	SuccessCriteria []string             `json:"success_criteria"` // copied from SubTask so R4b can check them
	Status          string               `json:"status"`           // "matched" | "failed"
	Output          any                  `json:"output"`
	FailureReason   *string              `json:"failure_reason"`
	GapTrajectory   []GapTrajectoryPoint `json:"gap_trajectory"`
	ToolCalls       []string             `json:"tool_calls,omitempty"` // tool names used in final execution attempt; for GGS blocked_tools
}

// ReplanRequest is produced by R4b and consumed by R7 (GGS). GGS owns gradient computation.
type ReplanRequest struct {
	TaskID          string           `json:"task_id"`
	GapSummary      string           `json:"gap_summary"`
	FailedSubTasks  []string         `json:"failed_subtasks"`
	CorrectionCount int              `json:"correction_count"`
	ElapsedMs       int64            `json:"elapsed_ms"`              // wall-clock ms since task started; for Ω computation
	Outcomes        []SubTaskOutcome `json:"outcomes"`                // full outcome data for GGS gradient computation
	Recommendation  string           `json:"recommendation"`          // "replan" | "abandon"
}

// LossBreakdown carries the GGS loss components for a replan round.
type LossBreakdown struct {
	D     float64 `json:"D"`     // intent-result distance [0,1]
	P     float64 `json:"P"`     // process implausibility [0,1]
	Omega float64 `json:"Omega"` // resource cost [0,1]
	L     float64 `json:"L"`     // total weighted loss
}

// PlanDirective is produced by R7 (GGS) and consumed by R2 Planner.
// It carries gradient-informed constraints so R2 can make a principled plan adjustment.
type PlanDirective struct {
	TaskID          string        `json:"task_id"`
	Loss            LossBreakdown `json:"loss"`
	Gradient        string        `json:"gradient"`          // "improving" | "stable" | "worsening" | "plateau"
	Directive       string        `json:"directive"`         // "refine" | "change_path" | "change_approach" | "break_symmetry" | "abandon"
	BlockedTools    []string      `json:"blocked_tools"`     // tools R2 must not use in next plan
	FailedCriterion string        `json:"failed_criterion"`  // primary criterion driving D
	FailureClass    string        `json:"failure_class"`     // "logical" | "environmental" | "mixed"
	BudgetPressure  float64       `json:"budget_pressure"`   // Ω value for display
	Rationale       string        `json:"rationale"`         // human-readable explanation; logged by Auditor
}

// MemoryEntry is written by R4b and read by R2
type MemoryEntry struct {
	EntryID     string   `json:"entry_id"`
	TaskID      string   `json:"task_id"`
	Type        string   `json:"type"` // "episodic"
	Content     any      `json:"content"`
	CriteriaMet []string `json:"criteria_met"`
	Timestamp   string   `json:"timestamp"`
	Tags        []string `json:"tags"`
}

// MemoryQuery is sent by R2 to R5
type MemoryQuery struct {
	TaskID string `json:"task_id,omitempty"`
	Tags   string `json:"tags,omitempty"`
	Query  string `json:"query,omitempty"`
}

// MemoryResponse is sent by R5 to R2
type MemoryResponse struct {
	TaskID  string        `json:"task_id"`
	Entries []MemoryEntry `json:"entries"`
}

// AuditEvent is written to the audit log by R6 Auditor
type AuditEvent struct {
	EventID     string `json:"event_id"`
	Timestamp   string `json:"timestamp"`
	FromRole    Role   `json:"from_role"`
	ToRole      Role   `json:"to_role"`
	MessageType string `json:"message_type"`
	Anomaly     string `json:"anomaly"` // "boundary_violation" | "convergence_failure" | "drift" | "none"
	Detail      *string `json:"detail"`
}

// AuditReport is a summary produced by R6 for the human operator
type AuditReport struct {
	ReportID           string             `json:"report_id"`
	Period             AuditPeriod        `json:"period"`
	TasksObserved      int                `json:"tasks_observed"`
	BoundaryViolations []string           `json:"boundary_violations"`
	ConvergenceHealth  ConvergenceHealth  `json:"convergence_health"`
	DriftAlerts        []string           `json:"drift_alerts"`
	Anomalies          []string           `json:"anomalies"`
}

type AuditPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type GapTrendDist struct {
	Improving int `json:"improving"`
	Stable    int `json:"stable"`
	Worsening int `json:"worsening"`
}

type ConvergenceHealth struct {
	AvgCorrectionCount   float64      `json:"avg_correction_count"`
	GapTrendDistribution GapTrendDist `json:"gap_trend_distribution"`
}

// OutcomeSummary is produced by R4b when all subtasks matched and the LLM merge
// verified all task criteria. R4b forwards it to R7 (GGS) so GGS can record the
// final loss value, update L_prev, and deliver the result to the user.
// This closes the medium loop on the happy path — GGS is always the decision-maker.
type OutcomeSummary struct {
	TaskID       string           `json:"task_id"`
	Summary      string           `json:"summary"`       // one-sentence user-facing summary from R4b LLM merge
	MergedOutput any              `json:"merged_output"` // combined user-facing result
	ElapsedMs    int64            `json:"elapsed_ms"`    // wall-clock ms since task started; for Ω logging
	Outcomes     []SubTaskOutcome `json:"outcomes"`      // full outcomes; GGS records final D/L
}

// FinalResult carries the merged result to the user
type FinalResult struct {
	TaskID  string `json:"task_id"`
	Summary string `json:"summary"`
	Output  any    `json:"output"`
}

// CCCall is published by R2 just before invoking the Claude Code CLI.
type CCCall struct {
	TaskID string `json:"task_id"`
	CallN  int    `json:"call_n"`  // 1-based call index within this planning session
	MaxN   int    `json:"max_n"`   // maxCCCalls constant
	Prompt string `json:"prompt"`  // the question sent to cc
}

// CCResponse is published by R2 after the Claude Code CLI replies.
type CCResponse struct {
	TaskID   string `json:"task_id"`
	CallN    int    `json:"call_n"`
	Chars    int    `json:"chars"`    // length of response in bytes
	Response string `json:"response"` // first 300 chars for display; full response used internally
}

// Ensure time import is used
var _ = time.Now
