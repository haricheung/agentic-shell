package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/types"
)

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

Prior memory entries (if any):
%s

Rules:
- Do NOT repeat the same approach that already failed (gap_summary and failed_subtasks describe what went wrong).
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
	memJSON, _ := json.MarshalIndent(memory, "", "  ")

	userPrompt := fmt.Sprintf("TaskSpec:\n%s\n\nPrior memory entries:\n%s", specJSON, memJSON)
	return p.dispatch(ctx, spec, userPrompt, systemPrompt)
}

func (p *Planner) replan(ctx context.Context, spec types.TaskSpec, rr types.ReplanRequest, memory []types.MemoryEntry) error {
	rrJSON, _ := json.MarshalIndent(rr, "", "  ")
	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	memJSON, _ := json.MarshalIndent(memory, "", "  ")

	userPrompt := fmt.Sprintf(replanPrompt, rrJSON, specJSON, memJSON)
	return p.dispatch(ctx, spec, userPrompt, systemPrompt)
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
