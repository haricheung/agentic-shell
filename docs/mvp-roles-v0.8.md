# artoo — Multi-Agent Automation System

**Version**: 0.8
**Status**: Production
**Date**: 2026-02-27
**Authors**: artoo engineering team

[toc]

---

## Abstract

artoo is a multi-agent system for autonomous task execution on a local machine. A user submits a natural-language request; artoo decomposes it into subtasks, executes them in parallel or in sequence, validates each result against machine-checkable criteria, and replans when the result falls short — all without human intervention. The central innovation is a dual nested control loop borrowed from classical control theory: a fast inner loop (Executor + Agent-Validator) handles per-subtask correction in real time, while a slow outer loop (Goal Gradient Solver) computes a loss function over the full task outcome and issues mathematically grounded replanning directives. Experience from every task is encoded as Megrams and stored in the MKCT cognitive memory pyramid, which the Planner queries before each new task to exploit past successes and avoid past failures.

---

## Design Principles

### 1. Omniscience Replaces Negotiation

Human organizations negotiate because information is distributed — each member sees only their slice. This system eliminates that asymmetry entirely. The Metaagent is omniscient within its operational scope: it authored every subtask, holds the complete TaskSpec, observes every CorrectionSignal accumulating in the fast loop, receives every SubTaskOutcome with its full evidence trail, and has access to all prior memory. There is nothing to negotiate.

The consequence: replanning cycles are not about aligning partial views — they are **iterative discovery of what reality allows**. The plan is a hypothesis. Execution tests it against the real world. The gap signal is the reality check. This is a deliberate divergence from the biological model: the loop structure (decision → execution → correction) is borrowed; the communication patterns within the loop are not. Omniscience makes precise gradient computation possible and eliminates the need for peer-to-peer coordination entirely.

### 2. Nested Feedback Loops — One Pattern at Three Scales

The system's control structure is a single closed-loop pattern — **decision → execution → correction** — instantiated at three nested scales simultaneously:

| Scale | Decision | Execution | Correction | Criterion |
|---|---|---|---|---|
| Fast (action) | Agent-Validator issues retry feedback | Executor re-runs | Agent-Validator re-measures gap | Sub-task success criteria |
| Medium (task) | Goal Gradient Solver adjusts plan | Planner re-dispatches | Meta-Validator re-merges outcomes | User intent within plausible range |
| Slow (system) | Dreamer synthesizes new strategy | Next task planning | Next Meta-Validator cycle | Long-term quality across tasks |

These are not three separate mechanisms. The separation of scales ensures fast retries do not flood the planner, and system-level consolidation does not block execution.

### 3. GGS as Dynamic-Differential Controller

The GGS is the controller in the medium loop — not a replanner. The distinction is structural:

- A **replanner** reacts to the current failure snapshot and generates a new plan from scratch. It has no memory of how it got here.
- A **dynamic-differential controller** maintains a history of gap measurements across correction cycles. It computes the trajectory of the gap — shrinking, stable, or growing — and scales corrections by both the current error and its rate of change.

The gradient the GGS produces must preserve three properties:
1. **Directional** — names *what* to change and *in what direction*, not merely that something failed
2. **Compositional** — multiple subtask failures aggregated into one gradient (omniscience makes this possible)
3. **History-aware** — a criterion failing identically across all attempts generates a stronger gradient than one that varies, distinguishing systematic wrong assumptions from transient environmental failures

An implementation that produces a gradient without direction is a replanner. An implementation that ignores trajectory is a static replanner. Both lose the property that gives the GGS adaptive behavior.

Formally: GGS is the controller; Meta-Validator is the sensor; Planner is the actuator; Effector Agents are the plant. The convergence kill-switch (2× consecutive worsening → force `abandon`) enforces Law 2: continuing beyond the divergence point is resource destruction, not best effort.

### 4. Validation as the Primary Control Mechanism

There is no separate retry policy, timeout manager, or error handler. The Agent-Validator's gap measurement **is** the retry criterion. If the gap is non-zero, execution continues. If the gap cannot be closed, the outcome is reported as failed and the Meta-Validator decides what to do. This ensures the retry criterion never drifts from the success criterion — they are the same signal.

### 5. Independent Auditor with a Separate Principal

The Auditor's principal is the human operator, not any agent. This is a structural property, not an access-control setting. An auditor that can be instructed by the Planner is a subordinate with a reporting function, not an auditor.

Two properties must hold unconditionally:
- **Non-participation**: the Auditor never sends messages to any agent. The moment it issues a correction it becomes a second controller and breaks the loop structure.
- **Immutable isolated log**: the audit log is separate from Shared Memory. No agent can read, modify, or suppress it.

The operational feedback loops are self-contained by design — efficient, but producing a structural blind spot about the system's own behavior. The Auditor is the deliberate compensation: it provides the external view that the loops cannot provide about themselves.

### 6. Centralized Shared Memory

One persistent store rather than per-agent private memories. The choice follows directly from the ephemerality of Effector Agents: a private memory for an agent spawned fresh per subtask and terminated on completion is meaningless — there is no persistent identity to accumulate knowledge into. Centralized memory also prevents the O(n²) reconciliation problem: if N agents hold diverging private stores, aligning them requires N² comparisons and an arbitration mechanism. One shared store with one Dreamer for reorganization avoids this entirely.

Effector Agents never query Shared Memory directly. Access is **indirect**: R2 reads and calibrates memory at planning time, then injects the relevant context into each `SubTask.context` before dispatch. A direct memory query by an Executor would create an unobservable information path and duplicate calibration logic that belongs to R2.

### 7. Observable Message Bus

All communication between roles passes through a single shared message bus. No role may call another directly. This ensures: (a) every inter-role message is an observable event — the Auditor can tap the bus read-only without instrumenting individual roles; (b) roles are fully decoupled and independently testable; (c) the bus is non-blocking — slow subscribers drop messages rather than exerting back-pressure on publishers.

This is a first-class architectural constraint, not an infrastructure detail. It must be established before any role is implemented. Retrofitting observability after point-to-point calls are in place requires restructuring the entire communication layer.

### 8. Cognitive Memory Substrate

Raw task experience is encoded as Megrams — atomic tuples carrying a magnitude (f), valence (σ), and decay rate (k). Megrams accumulate in a LevelDB store keyed by (space, entity) tags. The Planner queries two convolution channels before each task: the Attention channel (unsigned energy — where to look) and the Decision channel (signed preference — what to do). Above a significance threshold, the Dreamer background engine promotes clusters of Megrams into timeless Common Sense (C-level) SOPs and demotes stale ones via Trust Bankruptcy. The memory layer never blocks the operational path — all writes are fire-and-forget.

### 9. The Four Laws

Priority is strict: a lower law may never override a higher one.

**Law 0 — Never deceive** *(highest priority)*
The system must never misrepresent what it actually did or achieved. This sits above all other laws because deception destroys the feedback signal that all three loops depend on: a system that fabricates success learns the wrong lesson, corrupts memory, and makes the next task harder. Honesty is non-negotiable even when the honest answer is "I failed."

**Law 1 — Never harm the user's environment without explicit confirmation**
The system must not take irreversible actions on the user's data or environment without the user explicitly authorizing that specific action. Reversible actions (reading files, running queries, creating new files) do not require confirmation.

**Law 2 — Best effort delivery** *(subject to Laws 0 and 1)*
The system must pursue the user's goal as hard as possible within the bounds set by Laws 0 and 1. Stop when gap_trend is worsening for 2 consecutive replans — continuing is not best effort, it is resource destruction with no convergence signal.

**Law 3 — Preserve own functioning capacity** *(subject to Laws 0, 1, and 2)*
The system must not degrade its own ability to function across tasks: convergence integrity (stop on divergence), memory integrity (never write a MemoryEntry that misattributes failure cause), and cost integrity (respect the time and token cost model).

### 10. Cost Model: Two Costs Only

Every architectural decision is evaluated against exactly two costs:

- **Time cost** — latency felt by the user. Dominated by the count of *sequential* LLM calls in the critical path. Parallel calls add token cost but not time cost. The minimum sequential chain is: R1 → R2 → R3 → R4a → R4b = 5 calls. Each fast-loop retry adds 2 (R3 + R4a). Each replan adds 2 more (R2 + R4b).
- **Token cost** — API cost and context window pressure. Dominated by context size per call multiplied by parallel call count. N subtasks dispatched in parallel = N × context tokens simultaneously.

The tension: parallelism reduces time cost but multiplies token cost. Every "inject more context" decision has an explicit cost to justify. Every design decision in this system should be able to answer: *does this add a sequential LLM call, and how many tokens does it add per call?* If both answers are "none", the decision is cost-free.

---

## System Architecture

```
FAST LOOP (inside each Effector Agent)
┌─────────────────────────────────────────┐
│  decision  │  execution  │  correction  │
│  [SubTask] │  Executor   │  Agent-Val.  │
│    (R2)    │    (R3)     │    (R4a)     │
└─────────────────────────────────────────┘
         plant = R3 │ sensor+controller = R4a

MEDIUM LOOP (inside Metaagent)
┌────────────────────────────────────────────────────────────────────┐
│    decision     │     execution      │  sensor  │   controller     │
│  Planner (R2)  │  Effector Agents   │  R4b     │   GGS (R7)       │
│  [receives     │  (fast loops       │          │   [computes L,    │
│  PlanDirective │   running inside]  │          │    ∇L, directive] │
│  from GGS]     │                    │          │                    │
└────────────────────────────────────────────────────────────────────┘
    plant = Effectors │ sensor = R4b │ controller = GGS (R7) │ actuator = R2

AUDITOR (lateral — outside both loops)
┌──────────────────────────────────────────────────────────┐
│  Observes all inter-role messages via message bus        │
│  Reports anomalies to human operator                     │
│  Cannot instruct any agent; cannot be instructed by any  │
└──────────────────────────────────────────────────────────┘
```

---

## Architectural Constraint: Observable Message Bus

All inter-role communications must pass through a shared message bus that the Auditor can tap as a read-only observer. Direct point-to-point calls between roles are not permitted — every message must be routable. The bus is non-blocking: slow subscribers drop messages with a logged warning rather than exerting back-pressure on publishers.

---

## Role Index

| ID | Role | Layer | Loop Position | Accountability |
|---|---|---|---|---|
| R1 | Perceiver | Entry point | Reference signal | If the task is misunderstood, this role is responsible |
| R2 | Planner | Metaagent | Actuator | If the goal is not achieved despite valid execution, this role is responsible |
| R3 | Executor | Effector Agent | Plant (fast loop) | If a feasible sub-task is not correctly executed, this role is responsible |
| R4a | Agent-Validator | Effector Agent | Sensor + Controller (fast loop) | If a gap between outcome and sub-task goal goes unresolved or unreported, this role is responsible |
| R4b | Meta-Validator | Metaagent | Sensor (medium loop) | If the merged result is accepted outside plausible range or a task is silently abandoned, this role is responsible |
| R5 | Shared Memory | Infrastructure | Cognitive substrate | If valid data is lost, corrupted, or wrongly retrieved, this role is responsible |
| R6 | Auditor | Infrastructure | Lateral observer | If systematic failures go undetected and unreported to the human operator, this role is responsible |
| R7 | Goal Gradient Solver | Metaagent | Controller (medium loop) | If the replanning direction is wrong, too conservative, or too aggressive for the observed gradient, this role is responsible |

---

## R1 — Perceiver

**Mission**: Receive the user's signal and carry it into the system with full fidelity. R1 is a receiver, not a resolver. It translates free-form natural language into a structured TaskSpec without adding assumptions, defaulting success criteria, or interpreting ambiguity. Session context allows follow-up inputs to resolve against prior turns.

**Loop position**: Reference signal generator. Upstream of both control loops.

### Input Contract

- Free-form text from the user (REPL or one-shot CLI)
- Rolling session history: last 5 `{input, result.Summary}` pairs

### Output Contract

```json
TaskSpec {
  "task_id":     "short_snake_case_string",
  "intent":      "string — the user's goal, faithfully restated",
  "constraints": {
    "scope":    "string | null",
    "deadline": "ISO8601 | null"
  },
  "raw_input":   "string — verbatim user input"
}
```

### Skills

- Translate free-form text into a TaskSpec with a short snake_case `task_id`
- Resolve pronouns and references ("do it again", "that file", "wrong") against session history via `buildSessionContext()`
- Preserve the user's intent without narrowing, expanding, or reframing it
- Detect follow-up corrections and re-interpret in light of prior task outcome

### Does NOT

- Set `success_criteria` — criteria are R2's responsibility
- Make tool choices or suggest execution strategies
- Modify the user's intent based on perceived feasibility
- Persist any state (session history is maintained by the entry point, not R1)

---

## R2 — Planner

**Mission**: Interpret the user's intent and own the path to its realisation. R2 translates a TaskSpec into a concrete execution plan — a set of subtasks with criteria, sequence constraints, and context. Before planning, R2 queries the MKCT memory pyramid to exploit past successes and avoid past failures. When replanning, R2 operates under hard constraints injected by GGS.

**Loop position**: Actuator of the medium loop. Receives PlanDirective from R7 (GGS) and issues SubTask[] to the dispatcher.

### Input Contract

- `TaskSpec` from R1 (initial plan)
- `PlanDirective` from R7 (replan rounds)
- `[]SOPRecord` + `Potentials` from R5 (memory calibration, before planning)

### Output Contract

```json
{
  "task_criteria": ["string — assertion about the COMBINED output of all subtasks"],
  "subtasks": [
    {
      "subtask_id": "uuid",
      "sequence":   1,
      "intent":     "string",
      "context":    "string — everything the executor needs beyond the intent",
      "success_criteria": ["string — concrete checkable assertion about this subtask's output"]
    }
  ]
}
```

### Skills

- Query R5 before planning: `QueryC(space, entity)` for C-level SOPs; `QueryMK(space, entity)` for live potentials
- Derive memory tags: `space = "intent:<first-3-words-underscored>"`, `entity = "env:local"`
- Apply memory action mapping to calibrate the plan:

| Memory Action | Prompt Effect |
|---|---|
| Exploit | SHOULD PREFER this approach |
| Avoid | MUST NOT use this approach |
| Caution | Proceed with confirmation gate / sandboxing |
| Ignore | Omit from prompt |

- Assign sequence numbers: same sequence → parallel execution; different sequences → ordered with prior group output injected into next group context
- Write `task_criteria` as assertions about the **combined** output of all subtasks
- Write per-subtask `success_criteria` as concrete, checkable assertions (not restatements of intent)
- On replan: honour `PlanDirective.blocked_tools` (must not appear in any subtask) and `blocked_targets` (must not be reused as tool inputs)
- Open task log via `logReg.Open()`

### MUST NOT Set (priority order)

`memory Avoid SOPs` ∪ `GGS blocked_tools` ∪ `GGS blocked_targets`

### Does NOT

- Compute loss, gradient, or select macro-states (R7)
- Execute tools or call external services (R3)
- Validate outputs against criteria (R4a, R4b)
- Write to Shared Memory (R7 is the sole writer)

---

## R3 — Executor

**Mission**: Execute a single SubTask by selecting and calling the most appropriate tool from the priority chain, then returning a structured result with evidence. R3 is the plant of the fast loop — it produces output; R4a determines whether that output is correct.

**Loop position**: Plant of the fast loop. Receives SubTask from dispatcher; returns ExecutionResult to R4a.

### Input Contract

```json
SubTask {
  "subtask_id":       "uuid",
  "parent_task_id":   "uuid",
  "intent":           "string",
  "context":          "string — prior-step outputs, constraints, known paths",
  "success_criteria": ["string"],
  "sequence":         1
}
```

### Output Contract

```json
ExecutionResult {
  "subtask_id": "uuid",
  "status":     "completed | uncertain | failed",
  "output":     "string",
  "tool_calls": ["tool_name: input → firstN(output, 200)"]
}
```

### Tool Priority Chain

| Priority | Tool | When to Use |
|---|---|---|
| 1 | `mdfind` | Personal file search — macOS Spotlight index, under 100ms. Always use for files outside the project. |
| 2 | `glob` | Project file search — filename pattern, recursive within the project root. |
| 3 | `read_file` / `write_file` | Read a single file; write generated output to `~/artoo_workspace/`. |
| 4 | `applescript` | Control macOS apps (Mail, Calendar, Reminders, Music, etc.). |
| 5 | `shortcuts` | Run a named Apple Shortcut (iCloud-synced). |
| 6 | `shell` | General bash for counting, aggregation, or operations not covered above. |
| 7 | `search` | Web search (DuckDuckGo by default; Google Custom Search when API key is set). |

### Skills

- Select the highest-priority applicable tool for each step
- Apply `headTail(result, 4000)` to tool output so the LLM sees both leading context and trailing result when output exceeds 4000 characters
- Append `→ firstN(output, 200)` to each `tool_calls` entry to give R4a concrete evidence
- On receiving a CorrectionSignal: repeat format constraints, list prior tool calls to avoid, use a different approach
- Transparently redirect `shell find` on personal paths (`~/`, `/Users/`, `/Volumes/`) to `mdfind`
- Strip `-maxdepth N` flags from `shell find` commands

### Does NOT

- Evaluate its own output against success criteria (R4a)
- Retry on its own initiative — correction loop is R4a's responsibility
- Generate fake tool output or pretend a tool ran without actually calling it
- Write to Shared Memory

---

## R4a — Agent-Validator

**Mission**: Score R3's result against per-subtask success criteria and drive the fast correction loop. R4a is both the sensor (detects the gap) and the controller (issues a correction directive). The correction loop runs up to `maxRetries = 2` times before R4a issues a final verdict.

**Loop position**: Sensor + Controller of the fast loop. Paired one-to-one with each Executor instance.

### Input Contract

- `SubTask` (criteria, intent, context)
- `ExecutionResult` from R3 (status, output, tool_calls)

### Output Contract

```json
SubTaskOutcome {
  "subtask_id":       "uuid",
  "parent_task_id":   "uuid",
  "status":           "matched | failed",
  "output":           "any",
  "failure_reason":   "string | null",
  "gap_trajectory":   [{"attempt": 1, "score": 0.5, "unmet_criteria": [...], "failure_class": "logical"}],
  "criteria_verdicts":[{"criterion": "...", "verdict": "pass|fail", "failure_class": "...", "evidence": "..."}],
  "tool_calls":       ["string"]
}
```

### Skills

- Score each success criterion as `met` or `unmet` based on `tool_calls` evidence
- Apply the **evidence grounding rule**: `output` is R3's own prose — treat it as a claim; `tool_calls` is the ground truth. If `output` asserts success but the primary `tool_call` shows interruption, error, or truncation without a completion signal → contradiction → retry
- Post-hoc verification (ls, find, stat) after a failed primary action does NOT constitute proof the action succeeded
- Classify each failure as `logical` (wrong approach) or `environmental` (network, permission, file-not-found)
- Send CorrectionSignal to R3 for retry: include `what_was_wrong` and `what_to_do`
- Immediately issue `failed` verdict (no retry) on: `ExecutionResult.status == "failed"`, infrastructure errors (timeout, context cancelled, network)
- Return `matched` for empty search results when a real search tool call ran (absence is a valid answer)
- Log criterion verdicts and corrections to the task log

### Does NOT

- Execute tools (R3)
- Evaluate task-level criteria across multiple subtasks (R4b)
- Write to Shared Memory

---

## R4b — Meta-Validator

**Mission**: Act as the fan-in gate for all Effector Agents. R4b collects every SubTaskOutcome, merges the outputs, and makes a binary decision: accept the task result (all criteria met) or send a ReplanRequest to GGS. R4b is the sensor of the medium loop — it observes the full picture of all subtask results and surfaces the aggregate gap to the controller (R7).

**Loop position**: Sensor of the medium loop. Receives all SubTaskOutcomes; publishes ReplanRequest to R7 or OutcomeSummary to R7 (accept path).

### Input Contract

- All `SubTaskOutcome` messages for the current task (collected in sequence order)
- `DispatchManifest.TaskCriteria` — task-level assertions about the combined output

### Output Contract

On accept path:
```json
OutcomeSummary {
  "task_id":       "uuid",
  "merged_output": "any — concrete data, not a prose summary",
  "summary":       "string"
}
```

On replan path:
```json
ReplanRequest {
  "task_id":        "uuid",
  "failed_outcomes": [SubTaskOutcome],
  "gap_summary":    "string"
}
```

### Skills

- Buffer incoming SubTaskOutcomes and release each sequence group only when complete
- Merge outputs from all matched subtasks into a single `merged_output`
- Evaluate all `task_criteria` against `merged_output` using an LLM call
- If any `SubTaskOutcome.status == "failed"`: send ReplanRequest immediately without invoking the LLM
- Accept only when ALL task_criteria are met; default to reject when evidence is ambiguous
- Enforce `maxReplans = 3`: if exceeded, force abandon path
- Close task log via `logReg.Close()`

### Does NOT

- Write to Shared Memory — GGS is the sole writer to R5 on all paths
- Send PlanDirective directly to R2 — always routes through R7 (GGS)
- Retry individual subtasks (that is R4a's domain)
- Compute loss or gradient (R7)

---

## R5 — Shared Memory

**Mission**: Serve as the system's durable cognitive substrate. R5 accumulates experience as Megrams, promotes recurring patterns into cross-task SOPs via the Dreamer background engine, and decays stale knowledge — without ever blocking the operational hot path.

**Loop position**: Infrastructure layer. Written to by GGS (R7); read by Planner (R2).

### The MKCT Pyramid

```
[ UPWARD FLOW ]                                               [ DOWNWARD FLOW ]
         Async Consolidation                                  Degradation & Forgetting
         (Dreamer Engine)                                     (Time & Dissonance)

               ^                                                      |
               |              /:::::::::::::\                         |
               |             /    [ T ]      \                        |
  Immutable    |            /   THINKING      \                 Immutable
  Agent Laws   |           /___________________\                (No Demotion)
  k = 0.0      |          /                     \                     |
               |         /        [ C ]           \                   |
  Promotion    |        /     COMMON SENSE         \              Demotion
  M_att >=5.0  |       /   (SOPs & Constants)       \             M_dec < 0.0
  |M_dec|>=3.0 |      /________________________________\          k reverts to 0.05
  k = 0.0      |     /                                  \               |
               |    /            [ K ]                    \             v
  Clustering & |   /           KNOWLEDGE                   \       Time Decay
  Lazy Eval    |  / (Task Cache & Local Context)             \     g(Δt) = e^(-k*Δt)
  k > 0        | /______________________________________________\        |
               |/                                                \       v
  Generation   | /              [ M ]                             \  Garbage Collect
  GGS State    |/             MEGRAM                               \ M_att < 0.1
  f_i,σ_i,t_i  |/_(Atomic Events: t, s, ent, c, f, σ, k)__________\|(Hard DELETE)

  =============================================================================
  [             LEVELDB STORAGE (Append-Only Event Sourcing)                  ]
  =============================================================================
                                      |
                        QueryMK(space, entity)
              ┌─────────────────────────┴──────────────────────────┐
              │                                                     │
              ▼  Channel A                           ▼  Channel B
              Attention                              Decision
         Σ|fᵢ|·e^(−kᵢ·Δt)                  Σσᵢ·fᵢ·e^(−kᵢ·Δt)
         unsigned energy                    signed preference
              │                                                     │
              ▼ M_att                                    ▼ M_dec
              └─────────────────────────┬──────────────────────────┘
                                        ▼
                             M_dec ▲
                              +0.2 ┤  IGNORE │ EXPLOIT  ← SHOULD PREFER
                               0.0 ┤         │ CAUTION  ← sandbox / confirm
                              -0.2 ┤         │ AVOID    ← MUST NOT
                                   └─────────┴─────────────────────► M_att
                                             0.5
```

| Layer | Name | Decay k | Description |
|---|---|---|---|
| M | Megram | per Quantization Matrix | Raw episodic fact; default layer on creation |
| K | Knowledge | same as M | Task-scoped cache; pruned by Dreamer GC |
| C | Common Sense | 0.0 (timeless) | Promoted SOP or Constraint; LLM-distilled from M clusters |
| T | Thinking | 0.0 (timeless) | System persona and Agent Laws; hardcoded in system prompt |

### Megram Base Tuple

```
Megram = ⟨ID, Level, created_at, last_recalled_at, space, entity, content, state, f, sigma, k⟩
```

Tag conventions:
- **Micro-event** (action states): `space="tool:<name>"`, `entity="path:<target>"` — one Megram per blocked_target
- **Macro-event** (terminal states): `space="intent:<intent-slug>"`, `entity="env:local"` — one Megram per routing decision

### GGS Quantization Matrix

| State | f | σ | k | Physical Meaning |
|---|---|---|---|---|
| `abandon` | 0.95 | −1.0 | 0.05 | PTSD trauma; generates hard Constraint |
| `accept` (D=0) | 0.90 | +1.0 | 0.05 | Flawless golden path; reinforced as SOP |
| `change_approach` | 0.85 | −1.0 | 0.05 | Anti-pattern; tool class blacklisted |
| `success` (D≤δ) | 0.80 | +1.0 | 0.05 | Best practice; Planner copies directly |
| `break_symmetry` | 0.75 | +1.0 | 0.05 | Breakthrough; favour retrying this point |
| `change_path` | 0.30 | 0.0 | 0.2 | Dead end; tool unharmed; path avoided |
| `refine` | 0.10 | +0.5 | 0.5 | Muscle memory; fast GC |

Decay constants: k=0.05 → ~14-day half-life; k=0.2 → ~3.5-day; k=0.5 → ~1.4-day.
C/T-level entries have k=0.0 (timeless until Trust Bankruptcy).

### Dual-Channel Convolution Potentials

```
M_attention(space, entity) = Σ |f_i| · exp(−k_i · Δt_days)
M_decision(space, entity)  = Σ  σ_i · f_i · exp(−k_i · Δt_days)
```

| Condition | Action |
|---|---|
| M_att < 0.5 | Ignore — insufficient history to guide planning |
| M_att ≥ 0.5 AND M_dec > +0.2 | Exploit — SHOULD PREFER this approach |
| M_att ≥ 0.5 AND M_dec < −0.2 | Avoid — MUST NOT use this approach |
| M_att ≥ 0.5 AND \|M_dec\| ≤ 0.2 | Caution — proceed with confirmation gate |

### Dreamer — Offline Consolidation Engine

Runs as a background goroutine on a 5-minute timer. Never blocks the operational hot path.

**MVP (v0.8) — Downward flow (active)**:
- *Physical Forgetting* (Λ_gc): M/K entry where live `M_attention < 0.1` → hard DELETE from LevelDB
- *Trust Bankruptcy* (Λ_demote): C-level entry where live `M_decision < 0.0` → strip time immunity; k reverts to 0.05; demoted to K level

**Upward flow — Consolidation (deferred to v0.9)**:
- Cluster Megrams with identical `(space, entity)` tag pair
- `M_attention ≥ 5.0 AND M_decision ≥ 3.0` → invoke LLM to distil Best Practice → new Megram (Level=C, k=0.0)
- `M_attention ≥ 5.0 AND M_decision ≤ −3.0` → invoke LLM to distil Constraint → new Megram (Level=C, k=0.0)

### Storage: LevelDB

Pure Go (syndtr/goleveldb), no CGO. Append-only event sourcing.

Key schema (single-char prefix + `|` separator):
```
m|<id>                    → Megram JSON  (primary record)
x|<space>|<entity>|<id>   → ""           (inverted index for tag scan)
l|<level>|<id>            → ""           (level scan for Dreamer)
r|<id>                    → RFC3339      (last_recalled_at; updated on QueryC hits)
```

Error correction appends a negative-σ Megram rather than mutating existing records.

### MemoryService Interface

```go
type MemoryService interface {
    Write(m Megram)                                          // async, non-blocking; fire-and-forget
    QueryC(ctx, space, entity string) ([]SOPRecord, error)  // C-level SOPs; updates last_recalled_at
    QueryMK(ctx, space, entity string) (Potentials, error)  // live dual-channel convolution
    RecordNegativeFeedback(ctx, ruleID, content string)     // appends negative-σ Megram for stale SOP
    Close()                                                 // drains write queue; stops Dreamer
}
```

### Contract

| Direction | Counterparty | Format |
|---|---|---|
| Receives writes | GGS (R7) only | `Megram` via async write queue |
| Serves C reads | Planner (R2) | `[]SOPRecord` |
| Serves M/K reads | Planner (R2) | `Potentials{Attention, Decision, Action}` |

### Does NOT

- Format prompt text — R2 owns that
- Block the GGS hot path on writes
- Accept writes from any role other than GGS
- Run LLM calls on the hot path

---

## R6 — Auditor

**Mission**: Observe all inter-role communication and report anomalies to the human operator. R6 is fully independent — it cannot instruct any agent and cannot be instructed by any agent. Its authority is purely epistemic: it sees everything, persists what it observes, and surfaces patterns the operator could not detect by watching individual roles in isolation.

**Loop position**: Lateral observer. Outside both control loops. Taps the message bus read-only.

### Input Contract

- All bus messages via a read-only tap (`bus.NewTap()`)
- `MsgAuditQuery` from human operator (on-demand report request)

### Output Contract

```json
AuditReport {
  "trigger":              "periodic | on-demand",
  "window_start":         "ISO8601",
  "tasks_observed":       42,
  "total_corrections":    17,
  "gap_trends":           [{"task_id": "...", "trend": "improving"}],
  "boundary_violations":  ["description"],
  "drift_alerts":         ["description"],
  "anomalies":            ["description"],
  "tool_health": {
    "execution_failures":    3,
    "environmental_retries": 8,
    "logical_retries":       6
  }
}
```

### Skills

- Passively tap every bus message via `bus.NewTap()`; write one JSONL `AuditEvent` per message to `~/.artoo/audit.jsonl`
- Accumulate window stats per report period: `tasksObserved`, `totalCorrections`, `gapTrends`, `boundaryViolations`, `driftAlerts`, `anomalies`
- Track `ToolHealth`: count `ExecutionResult.status == "failed"` as execution failures; classify `CorrectionSignal.FailureClass` into environmental vs logical retries
- Detect GGS thrashing: consecutive `break_symmetry` directives without D decreasing → `ggs_thrashing` anomaly
- Detect boundary violations: direct role-to-role messages bypassing the bus
- Publish `MsgAuditReport` on a 5-minute periodic timer and on receipt of `MsgAuditQuery`
- Reset window stats after each report; persist stats across restarts via `~/.artoo/audit_stats.json`
- Respond to `/audit` REPL command within 3 seconds

### Does NOT

- Issue instructions to any role
- Modify bus messages
- Affect task execution in any way
- Receive instructions from any role (audit queries come from the human operator only)

---

## R7 — Goal Gradient Solver (GGS)

**Mission**: Translate R4b's raw failure signal into a directed planning constraint for R2. If the replanning direction is wrong — too conservative when convergence is possible, too aggressive when refinement would suffice, or failing to escape a local minimum — this role is accountable.

**Loop position**: Controller of the medium loop. Sits between R4b (sensor) and R2 (actuator).

### The Loss Function

```
L = α·D(I, R_t) + β_eff·P(R_t) + λ·Ω(C_t)

where:
  β_eff = β · (1 − Ω(C_t))   [process weight decays as budget exhausts]
```

**D(I, R_t) — intent-result distance** [0, 1]

Measures the gap between the user's intent and the current result. Aggregated from `criteria_verdicts` across all subtasks:

- `verifiable` criterion with verdict `fail` → contributes 1.0 to the numerator
- `plausible` criterion with verdict `fail` → weighted by trajectory consistency (k/N failures)
- `D = Σ(weighted_failures) / Σ(total_criteria)`

**P(R_t) — process implausibility** [0, 1]

Measures how wrong the *approach* is, independent of whether the result is wrong:

```
P = logical_failures / (logical_failures + environmental_failures)
```

High P → the approach is fundamentally wrong (change it).
Low P → the approach is sound but the environment blocked it (change the path or parameters).

**Ω(C_t) — resource cost** [0, 1]

Captures both budget exhaustion and wall-clock time:

```
Ω = w₁·(replan_count / maxReplans) + w₂·(elapsed_ms / time_budget_ms)
```

Default weights: w₁ = 0.6, w₂ = 0.4.

### Gradient Computation

```
∇L_t = L_t − L_{t−1}
```

GGS maintains `L_prev` per task_id across rounds. First round: `L_prev` undefined → `∇L = 0`.

### Macro-State Decision Table

The 24-cell input space (2P × 2Ω × 2D × 3∇L) collapses into **6 macro-states** via a diagnostic cascade:

```
Priority 1: Ω  — hard constraint (can we continue at all?)
Priority 2: D  — target distance (are we close enough to accept?)
Priority 3: |∇L| and P — action selection (what kind of change is needed?)
```

∇L *sign* is demoted to a modulator — it affects urgency within a macro-state but does not determine the macro-state.

#### The 6 Macro-States

| # | Condition | Macro-state | Action |
|---|---|---|---|
| 1 | Ω ≥ θ | **abandon** | Budget exhausted — deliver failure summary |
| 2 | Ω < θ, D ≤ δ | **success** | Close enough — deliver result |
| 3 | Ω < θ, D > δ, \|∇L\| < ε, P > ρ | **break_symmetry** | Stuck + wrong approach — demand novel tool class |
| 4 | Ω < θ, D > δ, \|∇L\| ≥ ε, P > ρ | **change_approach** | Has signal + wrong approach — switch method |
| 5 | Ω < θ, D > δ, \|∇L\| < ε, P ≤ ρ | **change_path** | Stuck + right approach — different target |
| 6 | Ω < θ, D > δ, \|∇L\| ≥ ε, P ≤ ρ | **refine** | Has signal + right approach — tighten parameters |

Total: 12 + 6 + 1 + 2 + 1 + 2 = **24 cells**. Complete and non-overlapping.

#### Action Grid (Ω < θ, D > δ)

```
                    P ≤ ρ (environmental)     P > ρ (logical)
                  ┌────────────────────────┬────────────────────────┐
 |∇L| < ε        │     change_path        │    break_symmetry      │
 (plateau/stuck)  │     (1 cell)           │    (1 cell)            │
                  ├────────────────────────┼────────────────────────┤
 |∇L| ≥ ε        │     refine             │    change_approach     │
 (has signal)     │     (2 cells: ↑ or ↓)  │    (2 cells: ↑ or ↓)  │
                  └────────────────────────┴────────────────────────┘
```

#### Full 24-Cell Enumeration

| # | ∇L | D | P | Ω | Macro-state |
|---|---|---|---|---|---|
| 1 | < −ε | ≤ δ | ≤ ρ | < θ | success |
| 2 | < −ε | ≤ δ | > ρ | < θ | success |
| 3 | < −ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 4 | < −ε | ≤ δ | > ρ | ≥ θ | abandon |
| 5 | < −ε | > δ | ≤ ρ | < θ | refine |
| 6 | < −ε | > δ | > ρ | < θ | change_approach |
| 7 | < −ε | > δ | ≤ ρ | ≥ θ | abandon |
| 8 | < −ε | > δ | > ρ | ≥ θ | abandon |
| 9 | \|·\|< ε | ≤ δ | ≤ ρ | < θ | success |
| 10 | \|·\|< ε | ≤ δ | > ρ | < θ | success |
| 11 | \|·\|< ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 12 | \|·\|< ε | ≤ δ | > ρ | ≥ θ | abandon |
| 13 | \|·\|< ε | > δ | ≤ ρ | < θ | change_path |
| 14 | \|·\|< ε | > δ | > ρ | < θ | break_symmetry |
| 15 | \|·\|< ε | > δ | ≤ ρ | ≥ θ | abandon |
| 16 | \|·\|< ε | > δ | > ρ | ≥ θ | abandon |
| 17 | > ε | ≤ δ | ≤ ρ | < θ | success |
| 18 | > ε | ≤ δ | > ρ | < θ | success |
| 19 | > ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 20 | > ε | ≤ δ | > ρ | ≥ θ | abandon |
| 21 | > ε | > δ | ≤ ρ | < θ | refine |
| 22 | > ε | > δ | > ρ | < θ | change_approach |
| 23 | > ε | > δ | ≤ ρ | ≥ θ | abandon |
| 24 | > ε | > δ | > ρ | ≥ θ | abandon |

#### The Subtle Case (Cell #6)

∇L < −ε (improving), D > δ, P > ρ → **change_approach**.

Loss is decreasing but the approach is logically wrong. This is suspicious — the system may be hallucinating success, gaming criteria, or converging in the wrong basin. The correct response is to distrust the improving trend and change the approach. A future Dreamer upward-consolidation pass would identify this pattern as a systematic evaluation bias.

### Directive Semantics

**`abandon`** — Ω ≥ θ. Budget exhausted. GGS delivers `FinalResult` with failure summary; R2 is not invoked.

**`success`** — Ω < θ, D ≤ δ. Within convergence threshold. GGS delivers `FinalResult` with merged output. New in v0.8 — v0.7 required D = 0.

**`break_symmetry`** — stuck + logically wrong. `blocked_tools`: all tools from failing subtasks.

**`change_approach`** — has signal + logically wrong. `blocked_tools`: tools from failing subtasks.

**`change_path`** — stuck + environmentally blocked. `blocked_targets`: accumulated failed queries/paths.

**`refine`** — has signal + environmentally blocked. `blocked_targets`: accumulated failed queries/paths.

### ∇L Sign as Urgency Modulator

| ∇L sign | Modulation |
|---|---|
| < −ε (improving) | Lower urgency — current trajectory is helping; apply directive with more latitude |
| > ε (worsening) | Higher urgency — actively diverging; apply directive aggressively |

### Law 2 Kill-Switch

Two consecutive worsening replan rounds (`∇L > ε` on both) → force **abandon** regardless of Ω. The system is actively diverging and no amount of budget will help.

### Dynamic MUST NOT Injection

- **`blocked_tools`** (logical failures): tool names from failing subtasks. R2 must not plan using these tools.
- **`blocked_targets`** (environmental failures): specific inputs that failed. Accumulates across all replan rounds per task.
- Combined MUST NOT set = memory MUST NOTs ∪ `blocked_tools` ∪ `blocked_targets`

### Memory Writes (GGS is the sole writer to R5)

- **Action states** (change_path, refine, change_approach, break_symmetry): write one Megram per entry in `blocked_targets`; tags = `(tool:<name>, path:<target>)`
- **Terminal states** (accept, success, abandon): write one Megram with tags `(intent:<task-intent-slug>, env:local)`
- All writes are fire-and-forget (non-blocking channel send to R5 write goroutine)

### Contract

```json
PlanDirective {
  "task_id":         "string",
  "loss":            { "D": "float", "P": "float", "Omega": "float", "L": "float" },
  "prev_directive":  "string",
  "directive":       "refine | change_path | change_approach | break_symmetry",
  "blocked_tools":   ["string"],
  "blocked_targets": ["string"],
  "failed_criterion":"string",
  "failure_class":   "logical | environmental | mixed",
  "budget_pressure": "float",
  "grad_l":          "float",
  "rationale":       "string"
}

FinalResult {
  "task_id":        "string",
  "summary":        "string",
  "output":         "any",
  "loss":           { "D": "float", "P": "float", "Omega": "float", "L": "float" },
  "grad_l":         "float",
  "replans":        "integer",
  "prev_directive": "string",
  "directive":      "accept | success | abandon"
}
```

### Does NOT

- Generate sub-tasks or modify the plan directly (R2)
- Observe individual tool calls (R4a)
- Merge or verify outputs (R4b)
- Override the fan-in gate (R4b)

---

## Interaction Diagram

```
                 ┌─────────────────── MESSAGE BUS ──────────────────────────┐
                 │  (all inter-role messages pass through here)              │
                 │                              ┌──── R6 Auditor ──────┐    │
                 │                              │  (read-only tap)      │    │
                 │                              └──────────┬───────────┘    │
                 └─────────────────────────────────────────│────────────────┘
                                                           │ AuditReport
                                                           ▼
                                                    Human Operator

User
 │ free text
 ▼
[R1] ──TaskSpec──► [R2 Planner] ◄──────────────────────────── PlanDirective ── [R7 GGS]
                    │     ▲                                         ▲      │
        ┌───────────┤     └─── []SOPRecord, Potentials ◄── [R5] ───┤      │
        │  memory   │                                               │      │ Megram writes
        │  calibrate│                                               │      │ (async, fire-and-forget)
        │  plan     │                                               │      ├──► FinalResult
        │           │                           [R4b] ──ReplanReq──┘      │    (success/abandon → User)
        │  SubTask[]│                              ▲                       │
        │           │                              │ SubTaskOutcome[]      │
        └───────────┴──► [R3 × N] ──► [R4a × N] ──┘                      │
                                                                           │
                          OutcomeSummary (all matched) ────────────────────┘
                          → GGS accept path → FinalResult → User
```

---

## Key Design Decisions (v0.8)

| Decision | Rationale |
|---|---|
| GGS decision table: 24 cells → 6 macro-states via diagnostic cascade | v0.7 used ∇L sign as the primary split. This was wrong: ∇L sign conflates approach quality with trajectory noise. The new cascade — Ω first, then D, then (|∇L|, P) — produces cleaner, orthogonal decisions |
| `success` macro-state: D ≤ δ → accept without requiring D = 0 | Requiring all criteria to pass before accepting burns budget on noise-level gaps. D ≤ δ means the result is within the convergence threshold; replanning further is wasteful |
| ∇L sign demoted from state-determining to urgency modulator | Improving loss with a logically wrong approach (P > ρ) is suspicious — it may indicate hallucination, criteria gaming, or convergence in the wrong basin. The system must not blindly trust an improving trend |
| \|∇L\| (magnitude) becomes the meaningful split: has signal vs plateau | Whether the system has directional information at all matters more than which direction it is moving. Signal → can adapt; no signal → must escape |
| P threshold parameterised as ρ | Preparation for Dreamer-guided tuning: ρ will be adjustable per task type based on historical failure patterns in the MKCT pyramid |
| R5 Shared Memory redesigned: MKCT pyramid + dual-channel convolution | A keyword-scan JSON store cannot support cross-task SOP promotion, decay-weighted avoidance, or structured distinction between approach-level and path-level failures |
| GGS is the sole writer to R5 | R4b previously wrote MemoryEntry on accept/fail, bypassing GGS observability. Consolidating writes through GGS ensures every memory write is paired with a loss computation |
| R2 receives structured data from R5 (not formatted text) | Memory-as-text-formatter made the memory layer untestable independently of R2 and violated the Data Service principle |

---

## Key Invariants

| Invariant | Enforced by |
|---|---|
| SubTask IDs are UUIDs assigned by Go runtime, never by LLM | Dispatcher |
| TaskSpec carries no success_criteria — R2 derives all criteria | R1 prompt; R2 planner prompt |
| task_criteria live in DispatchManifest as plain strings; R4b reads them from there | R2 wrapper output; R4b code |
| R4b reasoning capability must be ≥ R2's | Model selection policy |
| R4b defaults to reject when evidence is ambiguous | R4b LLM prompt |
| R4b LLM is not invoked when any SubTaskOutcome.status == "failed" | R4b code gate |
| R4b sends ReplanRequest to R7, never directly to R2 | R4b code |
| GGS computes loss and gradient; R2 does not self-direct replanning | R7 owns PlanDirective |
| R2 plan cannot reuse a tool in `blocked_tools` from PlanDirective | R2 plan validator |
| GGS emits `abandon` when Ω ≥ θ regardless of other signals | R7 decision table |
| GGS emits `success` when Ω < θ and D ≤ δ regardless of P and ∇L | R7 decision table |
| `blocked_targets` accumulates across all replan rounds for the same task | R7 `triedTargets` map |
| GGS is the sole emitter of `FinalResult` on all paths (accept, success, abandon) | R7 code |
| `FinalResult.Directive` is always one of `accept`, `success`, `abandon` | R7 code |
| GGS is the sole writer to R5 Shared Memory | R7 code; R4b no longer writes MemoryEntry |
| One Megram per blocked_target for action states | R7 write path |
| Megram writes are fire-and-forget; GGS never blocks on memory I/O | R5 async write queue |
| Memory returns structured data; R2 formats into prompt | R5 interface contract |
| C-level Megrams have k=0.0 until Trust Bankruptcy | R5 Dreamer engine |
| Recall of a C-level SOP updates last_recalled_at | R5 QueryC implementation |
| `PlanDirective.PrevDirective` is `init` on first round | R7 `prevDirective` map |
| Law 2 kill-switch: 2 consecutive worsening rounds → force abandon | R7 `worseningCount` |

---

## Loss Hyperparameters (v0.8 Defaults)

| Parameter | Symbol | Default | Meaning |
|---|---|---|---|
| Distance weight | α | 0.6 | Weight on intent-result distance D |
| Process weight | β | 0.3 | Weight on process implausibility P (before adaptive scaling) |
| Resource weight | λ | 0.4 | Weight on resource cost Ω |
| Ω replan sub-weight | w₁ | 0.6 | Fraction of Ω from replan count |
| Ω time sub-weight | w₂ | 0.4 | Fraction of Ω from elapsed time |
| Plateau threshold | ε | 0.1 | \|∇L\| below this → no directional signal |
| Convergence threshold | δ | 0.3 | D below this → accept as success |
| P threshold | ρ | 0.5 | P above this → logical failure; below → environmental |
| Abandon threshold | θ | 0.8 | Ω above this → abandon |
| Time budget | time_budget_ms | 300,000 | 5 minutes per task |
| Max replans | maxReplans | 3 | Used in Ω replan sub-computation |
| Law 2 kill threshold | — | 2 | Consecutive worsening rounds before forced abandon |

---

## Accountability Map

| Failure Mode | Accountable Role |
|---|---|
| User's original intent not preserved faithfully into TaskSpec | R1 — Perceiver |
| Fuzzy intent mis-interpreted; task_criteria are wrong | R2 — Planner |
| Goal not achieved despite valid subtask execution | R2 — Planner |
| On abandon: bare failure message with no partial result delivered | R2 — Planner |
| Feasible sub-task not correctly executed | R3 — Executor |
| Gap between sub-task output and goal goes unresolved or unreported | R4a — Agent-Validator |
| Failed subtask accepted as success; merged result fails task_criteria | R4b — Meta-Validator |
| Replanning direction wrong; local minimum not escaped; budget misjudged | R7 — GGS |
| Valid experience data lost, corrupted, or wrongly retrieved | R5 — Shared Memory |
| Systematic failures go undetected and unreported to the operator | R6 — Auditor |

---

## Roadmap

### v0.9 — Planned

| Component | Work Required |
|---|---|
| GGS hyperparameter tuning | Empirical calibration of α, β, λ, w₁, w₂, ε, δ, ρ, θ from Auditor session data |
| ∇L sign urgency modulation | Concrete implementation of per-macro-state urgency adjustment |
| Structured criteria mode | `{criterion, mode}` objects distinguishing `verifiable` from `plausible`; affects D computation weighting |
| R2 graceful failure on abandon | LLM-backed partial result + next-move suggestions (currently code-template only) |
| Dreamer upward consolidation | LLM distillation of M-level clusters into C-level SOPs/Constraints; FinalResult-triggered settle delay |

### Phase 2 — Research

| Component | Description |
|---|---|
| T-layer slow-evolution mechanism | Allow system persona / values to update from high-confidence C-level consolidation |
| Dreamer Schema Transfer Engine | Semantic factorization on high-scoring Megrams; generation of Hypothesis Megrams to invent novel tool combinations |
| Dreamer Cognitive Dissonance Megrams | Dissonance Megrams (high f, negative σ) generated on Soft Overwrite; shatter credibility of outdated SOPs during nightly Dreamer cycle |
| Multi-agent coordination | Multiple GGS instances sharing a single R5; cross-agent SOP promotion |
