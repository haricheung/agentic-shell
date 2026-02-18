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
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R4b — Meta-Validator. Merge SubTaskOutcome results and decide whether the task is complete or needs replanning.

Assessment rules:
- "accept" if the combined outputs of all subtasks satisfy every success criterion in the TaskSpec. Failed subtasks are acceptable if the overall goal is met by the remaining outputs.
- "replan" if one or more success criteria remain unmet and the gap is addressable by replanning. Summarise exactly what is missing and why.
- Do NOT accept if a critical subtask failed and its output is required to satisfy the TaskSpec.

merged_output rules:
- Combine all subtask outputs into a single user-facing result string or object.
- Include concrete data (file paths, values, counts) — not process descriptions.
- Omit intermediate steps (file discovery, etc.) unless they are the answer.

Output — choose ONE:

All criteria met:
{"verdict":"accept","summary":"<one sentence for the user>","merged_output":"<combined result>"}

Criteria unmet, replanning possible:
{"verdict":"replan","gap_summary":"<what is missing and why>","failed_subtasks":["<subtask_id>"],"recommendation":"replan"}

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
	llm      *llm.Client
	b        *bus.Bus
	mu       sync.Mutex
	trackers map[string]*manifestTracker // taskID -> tracker
	// replanCounts tracks per-task correction history for gap_trend
	replanCounts     map[string]int
	prevFailedCounts map[string]int
	// outputFn is called when a final result is ready for the user
	outputFn func(taskID, summary string, output any)
}

// New creates a MetaValidator.
func New(b *bus.Bus, llmClient *llm.Client, outputFn func(taskID, summary string, output any)) *MetaValidator {
	return &MetaValidator{
		llm:              llmClient,
		b:                b,
		trackers:         make(map[string]*manifestTracker),
		replanCounts:     make(map[string]int),
		prevFailedCounts: make(map[string]int),
		outputFn:         outputFn,
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

	// Compute gap_trend
	m.mu.Lock()
	prevFailed := m.prevFailedCounts[taskID]
	m.prevFailedCounts[taskID] = len(failedIDs)
	replanCount := m.replanCounts[taskID]
	m.mu.Unlock()

	gapTrend := computeGapTrend(len(failedIDs), prevFailed, replanCount)

	// Ask LLM to merge and assess
	outcomesJSON, _ := json.MarshalIndent(tracker.outcomes, "", "  ")
	specJSON, _ := json.MarshalIndent(tracker.spec, "", "  ")
	userPrompt := fmt.Sprintf("TaskSpec:\n%s\n\nSubTaskOutcomes:\n%s\n\nFailed subtasks: %v\nGap trend: %s",
		specJSON, outcomesJSON, failedIDs, gapTrend)

	raw, err := m.llm.Chat(ctx, systemPrompt, userPrompt)
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
		log.Printf("[R4b] task=%s ACCEPTED", taskID)

		// Build meaningful tags from task intent for cross-task retrieval
		intentTags := []string{"success", taskID}
		if tracker.spec.Intent != "" {
			for _, word := range strings.Fields(tracker.spec.Intent) {
				if len(word) >= 4 {
					intentTags = append(intentTags, strings.ToLower(strings.Trim(word, ".,;:!?")))
				}
			}
		}

		// Write to memory
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

		// Deliver final result
		finalResult := types.FinalResult{
			TaskID:  taskID,
			Summary: v.Summary,
			Output:  v.MergedOutput,
		}
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RoleUser,
			Type:      types.MsgFinalResult,
			Payload:   finalResult,
		})

		if m.outputFn != nil {
			m.outputFn(taskID, v.Summary, v.MergedOutput)
		}

		// Clean up tracker
		m.mu.Lock()
		delete(m.trackers, taskID)
		m.mu.Unlock()

	case "replan":
		const maxReplans = 3

		m.mu.Lock()
		m.replanCounts[taskID]++
		replanCount := m.replanCounts[taskID]
		m.mu.Unlock()

		// Abandon after too many failed replan rounds to prevent infinite loops.
		if replanCount >= maxReplans {
			log.Printf("[R4b] task=%s ABANDONED after %d replan rounds", taskID, replanCount)
			summary := fmt.Sprintf("❌ Task abandoned after %d failed attempts. %s", replanCount, v.GapSummary)
			finalResult := types.FinalResult{TaskID: taskID, Summary: summary}
			m.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleMetaVal,
				To:        types.RoleUser,
				Type:      types.MsgFinalResult,
				Payload:   finalResult,
			})
			if m.outputFn != nil {
				m.outputFn(taskID, summary, nil)
			}
			m.mu.Lock()
			delete(m.trackers, taskID)
			delete(m.replanCounts, taskID)
			delete(m.prevFailedCounts, taskID)
			m.mu.Unlock()
			return
		}

		// Write a procedural memory entry so the planner can avoid the same mistakes
		type failureLesson struct {
			Lesson      string   `json:"lesson"`
			GapSummary  string   `json:"gap_summary"`
			FailedTasks []string `json:"failed_subtasks"`
		}
		lesson := failureLesson{
			Lesson:      "Task failed: " + v.GapSummary + ". Avoid repeating the same approach.",
			GapSummary:  v.GapSummary,
			FailedTasks: failedIDs,
		}
		// Build tags from gap summary keywords for retrieval
		tags := []string{"failure", "replan", taskID}
		for _, word := range strings.Fields(v.GapSummary) {
			if len(word) >= 4 {
				tags = append(tags, strings.ToLower(strings.Trim(word, ".,;:!?")))
			}
		}
		failEntry := types.MemoryEntry{
			EntryID:   uuid.New().String(),
			TaskID:    taskID,
			Type:      "procedural",
			Content:   lesson,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tags:      tags,
		}
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RoleMemory,
			Type:      types.MsgMemoryWrite,
			Payload:   failEntry,
		})

		rr := types.ReplanRequest{
			TaskID:          taskID,
			MergedResult:    v.MergedOutput,
			GapSummary:      v.GapSummary,
			FailedSubTasks:  failedIDs,
			CorrectionCount: totalCorrections,
			GapTrend:        gapTrend,
			Recommendation:  v.Recommendation,
		}
		log.Printf("[R4b] task=%s requesting REPLAN gap_trend=%s", taskID, gapTrend)
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RolePlanner,
			Type:      types.MsgReplanRequest,
			Payload:   rr,
		})

		// Reset tracker for next round
		m.mu.Lock()
		tracker.outcomes = nil
		m.mu.Unlock()
	}
}

func computeGapTrend(currentFailed, prevFailed, replanCount int) string {
	if replanCount == 0 {
		return "stable"
	}
	if currentFailed < prevFailed {
		return "improving"
	}
	if currentFailed > prevFailed {
		return "worsening"
	}
	return "stable"
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
