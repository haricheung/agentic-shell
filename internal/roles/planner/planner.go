package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/tasklog"
	"github.com/haricheung/agentic-shell/internal/types"
)

const (
	maxMemoryEntries = 10
	maxCCCalls       = 2 // max times R2 may call cc per planning session
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

OPTIONAL TOOL — cc (Claude Code):
Before finalising the SubTask array you may consult the local Claude Code assistant when:
- The task involves project code/files and you need to understand structure or APIs.
- You are genuinely uncertain how to decompose a step correctly.
- You want to verify that a proposed tool or command will work in this codebase.

To call cc, output EXACTLY this JSON object (nothing else on the turn):
{"action":"call_cc","prompt":"<your specific question>"}

You will receive cc's response and may call it again (limit: 2 times total).
Only call cc when it materially improves planning — skip it for simple tasks.

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
- "refine": tighten parameters; keep the same approach. Loss is decreasing — you are on the right track.
- "change_path": same tool sequence, different target/parameters. Environment blocked the path, not the logic.
- "change_approach": use a clearly different tool class. The current approach is logically wrong.
- "break_symmetry": the system is stuck in a local minimum. You MUST NOT reuse any tool in blocked_tools. Demand a novel approach unlike anything tried before.

Rules:
- You MUST follow the directive field above — this overrides your own judgment about the best approach.
- You MUST NOT use any tool listed in blocked_tools — this is code-enforced.
- Do NOT repeat the failed subtask approach described in rationale.
- Apply the same sequence, context, and decomposition rules as the initial plan.
- Output ONLY the JSON wrapper object (task_criteria + subtasks) as specified in your system prompt.`

// Planner is R2. It decomposes TaskSpec into SubTasks and handles replanning.
type Planner struct {
	llm       *llm.Client
	b         *bus.Bus
	logReg    *tasklog.Registry
	mu        sync.RWMutex
	brainMode string // "llm" (default) or "cc"
}

// New creates a Planner. brainMode is "llm" or "cc"; empty defaults to "llm".
func New(b *bus.Bus, llmClient *llm.Client, logReg *tasklog.Registry, brainMode string) *Planner {
	if brainMode != "cc" {
		brainMode = "llm"
	}
	return &Planner{llm: llmClient, b: b, logReg: logReg, brainMode: brainMode}
}

// SetBrainMode switches R2's planning engine at runtime. Accepted values: "llm", "cc".
func (p *Planner) SetBrainMode(mode string) {
	if mode != "cc" {
		mode = "llm"
	}
	p.mu.Lock()
	p.brainMode = mode
	p.mu.Unlock()
	log.Printf("[R2] brain mode switched to %q", mode)
}

// BrainMode returns the current planning engine ("llm" or "cc").
func (p *Planner) BrainMode() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.brainMode
}

// Run listens for TaskSpec and PlanDirective messages.
func (p *Planner) Run(ctx context.Context) {
	taskSpecCh := p.b.Subscribe(types.MsgTaskSpec)
	directiveCh := p.b.Subscribe(types.MsgPlanDirective)
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

		case msg, ok := <-directiveCh:
			if !ok {
				return
			}
			pd, err := toPlanDirective(msg.Payload)
			if err != nil {
				log.Printf("[R2] ERROR: bad PlanDirective payload: %v", err)
				continue
			}
			log.Printf("[R2] received PlanDirective task_id=%s directive=%s gradient=%s Ω=%.3f",
				pd.TaskID, pd.Directive, pd.Gradient, pd.BudgetPressure)

			if currentSpec == nil {
				log.Printf("[R2] WARNING: PlanDirective received but no current TaskSpec")
				continue
			}

			if err := p.replanWithDirective(ctx, *currentSpec, pd, memoryEntries); err != nil {
				log.Printf("[R2] ERROR: replanning failed: %v", err)
			}
		}
	}
}

func (p *Planner) plan(ctx context.Context, spec types.TaskSpec, memory []types.MemoryEntry) error {
	// Open (or retrieve existing) task log — idempotent across replan rounds.
	tl := p.logReg.Open(spec.TaskID, spec.Intent)

	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	constraints := calibrate(memory, spec.Intent)

	today := time.Now().UTC().Format("2006-01-02")
	var userPrompt string
	if constraints != "" {
		log.Printf("[R2] calibration: injecting constraints from %d memory entries", len(memory))
		userPrompt = fmt.Sprintf(
			"Today's date: %s\n\nTaskSpec:\n%s\n\n--- MEMORY CONSTRAINTS (code-derived) ---\n%s--- END CONSTRAINTS ---",
			today, specJSON, constraints)
	} else {
		log.Printf("[R2] calibration: no relevant memory entries")
		userPrompt = fmt.Sprintf("Today's date: %s\n\nTaskSpec:\n%s", today, specJSON)
	}
	return p.dispatch(ctx, spec, userPrompt, systemPrompt, tl)
}

// replanWithDirective is called when R2 receives a PlanDirective from GGS (v0.7+).
// It merges GGS blocked_tools into the MUST NOT constraint set and applies the directive.
func (p *Planner) replanWithDirective(ctx context.Context, spec types.TaskSpec, pd types.PlanDirective, memory []types.MemoryEntry) error {
	tl := p.logReg.Get(spec.TaskID) // log already open from initial plan()

	pdJSON, _ := json.MarshalIndent(pd, "", "  ")
	specJSON, _ := json.MarshalIndent(spec, "", "  ")

	// Merge memory-sourced constraints with GGS blocked_tools.
	constraints := calibrate(memory, spec.Intent)
	if len(pd.BlockedTools) > 0 {
		ggsBlock := "MUST NOT (GGS blocked_tools — dynamic, for this task only):\n"
		for _, t := range pd.BlockedTools {
			ggsBlock += "  - Do not use tool: " + t + "\n"
		}
		if constraints == "" {
			constraints = ggsBlock
		} else {
			constraints = ggsBlock + "\n" + constraints
		}
	}
	if constraints == "" {
		constraints = "(none)"
	}

	today := time.Now().UTC().Format("2006-01-02")
	userPrompt := "Today's date: " + today + "\n\n" + fmt.Sprintf(planDirectivePrompt, pdJSON, specJSON, constraints)
	return p.dispatch(ctx, spec, userPrompt, systemPrompt, tl)
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
//   - Truncates content JSON at 180 chars, appending "…" when trimmed
//   - Prepends "[tags: t1, t2] " when tags are present
//   - Returns raw content JSON with no prefix when tags are empty
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

// ccEnviron returns os.Environ() with CLAUDECODE removed so that cc can
// launch as a subprocess even when agsh itself is running inside a Claude Code
// session (cc refuses to start when CLAUDECODE is set in the environment).
func ccEnviron() []string {
	env := os.Environ()
	out := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			out = append(out, e)
		}
	}
	return out
}

// runCC invokes the local Claude Code CLI ("cc --print <prompt>") and returns
// its trimmed stdout as a plain string. It is used by R2 to consult Claude Code
// during planning when the task involves code or file structure analysis.
//
// Expectations:
//   - Returns trimmed stdout when cc exits successfully (exit code 0)
//   - Returns "[cc error: <msg>]" string (not a Go error) when cc is unavailable or exits non-zero
//   - Truncates output at 4000 chars, appending "…" when trimmed
//   - Respects ctx cancellation; times out after 60 s regardless
//   - Unsets CLAUDECODE env var so cc can launch inside a Claude Code session
func runCC(ctx context.Context, prompt string) string {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// Use zsh -i so shell aliases (e.g. cc=…claude) are loaded.
	// Prompt is passed via AGSH_PROMPT to avoid any shell-injection risk.
	cmd := exec.CommandContext(ctx, "zsh", "-i", "-c", `cc --print "$AGSH_PROMPT"`)
	cmd.Env = append(ccEnviron(), "AGSH_PROMPT="+prompt)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("[cc error: %v]", err)
	}
	result := strings.TrimSpace(string(out))
	if len(result) > 4000 {
		result = result[:4000] + "…"
	}
	return result
}

// dispatch runs the brain LLM with an optional cc tool loop.
// dispatch routes to the active brain: LLM (default) or cc.
func (p *Planner) dispatch(ctx context.Context, spec types.TaskSpec, userPrompt, sysPrompt string, tl *tasklog.TaskLog) error {
	p.mu.RLock()
	brain := p.brainMode
	p.mu.RUnlock()
	if brain == "cc" {
		return p.dispatchViaCCBrain(ctx, spec, userPrompt, sysPrompt)
	}
	return p.dispatchViaLLM(ctx, spec, userPrompt, sysPrompt, tl)
}

// dispatchViaLLM drives the LLM planning loop. The LLM may call cc up to
// maxCCCalls times before producing the final SubTask array.
//
// Expectations:
//   - Parses and emits SubTask array directly when LLM skips the cc tool
//   - Calls runCC and feeds result back when LLM outputs {"action":"call_cc","prompt":"..."}
//   - Hard-stops after maxCCCalls cc calls and requests final plan
//   - Falls back to SubTask JSON parse when output does not match the cc action shape
func (p *Planner) dispatchViaLLM(ctx context.Context, spec types.TaskSpec, userPrompt, sysPrompt string, tl *tasklog.TaskLog) error {
	currentUser := userPrompt
	var ccHistory strings.Builder
	ccCalls := 0

	for {
		raw, usage, err := p.llm.Chat(ctx, sysPrompt, currentUser)
		tl.LLMCall("planner", sysPrompt, currentUser, raw, usage.PromptTokens, usage.CompletionTokens, 0)
		if err != nil {
			return fmt.Errorf("llm: %w", err)
		}
		raw = llm.StripFences(raw)

		// Attempt to detect a cc tool call before treating output as final plan.
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, "[") && ccCalls < maxCCCalls {
			var act struct {
				Action string `json:"action"`
				Prompt string `json:"prompt"`
			}
			if json.Unmarshal([]byte(trimmed), &act) == nil && act.Action == "call_cc" && act.Prompt != "" {
				ccCalls++
				log.Printf("[R2] cc consultation %d/%d: %q", ccCalls, maxCCCalls, act.Prompt)
				p.b.Publish(types.Message{
					ID:        uuid.New().String(),
					Timestamp: time.Now().UTC(),
					From:      types.RolePlanner,
					To:        types.RoleCC,
					Type:      types.MsgCCCall,
					Payload:   types.CCCall{TaskID: spec.TaskID, CallN: ccCalls, MaxN: maxCCCalls, Prompt: act.Prompt},
				})
				ccOut := runCC(ctx, act.Prompt)
				log.Printf("[R2] cc response (%d chars): %.300s", len(ccOut), ccOut)
				preview := ccOut
				if len([]rune(preview)) > 300 {
					preview = string([]rune(preview)[:300]) + "…"
				}
				p.b.Publish(types.Message{
					ID:        uuid.New().String(),
					Timestamp: time.Now().UTC(),
					From:      types.RoleCC,
					To:        types.RolePlanner,
					Type:      types.MsgCCResponse,
					Payload:   types.CCResponse{TaskID: spec.TaskID, CallN: ccCalls, Chars: len(ccOut), Response: preview},
				})
				ccHistory.WriteString(fmt.Sprintf("\n\n[cc call %d]\nQ: %s\nA: %s", ccCalls, act.Prompt, ccOut))
				currentUser = userPrompt + ccHistory.String() + "\n\nNow output the final JSON wrapper object (task_criteria + subtasks):"
				continue
			}
		}

		// Final plan — parse and emit.
		return p.emitSubTasks(spec, raw, ccCalls, "llm")
	}
}

// dispatchViaCCBrain uses cc as the primary planning engine. The full system +
// user prompt is passed to cc --print; the response is expected to be a SubTask JSON array.
func (p *Planner) dispatchViaCCBrain(ctx context.Context, spec types.TaskSpec, userPrompt, sysPrompt string) error {
	fullPrompt := sysPrompt + "\n\n" + userPrompt
	log.Printf("[R2] cc-brain planning task=%s", spec.TaskID)
	log.Printf("[R2] ── CC-BRAIN PROMPT ────────────────────────────\n%s\n── END CC-BRAIN PROMPT ─────────────────────────", fullPrompt)

	execCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	// Use zsh -i so shell aliases (e.g. cc=…claude) are loaded.
	// Prompt is passed via AGSH_PROMPT to avoid any shell-injection risk.
	cmd := exec.CommandContext(execCtx, "zsh", "-i", "-c", `cc --print "$AGSH_PROMPT"`)
	cmd.Env = append(ccEnviron(), "AGSH_PROMPT="+fullPrompt)
	out, err := cmd.Output()
	var raw string
	if err != nil {
		log.Printf("[R2] cc-brain error: %v", err)
		raw = fmt.Sprintf("[cc error: %v]", err)
	} else {
		raw = strings.TrimSpace(string(out))
	}
	raw = llm.StripFences(raw)
	log.Printf("[R2] ── CC-BRAIN RESPONSE (%d chars) ─────────────────\n%s\n── END CC-BRAIN RESPONSE ───────────────────────", len(raw), raw)
	return p.emitSubTasks(spec, raw, 0, "cc")
}

// emitSubTasks parses a raw SubTask plan (wrapper or array) and fans it out on the bus.
// It first attempts to parse the new wrapper format {"task_criteria":[...],"subtasks":[...]};
// if that fails it falls back to the legacy raw JSON array for backward compatibility.
func (p *Planner) emitSubTasks(spec types.TaskSpec, raw string, ccCalls int, plannerBrain string) error {
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
		PlannerBrain: plannerBrain,
		CCCalls:      ccCalls,
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
		criteriaJSON, _ := json.Marshal(st.SuccessCriteria)
		log.Printf("[R2] dispatched subtask=%s sequence=%d intent=%q criteria(%d)=%s",
			st.SubTaskID, st.Sequence, st.Intent, len(st.SuccessCriteria), criteriaJSON)
		for i, c := range st.SuccessCriteria {
			log.Printf("[R2]   [%d] %q", i+1, c)
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

func toMemoryResponse(payload any) (types.MemoryResponse, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.MemoryResponse{}, err
	}
	var r types.MemoryResponse
	return r, json.Unmarshal(b, &r)
}
