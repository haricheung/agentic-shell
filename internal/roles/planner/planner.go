package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/roles/memory"
	"github.com/haricheung/agentic-shell/internal/tasklog"
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
- Do NOT suggest third-party CLI tools (cloc, tokei, jq, ripgrep, fd, bat, etc.) — use only standard Unix commands (find, wc, grep, awk, sed, sort, du) or the executor's built-in tools (mdfind, glob, shell, search).

Success criteria rules (critical):
- Each criterion MUST be a concrete, checkable assertion about tool output — NOT a restatement of the intent.
- Bad (intent echo):  "get today's day of the week"
- Good (assertion):   "output explicitly states which day of the week today is (e.g. Monday / 星期一)"
- Bad (intent echo):  "find the audio file"
- Good (assertion):   "output contains a valid absolute file path ending in .mp3 or .m4a"
- Bad (intent echo):  "check PM2.5"
- Good (assertion):   "output contains a numeric PM2.5 value"
- Criteria must be falsifiable: a validator reading only the tool output must be able to say pass or fail.

Task criteria rules (critical):
- task_criteria apply to the MERGED output of ALL subtasks — not to individual steps.
- Same quality bar as subtask success_criteria: concrete, falsifiable assertions.
- Bad: "all subtasks completed successfully"
- Good: "merged output contains a valid absolute file path ending in .mp3 or .m4a"

Memory constraint rules (when a MEMORY CONSTRAINTS block is present):
- Every "MUST NOT" line records an approach that failed before for a similar task. You MUST NOT use that approach regardless of how promising it seems.
- Every "SHOULD PREFER" line records an approach that worked before. Prefer it over untested alternatives.

When ready to finalise, output ONLY this JSON object (no markdown, no prose):
{
  "task_criteria": ["<assertion about the COMBINED output of ALL subtasks>"],
  "subtasks": [
    {
      "subtask_id": "<uuid>",
      "parent_task_id": "...",
      "intent": "<one-sentence action>",
      "success_criteria": ["<assertion checkable against tool output>"],
      "context": "<relevant background, constraints, known paths>",
      "deadline": null,
      "sequence": 1
    }
  ]
}

Generate a fresh UUID string for each subtask_id.`

const planDirectivePrompt = `You are R2 — Planner. A PlanDirective from GGS (Goal Gradient Solver) requires a revised decomposition.

PlanDirective:
%s

Original TaskSpec:
%s

Memory + GGS Constraints (code-derived — ALL constraints are mandatory):
%s

Directive semantics:
- "refine": tighten parameters; keep the same approach. The approach is sound — adjust path or search terms.
- "change_path": same tool sequence, different target/parameters. Environment blocked the path, not the logic.
- "change_approach": use a clearly different tool class. The current approach is logically wrong.
- "break_symmetry": the system is stuck in a local minimum. You MUST NOT reuse any tool in blocked_tools. Demand a novel approach unlike anything tried before.

Failure class guidance (from failure_class field in PlanDirective):
- "environmental": approach is sound but the specific path/target is blocked.
  Change target, parameters, or search terms — NOT the algorithm or tool class.
- "logical": the algorithm or approach itself is wrong.
  Change the tool class or method entirely — NOT just the search terms.
- "mixed": both present — fix the environmental blockers first, then reassess approach.

Rules:
- You MUST follow the directive field above — this overrides your own judgment about the best approach.
- You MUST NOT use any tool listed in blocked_tools — this is code-enforced.
- Do NOT repeat the failed subtask approach described in rationale.
- Apply the same sequence, context, and decomposition rules as the initial plan.
- Output ONLY the JSON wrapper object (task_criteria + subtasks) as specified in your system prompt.`

// Planner is R2. It decomposes TaskSpec into SubTasks and handles replanning.
type Planner struct {
	llm    *llm.Client
	b      *bus.Bus
	logReg *tasklog.Registry
	mem    types.MemoryService // R5; may be nil (memory disabled)
}

// New creates a Planner. mem may be nil to disable MKCT memory queries (e.g. in tests).
func New(b *bus.Bus, llmClient *llm.Client, logReg *tasklog.Registry, mem types.MemoryService) *Planner {
	return &Planner{llm: llmClient, b: b, logReg: logReg, mem: mem}
}

// Run listens for TaskSpec and PlanDirective messages.
// Memory is queried synchronously via direct calls to R5 (no bus round-trip).
func (p *Planner) Run(ctx context.Context) {
	taskSpecCh := p.b.Subscribe(types.MsgTaskSpec)
	directiveCh := p.b.Subscribe(types.MsgPlanDirective)

	var currentSpec *types.TaskSpec

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
				slog.Error("[R2] bad TaskSpec payload", "error", err)
				continue
			}
			slog.Info("[R2] received TaskSpec", "task", spec.TaskID)
			currentSpec = &spec
			go func(s types.TaskSpec) {
				if err := p.plan(ctx, s); err != nil {
					slog.Error("[R2] planning failed", "error", err)
				}
			}(spec)

		case msg, ok := <-directiveCh:
			if !ok {
				return
			}
			pd, err := toPlanDirective(msg.Payload)
			if err != nil {
				slog.Error("[R2] bad PlanDirective payload", "error", err)
				continue
			}
			slog.Info("[R2] received PlanDirective", "task", pd.TaskID, "directive", pd.Directive, "prev", pd.PrevDirective, "budget_pressure", pd.BudgetPressure)

			if currentSpec == nil {
				slog.Warn("[R2] PlanDirective received but no current TaskSpec")
				continue
			}
			spec := *currentSpec
			go func(s types.TaskSpec, directive types.PlanDirective) {
				if err := p.replanWithDirective(ctx, s, directive); err != nil {
					slog.Error("[R2] replanning failed", "error", err)
				}
			}(spec, pd)
		}
	}
}

func (p *Planner) plan(ctx context.Context, spec types.TaskSpec) error {
	// Open (or retrieve existing) task log — idempotent across replan rounds.
	tl := p.logReg.Open(spec.TaskID, spec.Intent)

	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	constraints := p.queryMKCTConstraints(ctx, spec.Intent, tl)

	today := time.Now().UTC().Format("2006-01-02")
	var userPrompt string
	if constraints != "" {
		userPrompt = fmt.Sprintf(
			"Today's date: %s\n\nTaskSpec:\n%s\n\n--- MEMORY CONSTRAINTS (code-derived) ---\n%s--- END CONSTRAINTS ---",
			today, specJSON, constraints)
	} else {
		userPrompt = fmt.Sprintf("Today's date: %s\n\nTaskSpec:\n%s", today, specJSON)
	}
	return p.dispatch(ctx, spec, userPrompt, systemPrompt, tl)
}

// replanWithDirective is called when R2 receives a PlanDirective from GGS (v0.7+).
// It merges MKCT memory constraints with GGS blocked_tools and blocked_targets.
func (p *Planner) replanWithDirective(ctx context.Context, spec types.TaskSpec, pd types.PlanDirective) error {
	tl := p.logReg.Get(spec.TaskID) // log already open from initial plan()

	pdJSON, _ := json.MarshalIndent(pd, "", "  ")
	specJSON, _ := json.MarshalIndent(spec, "", "  ")

	// Query MKCT memory for this intent.
	constraints := p.queryMKCTConstraints(ctx, spec.Intent, tl)

	// blocked_tools: tool names to avoid (logical failure directives: break_symmetry, change_approach).
	if len(pd.BlockedTools) > 0 {
		ggsBlock := "MUST NOT (GGS blocked_tools — tool names that are logically wrong for this task):\n"
		for _, t := range pd.BlockedTools {
			ggsBlock += "  - Do not use tool: " + t + "\n"
		}
		if constraints == "" {
			constraints = ggsBlock
		} else {
			constraints = ggsBlock + "\n" + constraints
		}
	}

	// blocked_targets: specific failed inputs to avoid (environmental failure directives: change_path, refine).
	// These accumulate across all replan rounds — the full history of tried-and-blocked targets.
	if len(pd.BlockedTargets) > 0 {
		targBlock := "MUST NOT (GGS blocked_targets — specific inputs already tried and blocked by environment):\n"
		for _, t := range pd.BlockedTargets {
			targBlock += "  - Do not use this specific query/command/path again: " + t + "\n"
		}
		if constraints == "" {
			constraints = targBlock
		} else {
			constraints = targBlock + "\n" + constraints
		}
	}

	if constraints == "" {
		constraints = "(none)"
	}

	today := time.Now().UTC().Format("2006-01-02")
	userPrompt := "Today's date: " + today + "\n\n" + fmt.Sprintf(planDirectivePrompt, pdJSON, specJSON, constraints)
	return p.dispatch(ctx, spec, userPrompt, systemPrompt, tl)
}

// queryMKCTConstraints queries R5 for the given intent and returns a formatted
// constraint string for injection into the planning prompt. Returns "" when memory
// is disabled (mem == nil) or when no relevant entries exist.
//
// Expectations:
//   - Returns "" when p.mem is nil
//   - Returns "" when QueryC and QueryMK both return empty/Ignore results
//   - Derives space tag as memory.IntentSlug(intent); entity as "env:local"
//   - Includes "SHOULD PREFER" block when Action is Exploit
//   - Includes "MUST NOT" block when Action is Avoid
//   - Includes "CAUTION" block when Action is Caution
//   - Appends C-level SOPs as "SHOULD PREFER" (σ>0) or "MUST NOT" (σ<0) lines
//   - Logs a memory_query event to tl after computing constraints
func (p *Planner) queryMKCTConstraints(ctx context.Context, intent string, tl *tasklog.TaskLog) string {
	if p.mem == nil {
		return ""
	}
	space := memory.IntentSlug(intent)
	entity := "env:local"

	sops, err1 := p.mem.QueryC(ctx, space, entity)
	pots, err2 := p.mem.QueryMK(ctx, space, entity)
	if err1 != nil {
		slog.Warn("[R2] QueryC failed", "error", err1)
	}
	if err2 != nil {
		slog.Warn("[R2] QueryMK failed", "error", err2)
	}

	constraints := calibrateMKCT(sops, pots)
	tl.MemoryQuery(space, entity, len(sops), pots.Action, pots.Attention, pots.Decision)
	if constraints != "" {
		slog.Debug("[R2] MKCT calibration", "sops", len(sops), "action", pots.Action)
	} else {
		slog.Debug("[R2] MKCT calibration: no relevant memory", "action", pots.Action)
	}
	return constraints
}

// calibrateMKCT builds a planning constraint string from MKCT QueryC and QueryMK results.
// SOPs with σ>0 become SHOULD PREFER lines; SOPs with σ≤0 become MUST NOT lines.
// Potential action (Exploit/Avoid/Caution) adds a general heuristic line.
//
// Expectations:
//   - Returns "" when sops is empty and pots.Action is "Ignore"
//   - Includes "SHOULD PREFER" block when pots.Action is "Exploit"
//   - Includes "MUST NOT" block when pots.Action is "Avoid"
//   - Includes "CAUTION" block when pots.Action is "Caution"
//   - Positive-σ SOPs appear under "SHOULD PREFER (proven best practices)"
//   - Non-positive-σ SOPs appear under "MUST NOT (proven constraints)"
func calibrateMKCT(sops []types.SOPRecord, pots types.Potentials) string {
	var sb strings.Builder

	// General heuristic from dual-channel potentials.
	switch pots.Action {
	case "Exploit":
		sb.WriteString("SHOULD PREFER (memory: this approach worked well for similar tasks):\n")
		sb.WriteString("  - Follow the same general approach that succeeded previously.\n")
	case "Avoid":
		sb.WriteString("MUST NOT (memory: this approach consistently failed for similar tasks):\n")
		sb.WriteString("  - Do not repeat the approach that failed previously.\n")
	case "Caution":
		sb.WriteString("CAUTION (memory: mixed results for similar tasks):\n")
		sb.WriteString("  - Validate each step carefully before committing.\n")
	}

	// C-level SOPs: best practices (σ>0) and constraints (σ≤0).
	var mustNots, shouldPrefers []string
	for _, sop := range sops {
		line := "  - " + sop.Content
		if sop.Sigma > 0 {
			shouldPrefers = append(shouldPrefers, line)
		} else {
			mustNots = append(mustNots, line)
		}
	}
	if len(mustNots) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("MUST NOT (proven constraints from accumulated experience):\n")
		for _, c := range mustNots {
			sb.WriteString(c + "\n")
		}
	}
	if len(shouldPrefers) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("SHOULD PREFER (proven best practices from accumulated experience):\n")
		for _, c := range shouldPrefers {
			sb.WriteString(c + "\n")
		}
	}
	return sb.String()
}

// calibrate implements Steps 1–3 of the Memory Calibration Protocol.
// Step 1 — Retrieve: caller provides entries already fetched from R5 (no LLM call).
// Step 2 — Calibrate: sort by recency (newest first), cap at maxMemoryEntries,
//
//	keyword-filter against current intent (discard zero-overlap entries).
//
// Step 3 — Constrain: derive MUST NOT (procedural) and SHOULD PREFER (episodic) lines.
// Returns an empty string when no relevant entries exist.
//
// Expectations:
//   - Returns "" when entries is empty
//   - Sorts entries newest-first before applying cap (most recent lessons take priority)
//   - Caps to maxMemoryEntries; entries beyond the cap are silently dropped
//   - Drops entries with zero keyword overlap against intent (>= 3-char words)
//   - Returns "" when all entries are filtered by keyword or have unknown type
//   - Procedural entries appear under "MUST NOT" heading
//   - Episodic entries appear under "SHOULD PREFER" heading
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
//
// Expectations:
//   - Truncates content JSON at 400 chars, appending "…" when trimmed
//   - Prepends "[tags: t1, t2] " when tags are present
//   - Returns raw content JSON with no prefix when tags are empty
func entrySummary(e types.MemoryEntry) string {
	raw, _ := json.Marshal(e.Content)
	s := string(raw)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	if len(e.Tags) > 0 {
		return fmt.Sprintf("[tags: %s] %s", strings.Join(e.Tags, ", "), s)
	}
	return s
}

// memTokenize splits s into lowercase keywords of length >= 3.
//
// Expectations:
//   - Returns only words with len >= 3 (short noise words are discarded)
//   - All returned words are lowercase
//   - Returns nil for empty or whitespace-only input
func memTokenize(s string) []string {
	var words []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if len(w) >= 3 {
			words = append(words, w)
		}
	}
	return words
}

// dispatch drives the LLM planning loop.
//
// Expectations:
//   - Calls p.llm.Chat and parses the response as a SubTask plan
//   - Retries are handled externally (replanning); this function runs once per plan attempt
func (p *Planner) dispatch(ctx context.Context, spec types.TaskSpec, userPrompt, sysPrompt string, tl *tasklog.TaskLog) error {
	raw, usage, err := p.llm.Chat(ctx, sysPrompt, userPrompt)
	tl.LLMCall("planner", sysPrompt, userPrompt, raw, usage.PromptTokens, usage.CompletionTokens, usage.ElapsedMs, 0)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	return p.emitSubTasks(spec, llm.StripFences(raw))
}

// emitSubTasks parses a raw SubTask plan (wrapper or bare array) and fans it out on the bus.
// It first attempts the wrapper format {"task_criteria":[...],"subtasks":[...]};
// if that fails it falls back to a bare JSON array for backward compatibility.
func (p *Planner) emitSubTasks(spec types.TaskSpec, raw string) error {
	var subTasks []types.SubTask
	var taskCriteria []string

	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		var wrapper struct {
			TaskCriteria []string        `json:"task_criteria"`
			Subtasks     []types.SubTask `json:"subtasks"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapper); err == nil && len(wrapper.Subtasks) > 0 {
			subTasks = wrapper.Subtasks
			taskCriteria = wrapper.TaskCriteria
		}
	}
	if subTasks == nil {
		if err := json.Unmarshal([]byte(raw), &subTasks); err != nil {
			return fmt.Errorf("parse SubTasks: %w (raw: %s)", err, raw)
		}
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
		TaskCriteria: taskCriteria,
	}
	p.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RolePlanner,
		To:        types.RoleMetaVal,
		Type:      types.MsgDispatchManifest,
		Payload:   manifest,
	})
	slog.Info("[R2] dispatched manifest", "task", spec.TaskID, "subtasks", len(subTasks))

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
		slog.Debug("[R2] dispatched subtask", "subtask", st.SubTaskID, "seq", st.Sequence, "intent", st.Intent, "criteria_count", len(st.SuccessCriteria))
		for i, c := range st.SuccessCriteria {
			slog.Debug("[R2] criterion", "index", i+1, "criterion", c)
		}
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

func toPlanDirective(payload any) (types.PlanDirective, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.PlanDirective{}, err
	}
	var pd types.PlanDirective
	return pd, json.Unmarshal(b, &pd)
}

