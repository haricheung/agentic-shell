package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/types"
)

const maxMemoryEntries = 10

const systemPrompt = `You are R2 — Planner. Decompose a TaskSpec into the minimum necessary SubTask objects.

Decomposition rules:
- PREFER one SubTask for any simple operation (single lookup, single command, single file op).
- Split into multiple SubTasks ONLY when steps are genuinely independent or must be ordered.
- Fewer SubTasks = fewer LLM calls = faster results.

Sequence rules (critical):
- Same sequence number → subtasks run IN PARALLEL (no data dependency between them).
- Different sequence numbers → subtasks run IN ORDER. Use this when subtask B needs output from subtask A.
  Example: sequence=1 "locate file", sequence=2 "extract audio from located file".
  The dispatcher injects the outputs of sequence N into every sequence N+1 subtask's context automatically.
- Start sequence numbering at 1.

Context field rules:
- Always populate context with everything the executor needs beyond the intent: known file paths, format requirements, constraints, relevant memory.
- For sequence N+1 subtasks, you do NOT need to repeat how to find a file already located in sequence N — the dispatcher will inject prior outputs.

Memory constraint rules (when a MEMORY CONSTRAINTS block is present):
- Every "MUST NOT" line records an approach that failed before for a similar task. You MUST NOT use that approach regardless of how promising it seems.
- Every "SHOULD PREFER" line records an approach that worked before. Prefer it over untested alternatives.

Output ONLY a JSON array (no wrapper, no markdown, no prose):
[
  {
    "subtask_id": "<uuid>",
    "parent_task_id": "...",
    "intent": "<one-sentence action>",
    "success_criteria": ["<verifiable from tool output>"],
    "context": "<relevant background, constraints, known paths>",
    "deadline": null,
    "sequence": 1
  }
]

Generate a fresh UUID string for each subtask_id.`

const replanPrompt = `You are R2 — Planner. A ReplanRequest has been received. Generate a revised decomposition that addresses the identified gaps.

ReplanRequest:
%s

Original TaskSpec:
%s

Memory Constraints (code-derived — MUST NOT constraints are mandatory):
%s

Rules:
- Do NOT repeat the same approach that already failed (gap_summary and failed_subtasks describe what went wrong).
- You MUST respect every MUST NOT constraint above — these record approaches that failed on prior tasks.
- Apply the same sequence, context, and decomposition rules as the initial plan.
- Output ONLY a JSON array of SubTask objects as specified in your system prompt.`

// Planner is R2. It decomposes TaskSpec into SubTasks and handles replanning.
type Planner struct {
	llm *llm.Client
	b   *bus.Bus
}

// New creates a Planner.
func New(b *bus.Bus, llmClient *llm.Client) *Planner {
	return &Planner{llm: llmClient, b: b}
}

// Run listens for TaskSpec and ReplanRequest messages.
func (p *Planner) Run(ctx context.Context) {
	taskSpecCh := p.b.Subscribe(types.MsgTaskSpec)
	replanCh := p.b.Subscribe(types.MsgReplanRequest)
	memoryCh := p.b.Subscribe(types.MsgMemoryResponse)

	// pendingTaskSpecs holds the current TaskSpec awaiting planning
	var currentSpec *types.TaskSpec
	var memoryEntries []types.MemoryEntry
	var awaitingMemory bool

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-taskSpecCh:
			if !ok {
				return
			}
			spec, err := toTaskSpec(msg.Payload)
			if err != nil {
				log.Printf("[R2] ERROR: bad TaskSpec payload: %v", err)
				continue
			}
			log.Printf("[R2] received TaskSpec task_id=%s", spec.TaskID)
			currentSpec = &spec
			memoryEntries = nil

			// Query memory before planning
			p.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RolePlanner,
				To:        types.RoleMemory,
				Type:      types.MsgMemoryRead,
				Payload: types.MemoryQuery{
					Query: spec.Intent,
				},
			})
			awaitingMemory = true

		case msg, ok := <-memoryCh:
			if !ok {
				return
			}
			if !awaitingMemory || currentSpec == nil {
				continue
			}
			resp, err := toMemoryResponse(msg.Payload)
			if err != nil {
				log.Printf("[R2] ERROR: bad MemoryResponse payload: %v", err)
			} else {
				memoryEntries = resp.Entries
			}
			awaitingMemory = false
			if err := p.plan(ctx, *currentSpec, memoryEntries); err != nil {
				log.Printf("[R2] ERROR: planning failed: %v", err)
			}

		case msg, ok := <-replanCh:
			if !ok {
				return
			}
			rr, err := toReplanRequest(msg.Payload)
			if err != nil {
				log.Printf("[R2] ERROR: bad ReplanRequest payload: %v", err)
				continue
			}
			log.Printf("[R2] received ReplanRequest task_id=%s gap_trend=%s", rr.TaskID, rr.GapTrend)

			if rr.Recommendation == "abandon" {
				log.Printf("[R2] task %s: abandoning per ReplanRequest recommendation", rr.TaskID)
				continue
			}

			if currentSpec == nil {
				log.Printf("[R2] WARNING: ReplanRequest received but no current TaskSpec")
				continue
			}

			if err := p.replan(ctx, *currentSpec, rr, memoryEntries); err != nil {
				log.Printf("[R2] ERROR: replanning failed: %v", err)
			}
		}
	}
}

func (p *Planner) plan(ctx context.Context, spec types.TaskSpec, memory []types.MemoryEntry) error {
	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	constraints := calibrate(memory, spec.Intent)

	var userPrompt string
	if constraints != "" {
		log.Printf("[R2] calibration: injecting constraints from %d memory entries", len(memory))
		userPrompt = fmt.Sprintf(
			"TaskSpec:\n%s\n\n--- MEMORY CONSTRAINTS (code-derived) ---\n%s--- END CONSTRAINTS ---",
			specJSON, constraints)
	} else {
		log.Printf("[R2] calibration: no relevant memory entries")
		userPrompt = fmt.Sprintf("TaskSpec:\n%s", specJSON)
	}
	return p.dispatch(ctx, spec, userPrompt, systemPrompt)
}

func (p *Planner) replan(ctx context.Context, spec types.TaskSpec, rr types.ReplanRequest, memory []types.MemoryEntry) error {
	rrJSON, _ := json.MarshalIndent(rr, "", "  ")
	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	constraints := calibrate(memory, spec.Intent)
	if constraints == "" {
		constraints = "(none)"
	}
	userPrompt := fmt.Sprintf(replanPrompt, rrJSON, specJSON, constraints)
	return p.dispatch(ctx, spec, userPrompt, systemPrompt)
}

// calibrate implements Steps 1–3 of the Memory Calibration Protocol.
// Step 1 — Retrieve: caller provides entries already fetched from R5 (no LLM call).
// Step 2 — Calibrate: sort by recency (newest first), cap at maxMemoryEntries,
//
//	keyword-filter against current intent (discard zero-overlap entries).
//
// Step 3 — Constrain: derive MUST NOT (procedural) and SHOULD PREFER (episodic) lines.
// Returns an empty string when no relevant entries exist.
func calibrate(entries []types.MemoryEntry, intent string) string {
	if len(entries) == 0 {
		return ""
	}

	// Step 2 — sort newest first (ISO8601 timestamps sort lexicographically)
	sorted := make([]types.MemoryEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp > sorted[j].Timestamp
	})
	if len(sorted) > maxMemoryEntries {
		sorted = sorted[:maxMemoryEntries]
	}

	// Step 2 — keyword filter: keep entries with any keyword overlap against intent
	intentKW := memTokenize(intent)
	var relevant []types.MemoryEntry
	for _, e := range sorted {
		raw, _ := json.Marshal(e)
		haystack := strings.ToLower(string(raw))
		for _, kw := range intentKW {
			if strings.Contains(haystack, kw) {
				relevant = append(relevant, e)
				break
			}
		}
	}
	if len(relevant) == 0 {
		return ""
	}

	// Step 3 — derive constraint lines
	var mustNots, shouldPrefers []string
	for _, e := range relevant {
		line := "  - " + entrySummary(e)
		switch e.Type {
		case "procedural":
			mustNots = append(mustNots, line)
		case "episodic":
			shouldPrefers = append(shouldPrefers, line)
		}
	}

	var sb strings.Builder
	if len(mustNots) > 0 {
		sb.WriteString("MUST NOT (prior failures — do not repeat these approaches):\n")
		for _, c := range mustNots {
			sb.WriteString(c + "\n")
		}
	}
	if len(shouldPrefers) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("SHOULD PREFER (prior successes — these approaches worked):\n")
		for _, c := range shouldPrefers {
			sb.WriteString(c + "\n")
		}
	}
	return sb.String()
}

// entrySummary produces a short readable description of a memory entry for constraint text.
func entrySummary(e types.MemoryEntry) string {
	raw, _ := json.Marshal(e.Content)
	s := string(raw)
	if len(s) > 180 {
		s = s[:180] + "…"
	}
	if len(e.Tags) > 0 {
		return fmt.Sprintf("[tags: %s] %s", strings.Join(e.Tags, ", "), s)
	}
	return s
}

// memTokenize splits s into lowercase keywords of length >= 3.
func memTokenize(s string) []string {
	var words []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if len(w) >= 3 {
			words = append(words, w)
		}
	}
	return words
}

func (p *Planner) dispatch(ctx context.Context, spec types.TaskSpec, userPrompt, sysPrompt string) error {
	raw, err := p.llm.Chat(ctx, sysPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	raw = llm.StripFences(raw)

	var subTasks []types.SubTask
	if err := json.Unmarshal([]byte(raw), &subTasks); err != nil {
		return fmt.Errorf("parse SubTasks: %w (raw: %s)", err, raw)
	}

	if len(subTasks) == 0 {
		return fmt.Errorf("planner returned 0 sub-tasks")
	}

	// Assign IDs and parent
	subtaskIDs := make([]string, 0, len(subTasks))
	for i := range subTasks {
		if subTasks[i].SubTaskID == "" {
			subTasks[i].SubTaskID = uuid.New().String()
		}
		subTasks[i].ParentTaskID = spec.TaskID
		subtaskIDs = append(subtaskIDs, subTasks[i].SubTaskID)
	}

	// Publish manifest first so R4b knows expected count
	manifest := types.DispatchManifest{
		TaskID:       spec.TaskID,
		SubTaskIDs:   subtaskIDs,
		TaskSpec:     &spec,
		DispatchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	p.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RolePlanner,
		To:        types.RoleMetaVal,
		Type:      types.MsgDispatchManifest,
		Payload:   manifest,
	})
	log.Printf("[R2] dispatched manifest task_id=%s subtasks=%d", spec.TaskID, len(subTasks))

	// Fan-out sub-tasks to executor
	for _, st := range subTasks {
		p.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RolePlanner,
			To:        types.RoleExecutor,
			Type:      types.MsgSubTask,
			Payload:   st,
		})
		log.Printf("[R2] dispatched subtask=%s sequence=%d", st.SubTaskID, st.Sequence)
	}

	return nil
}

func toTaskSpec(payload any) (types.TaskSpec, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.TaskSpec{}, err
	}
	var s types.TaskSpec
	return s, json.Unmarshal(b, &s)
}

func toReplanRequest(payload any) (types.ReplanRequest, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.ReplanRequest{}, err
	}
	var r types.ReplanRequest
	return r, json.Unmarshal(b, &r)
}

func toMemoryResponse(payload any) (types.MemoryResponse, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.MemoryResponse{}, err
	}
	var r types.MemoryResponse
	return r, json.Unmarshal(b, &r)
}
