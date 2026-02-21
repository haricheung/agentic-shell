package metaval

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/tasklog"
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R4b — Meta-Validator. Merge SubTaskOutcome results and decide whether the task is complete or needs replanning.

Each SubTaskOutcome carries the subtask's intent and success_criteria that R4a was checking.
Your job: verify that every criterion across all subtasks is satisfied by the combined outputs.

Assessment rules:
- "accept" ONLY when the combined outputs POSITIVELY demonstrate every success_criterion listed in every SubTaskOutcome.
- "replan" if any criterion is unmet or a subtask failed and its output is needed. State exactly which criterion failed and why.
- Do NOT accept on vague grounds. Absence of failure is not the same as presence of evidence.

merged_output rules:
- Combine all subtask outputs into a single user-facing result string or object.
- Include concrete data (file paths, values, counts) — not process descriptions.
- Omit intermediate steps (file discovery, etc.) unless they are the answer.

Output — choose ONE:

All criteria met:
{"verdict":"accept","summary":"<one sentence for the user>","merged_output":"<combined result>"}

Criteria unmet, replanning possible:
{"verdict":"replan","gap_summary":"<which criterion failed and why>","failed_subtasks":["<subtask_id>"],"recommendation":"replan"}

No markdown, no prose, no code fences.`

// manifestTracker tracks incoming SubTaskOutcomes for a given dispatch manifest
type manifestTracker struct {
	spec          types.TaskSpec
	manifest      types.DispatchManifest
	outcomes      []types.SubTaskOutcome
	expectedCount int
}

// MetaValidator is R4b. It collects SubTaskOutcomes and merges results.
type MetaValidator struct {
	llm    *llm.Client
	b      *bus.Bus
	logReg *tasklog.Registry
	mu     sync.Mutex
	trackers   map[string]*manifestTracker // taskID -> tracker
	taskStart  map[string]time.Time        // taskID -> time first manifest was received
	replanCounts map[string]int            // replan round counter for maxReplans safety net
	// outputFn is called when a final result is ready for the user
	outputFn func(taskID, summary string, output any)
}

// New creates a MetaValidator.
func New(b *bus.Bus, llmClient *llm.Client, outputFn func(taskID, summary string, output any), logReg *tasklog.Registry) *MetaValidator {
	return &MetaValidator{
		llm:          llmClient,
		b:            b,
		logReg:       logReg,
		trackers:     make(map[string]*manifestTracker),
		taskStart:    make(map[string]time.Time),
		replanCounts: make(map[string]int),
		outputFn:     outputFn,
	}
}

// Run listens for DispatchManifest and SubTaskOutcome messages.
func (m *MetaValidator) Run(ctx context.Context) {
	manifestCh := m.b.Subscribe(types.MsgDispatchManifest)
	outcomeCh := m.b.Subscribe(types.MsgSubTaskOutcome)

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-manifestCh:
			if !ok {
				return
			}
			manifest, err := toDispatchManifest(msg.Payload)
			if err != nil {
				log.Printf("[R4b] ERROR: bad DispatchManifest: %v", err)
				continue
			}

			var spec types.TaskSpec
			if manifest.TaskSpec != nil {
				spec = *manifest.TaskSpec
			}

			m.mu.Lock()
			m.trackers[manifest.TaskID] = &manifestTracker{
				spec:          spec,
				manifest:      manifest,
				expectedCount: len(manifest.SubTaskIDs),
			}
			// Record start time on first manifest for GGS Ω elapsed time computation.
			if _, seen := m.taskStart[manifest.TaskID]; !seen {
				m.taskStart[manifest.TaskID] = time.Now().UTC()
			}
			m.mu.Unlock()
			log.Printf("[R4b] tracking task=%s expecting %d outcomes", manifest.TaskID, len(manifest.SubTaskIDs))

		case msg, ok := <-outcomeCh:
			if !ok {
				return
			}
			outcome, err := toSubTaskOutcome(msg.Payload)
			if err != nil {
				log.Printf("[R4b] ERROR: bad SubTaskOutcome: %v", err)
				continue
			}

			m.mu.Lock()
			tracker, found := m.trackers[outcome.ParentTaskID]
			if !found {
				m.mu.Unlock()
				log.Printf("[R4b] WARNING: outcome for unknown task %s", outcome.ParentTaskID)
				continue
			}
			tracker.outcomes = append(tracker.outcomes, outcome)
			complete := len(tracker.outcomes) >= tracker.expectedCount
			m.mu.Unlock()

			log.Printf("[R4b] outcome for subtask=%s status=%s (%d/%d)",
				outcome.SubTaskID, outcome.Status, len(tracker.outcomes), tracker.expectedCount)

			if complete {
				go m.evaluate(ctx, tracker)
			}
		}
	}
}

func (m *MetaValidator) evaluate(ctx context.Context, tracker *manifestTracker) {
	taskID := tracker.manifest.TaskID

	// Collect failed sub-tasks
	var failedIDs []string
	totalCorrections := 0
	for _, o := range tracker.outcomes {
		if o.Status == "failed" {
			failedIDs = append(failedIDs, o.SubTaskID)
		}
		totalCorrections += len(o.GapTrajectory)
	}

	// Log the full criteria set R4b is evaluating against so it's auditable.
	log.Printf("[R4b] task=%s evaluating %d outcomes (failed=%d)",
		taskID, len(tracker.outcomes), len(failedIDs))
	for _, o := range tracker.outcomes {
		criteriaJSON, _ := json.Marshal(o.SuccessCriteria)
		log.Printf("[R4b]   subtask=%s status=%s criteria=%s", o.SubTaskID, o.Status, criteriaJSON)
	}

	// Hard gate (code-enforced, not LLM-dependent): any failed subtask forces
	// replan immediately. The LLM is only called when ALL subtasks matched so
	// it cannot override a failed status by reasoning about "overall goal".
	if len(failedIDs) > 0 {
		log.Printf("[R4b] task=%s hard-gate REPLAN: %d failed subtask(s): %v", taskID, len(failedIDs), failedIDs)
		// Build gap summary with actual failure reasons so GGS and memory get
		// actionable text, not just subtask IDs.
		var gapParts []string
		for _, o := range tracker.outcomes {
			if o.Status == "failed" {
				if o.FailureReason != nil && *o.FailureReason != "" {
					gapParts = append(gapParts, *o.FailureReason)
				} else {
					gapParts = append(gapParts, o.SubTaskID)
				}
			}
		}
		gapSummary := strings.Join(gapParts, "; ")
		if gapSummary == "" {
			gapSummary = fmt.Sprintf("%d subtask(s) failed: %v", len(failedIDs), failedIDs)
		}
		m.triggerReplan(ctx, tracker, failedIDs, totalCorrections, gapSummary)
		return
	}

	// All subtasks matched — call LLM only to merge outputs and produce the
	// user-facing summary. It can no longer influence accept/replan decision.
	outcomesJSON, _ := json.MarshalIndent(tracker.outcomes, "", "  ")
	specJSON, _ := json.MarshalIndent(tracker.spec, "", "  ")
	userPrompt := fmt.Sprintf(
		"TaskSpec (intent and constraints):\n%s\n\nSubTaskOutcomes (each carries its own success_criteria):\n%s\n\nAll subtasks matched. Merge their outputs into a single user-facing result.",
		specJSON, outcomesJSON)

	raw, usage, err := m.llm.Chat(ctx, systemPrompt, userPrompt)
	tl := m.logReg.Get(taskID)
	tl.LLMCall("metaval", systemPrompt, userPrompt, raw, usage.PromptTokens, usage.CompletionTokens, 0)
	if err != nil {
		log.Printf("[R4b] ERROR: LLM call failed: %v", err)
		return
	}
	raw = llm.StripFences(raw)

	var v struct {
		Verdict        string `json:"verdict"`
		Summary        string `json:"summary"`
		MergedOutput   any    `json:"merged_output"`
		GapSummary     string `json:"gap_summary"`
		FailedSubtasks []string `json:"failed_subtasks"`
		Recommendation string `json:"recommendation"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		log.Printf("[R4b] ERROR: parse verdict: %v (raw: %s)", err, raw)
		return
	}

	switch v.Verdict {
	case "accept":
		log.Printf("[R4b] task=%s ACCEPTED — forwarding to R7 (GGS) for final loss record and delivery", taskID)
		m.logReg.Close(taskID, "accepted") // write task_end and flush before delivering result

		// Build meaningful tags from task intent for cross-task retrieval
		intentTags := []string{"success", taskID}
		if tracker.spec.Intent != "" {
			for _, word := range strings.Fields(tracker.spec.Intent) {
				if len(word) >= 4 {
					intentTags = append(intentTags, strings.ToLower(strings.Trim(word, ".,;:!?")))
				}
			}
		}

		// Write to memory (R4b's responsibility; GGS does not write memory)
		entry := types.MemoryEntry{
			EntryID:   uuid.New().String(),
			TaskID:    taskID,
			Type:      "episodic",
			Content:   v.MergedOutput,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tags:      intentTags,
		}
		for _, o := range tracker.outcomes {
			if o.Status == "matched" {
				entry.CriteriaMet = append(entry.CriteriaMet, o.SubTaskID)
			}
		}
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RoleMemory,
			Type:      types.MsgMemoryWrite,
			Payload:   entry,
		})

		// Forward to GGS so it records the final loss (D=0) and delivers the result.
		// GGS is the sole decision-maker in the medium loop — it closes the loop even
		// on the happy path by computing L_t and updating L_prev before emitting FinalResult.
		m.mu.Lock()
		start, hasStart := m.taskStart[taskID]
		outcomes := append([]types.SubTaskOutcome(nil), tracker.outcomes...)
		m.mu.Unlock()
		var elapsedMs int64
		if hasStart {
			elapsedMs = time.Since(start).Milliseconds()
		}
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RoleGGS,
			Type:      types.MsgOutcomeSummary,
			Payload: types.OutcomeSummary{
				TaskID:       taskID,
				Summary:      v.Summary,
				MergedOutput: v.MergedOutput,
				ElapsedMs:    elapsedMs,
				Outcomes:     outcomes,
			},
		})

		// Clean up tracker and per-task timing
		m.mu.Lock()
		delete(m.trackers, taskID)
		delete(m.taskStart, taskID)
		delete(m.replanCounts, taskID)
		m.mu.Unlock()

	case "replan":
		m.triggerReplan(ctx, tracker, failedIDs, totalCorrections, v.GapSummary)
	}
}

// triggerReplan handles the replan path for both the hard gate (code-enforced
// failed subtask check) and the LLM-driven replan verdict. It writes a
// procedural memory entry, publishes a ReplanRequest to R7 (GGS), and resets
// the tracker. In v0.7 GGS owns gradient computation; R4b delivers raw outcome data.
//
// Expectations:
//   - Abandons and publishes FinalResult when replanCount >= maxReplans (safety net)
//   - Writes a procedural MemoryEntry before publishing ReplanRequest
//   - Resets tracker.outcomes so the next round starts clean
//   - Increments replanCounts before checking the limit
//   - Sends ReplanRequest to R7 (GGS), not R2 (Planner)
//   - Includes full outcomes and elapsed_ms in ReplanRequest for GGS gradient computation
func (m *MetaValidator) triggerReplan(ctx context.Context, tracker *manifestTracker, failedIDs []string, totalCorrections int, gapSummary string) {
	const maxReplans = 3
	taskID := tracker.manifest.TaskID

	m.mu.Lock()
	m.replanCounts[taskID]++
	replanCount := m.replanCounts[taskID]
	start, hasStart := m.taskStart[taskID]
	outcomes := append([]types.SubTaskOutcome(nil), tracker.outcomes...) // snapshot before reset
	m.mu.Unlock()

	tl := m.logReg.Get(taskID)
	tl.Replan(gapSummary, replanCount)

	// Safety net: hard-abandon after maxReplans regardless of GGS directive.
	// GGS should have issued abandon before this point via Ω ≥ 0.8, but this
	// prevents infinite looping if GGS is slow or unavailable.
	if replanCount >= maxReplans {
		log.Printf("[R4b] task=%s ABANDONED (safety net) after %d replan rounds", taskID, replanCount)
		m.logReg.Close(taskID, "abandoned")
		// Build a human-readable reason from the last failure outcomes so the
		// user sees WHY it failed, not just which subtask ID.
		var reasons []string
		for _, o := range outcomes {
			if o.Status == "failed" && o.FailureReason != nil && *o.FailureReason != "" {
				reasons = append(reasons, *o.FailureReason)
			}
		}
		detail := gapSummary
		if len(reasons) > 0 {
			detail = strings.Join(reasons, "; ")
		}
		summary := fmt.Sprintf("❌ Task abandoned after %d failed attempts. %s", replanCount, detail)
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RoleUser,
			Type:      types.MsgFinalResult,
			Payload:   types.FinalResult{TaskID: taskID, Summary: summary},
		})
		if m.outputFn != nil {
			m.outputFn(taskID, summary, nil)
		}
		m.mu.Lock()
		delete(m.trackers, taskID)
		delete(m.replanCounts, taskID)
		delete(m.taskStart, taskID)
		m.mu.Unlock()
		return
	}

	// Write procedural memory so the planner can avoid repeating the failure.
	type failureLesson struct {
		Lesson      string   `json:"lesson"`
		GapSummary  string   `json:"gap_summary"`
		FailedTasks []string `json:"failed_subtasks"`
	}
	lesson := failureLesson{
		Lesson:      "Task failed: " + gapSummary + ". Avoid repeating the same approach.",
		GapSummary:  gapSummary,
		FailedTasks: failedIDs,
	}
	tags := []string{"failure", "replan", taskID}
	for _, word := range strings.Fields(gapSummary) {
		if len(word) >= 4 {
			tags = append(tags, strings.ToLower(strings.Trim(word, ".,;:!?")))
		}
	}
	m.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleMetaVal,
		To:        types.RoleMemory,
		Type:      types.MsgMemoryWrite,
		Payload: types.MemoryEntry{
			EntryID:   uuid.New().String(),
			TaskID:    taskID,
			Type:      "procedural",
			Content:   lesson,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tags:      tags,
		},
	})

	// Compute elapsed time for GGS Ω calculation.
	var elapsedMs int64
	if hasStart {
		elapsedMs = time.Since(start).Milliseconds()
	}

	rr := types.ReplanRequest{
		TaskID:          taskID,
		GapSummary:      gapSummary,
		FailedSubTasks:  failedIDs,
		CorrectionCount: totalCorrections,
		ElapsedMs:       elapsedMs,
		Outcomes:        outcomes,
		Recommendation:  "replan",
	}
	log.Printf("[R4b] task=%s sending ReplanRequest to R7 (GGS) round=%d gap=%q elapsed=%dms",
		taskID, replanCount, gapSummary, elapsedMs)
	m.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleMetaVal,
		To:        types.RoleGGS,
		Type:      types.MsgReplanRequest,
		Payload:   rr,
	})

	m.mu.Lock()
	tracker.outcomes = nil
	m.mu.Unlock()
}

func toDispatchManifest(payload any) (types.DispatchManifest, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.DispatchManifest{}, err
	}
	var d types.DispatchManifest
	return d, json.Unmarshal(b, &d)
}

func toSubTaskOutcome(payload any) (types.SubTaskOutcome, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.SubTaskOutcome{}, err
	}
	var o types.SubTaskOutcome
	return o, json.Unmarshal(b, &o)
}
