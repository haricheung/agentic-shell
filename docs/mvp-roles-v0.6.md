# MVP Role Definitions

**Version**: 0.6
**Status**: Draft
**Date**: 2026-02-20
**Scope**: Minimum viable organization — seven roles. Goal Gradient Solver and Dreamer deferred to v0.7.

## Changelog from v0.5

| Change | Reason |
|---|---|
| R2 — memory integration replaced with active calibration protocol | v0.5 injected raw memory entries as advisory text; LLM ignored them. Memory must produce explicit, code-enforced planning constraints, not suggestions |
| R2 — calibration step is code-side only (keyword match + recency); no LLM call | Separate LLM call for calibration would add 1-2 s latency per task. The Dreamer (v0.7) pre-organizes semantic memory so calibration degrades to a near-zero read; in v0.6 calibration is bounded by capping entries at 10 and doing filtering in Go code, not in the LLM |
| R4a — holistic scoring replaced with per-criterion independent evaluation | v0.5 asked LLM to "score the result overall"; LLM accepted plausible-sounding outputs even when specific criteria were unmet. Each criterion must produce a binary verdict independently |
| R4a — criterion verdicts now include failure_class (logical \| environmental) | Hard per-criterion evaluation is correct but the error signal was too coarse. Environmental failures (network, permissions, not found) need a different controller response than logical failures (wrong answer). Enriching the signal enables targeted replanning without weakening the gate |
| R4b — LLM gate: hard-fail on any failed subtask enforced in code before LLM | v0.5 let the LLM merge all outcomes together and reason holistically; it accepted 1-matched + 1-failed as success. LLM is now only invoked when all subtasks passed |
| SubTask IDs removed from planner LLM output; assigned by runtime | Planner LLM fabricated fake sequential UUIDs (top ID reused 270 times), breaking all dispatcher routing guarantees. IDs are now assigned by Go after parsing |
| R1 success_criteria removed from TaskSpec | R1 is a transducer (arm), not a decision-maker. Deriving verifiable criteria requires knowing what tools can measure — that is R2's domain knowledge, not R1's. R1 carries intent faithfully; R2 operationalizes it into testable predicates |
| R2 owns all criteria derivation — task-level and subtask-level | Task-level success_criteria (for R4b) now live in DispatchManifest, produced by R2. Subtask-level criteria live in SubTask as before. Accountability for criteria quality shifts from R1 to R2 |
| R1 confirmed memory-blind (explicit) | R1 is a transducer (reference signal), not a decision-maker. Memory belongs to R2 (brain). Made explicit rather than implicit |

---

## Feedback Loop Structure

```
FAST LOOP (inside each Effector Agent)
┌─────────────────────────────────────────┐
│  decision  │  execution  │  correction  │
│  [SubTask] │  Executor   │  Agent-Val.  │
│    (R2)    │    (R3)     │    (R4a)     │
└─────────────────────────────────────────┘
         plant = R3 │ sensor+controller = R4a

MEDIUM LOOP (inside Metaagent, MVP simplified)
┌──────────────────────────────────────────────────────────┐
│    decision     │     execution      │    correction      │
│  Planner (R2)  │  Effector Agents   │  Meta-Val. (R4b)  │
│  [also acts as │  (fast loops       │  [sensor only;     │
│  controller    │   running inside]  │   GGS deferred]    │
│  in MVP]       │                    │                    │
└──────────────────────────────────────────────────────────┘
         plant = Effectors │ sensor = R4b │ controller = GGS (deferred)

AUDITOR (lateral — outside both loops)
┌──────────────────────────────────────────────────────────┐
│  Observes all inter-role messages via message bus        │
│  Reports anomalies to human operator                     │
│  Cannot instruct any agent; cannot be instructed by any  │
└──────────────────────────────────────────────────────────┘
```

In steady state (post-MVP), GGS sits between R4b (sensor) and R2 (actuator) in the
medium loop. In MVP, R2 absorbs the controller role directly from R4b's error signal.

---

## Architectural Constraint: Observable Message Bus

All inter-role communications must pass through a shared message bus that the Auditor
can tap as a read-only observer. Direct point-to-point calls between roles are not
permitted — every message must be routable.

---

## Role Definition Template

- **Mission** — the outcome this role owns; explicit accountability
- **Loop position** — which part of the feedback loop this role occupies
- **Skills** — what it is capable of doing
- **Contract** — inputs and outputs with format and counterparty
- **Does NOT** — explicit boundary enforcing orthogonality

---

## Role Index

| ID | Role | Lives in | Loop position | Mission Summary |
|---|---|---|---|---|
| R1 | Perceiver | Entry point | Reference signal | If the task is misunderstood, this role is responsible |
| R2 | Planner | Metaagent | Actuator (+ MVP controller) | If the goal is not achieved despite valid execution, this role is responsible |
| R3 | Executor | Effector Agent | Plant | If a feasible sub-task is not correctly executed, this role is responsible |
| R4a | Agent-Validator | Effector Agent | Sensor + Controller (fast loop) | If a gap between outcome and sub-task goal goes unresolved or unreported, this role is responsible |
| R4b | Meta-Validator | Metaagent | Sensor (medium loop) | If the merged result is accepted outside plausible range or a task is silently abandoned, this role is responsible |
| R5 | Shared Memory | Infrastructure | State store | If valid data is lost, corrupted, or wrongly retrieved, this role is responsible |
| R6 | Auditor | Infrastructure | Lateral observer | If systematic failures go undetected and unreported to the human operator, this role is responsible |

---

## R1 — Perceiver

**Mission**: Faithfully translate raw user input into a structured intent specification.
R1 is a transducer — it carries the user's signal into the system without interpretation
or elaboration. If the system acts on a wrong or ambiguously specified intent, this role
is accountable. It is not accountable for how success is measured — that belongs to R2.

**Loop position**: Reference signal. The `TaskSpec` it produces is the raw target.
R2 operationalizes it into measurable criteria. R1's quality is the ceiling for how
accurately the system understands *what* the user wants; R2's quality is the ceiling
for how correctly the system verifies *whether* it was achieved.

**Skills**:
- Parse natural language into structured intent
- Identify ambiguities in *references* (which file? what time range? which account?)
  and ask clarifying questions (max 2 rounds; batch into one turn; proceed on empty
  answer rather than looping)
- Identify scope constraints (file paths, time bounds, domains) as stated by the user
- Produce a `TaskSpec` JSON that faithfully represents the user's stated goal

**Does NOT**:
- Derive success criteria — R1 does not know what the executor can measure; that is R2's job
- Decompose the task into sub-tasks (R2)
- Evaluate whether a result satisfies the user (R4b)
- Access memory or prior task history (R5) — R1 is a transducer, not a decision-maker
- Elaborate or intellectualize the user's goal — carry it faithfully, nothing more

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | User | Free-text natural language |
| Produces | Planner (R2) | `TaskSpec` JSON |
| Produces | User | Clarifying question (max 2 rounds, batched, skipped if empty answer) |

```json
TaskSpec {
  "task_id":    "string",   // short snake_case; assigned by R1
  "intent":     "string",   // one sentence, action-oriented; user's words, not paraphrase
  "constraints": {
    "scope":    "string | null",   // as stated by user (path, domain, app name, etc.)
    "deadline": "ISO8601 | null"
  },
  "raw_input":  "string"    // verbatim user input, unmodified
}
```

---

## R2 — Planner

**Mission**: Own the path from task specification to final result. If the overall goal is
not achieved despite Executors performing correctly — because decomposition was wrong,
sequencing was wrong, or prior failures were ignored — this role is accountable.

**Loop position**: Actuator of the medium loop. In MVP (no Goal Gradient Solver), also
absorbs the controller role: interprets `ReplanRequest` directly and decides what to
change. Known MVP limitation — naive replanning rather than directed gradient
correction.

**Skills**:
- Execute the memory calibration protocol before producing any plan (see below)
- Derive task-level `success_criteria` from `TaskSpec.intent` — independently falsifiable
  predicates that R4b can verify from tool output alone; R2 knows what the executor can
  measure, R1 does not; criteria quality is R2's accountability
- Decompose a `TaskSpec` into an ordered or parallel set of atomic `SubTask` objects,
  each self-contained, each with independently falsifiable success criteria derived from
  the task-level criteria above
- Dispatch sub-tasks and send a `DispatchManifest` (including task-level criteria) to R4b
- Receive `ReplanRequest` and apply the constraints it implies — plan must differ from
  the recorded failed approach; cannot reissue an identical plan
- Write episodic/procedural `MemoryEntry` objects to R5 (on task completion/failure)

### Memory Calibration Protocol (mandatory before every plan)

R2 must execute this protocol before generating any `SubTask` list. It is not optional
and is not advisory.

**Latency budget**: the calibration step must not add perceivable latency. Steps 1–3
are executed in Go code (no LLM call). Only Step 4 (plan generation) invokes the LLM.

**Step 1 — Retrieve**: Query R5 with the current `TaskSpec.intent` and key terms.
Receive up to **10** most recent `MemoryEntry[]` (capped in Go before passing to planner).

**Step 2 — Calibrate**: In Go code, filter entries by:
- Keyword overlap with `TaskSpec.intent` (discard zero-overlap entries)
- Recency (prefer newer entries; if two entries contradict, keep the newer)
Calibration output: a filtered, ranked list. **No LLM call for this step.**

**Step 3 — Constrain**: In Go code, derive explicit planning constraints from calibrated
entries and format them as injected text for the planner prompt:
- `MUST NOT`: "must not use approach X for intent Y because lesson Z" (from procedural entries)
- `SHOULD PREFER`: "should use approach A for intent B because episodic evidence C" (from episodic entries)

**Step 4 — Plan**: LLM generates the `SubTask` list given the prompt with pre-formatted
constraints injected. The plan must demonstrably satisfy all `MUST NOT` constraints.
Issuing an identical plan to a previous failed plan is a planning error regardless of LLM output.

**Step 4 is code-enforced**: if the generated plan reuses a tool or approach flagged in
a `MUST NOT` constraint, the plan is rejected and re-generated before dispatch.

**v0.6 known limitation**: R5 stores raw episodic/procedural entries (no Dreamer yet).
The calibrate step does more filtering work than it will in v0.7, when the Dreamer
pre-organizes episodic → semantic memory between tasks. In v0.7, R2's calibration reads
pre-curated semantic entries; the calibrate step degrades to a near-zero-cost pass-through.

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Perceiver (R1) | `TaskSpec` JSON |
| Receives | Meta-Validator (R4b) | `ReplanRequest` JSON |
| Receives | Shared Memory (R5) | `MemoryEntry[]` |
| Produces | Executor (R3) | `SubTask` JSON (subtask_id assigned by runtime, not LLM) |
| Produces | Meta-Validator (R4b) | `DispatchManifest` JSON |
| Produces | Shared Memory (R5) | Read query + `MemoryEntry` (on completion/failure) |
| Produces | User (final) | Final result (via R4b) |

```json
SubTask {
  // subtask_id: NOT produced by R2 LLM — assigned by Go runtime (uuid.NewString())
  //             after parsing LLM output, before dispatch
  "parent_task_id":   "string",
  "intent":           "string",
  "success_criteria": ["string"],  // independently falsifiable; derived from task_criteria below
  "context":          "string",
  "deadline":         "ISO8601 | null",
  "sequence":         "integer"    // same value = parallel; different = ordered dependency
}

DispatchManifest {
  "task_id":        "string",
  "subtask_ids":    ["string"],      // IDs assigned by runtime, sent after SubTask dispatch
  "task_criteria":  ["string"],      // task-level success criteria; derived by R2 from TaskSpec.intent
                                     // R4b verifies merged result against these
  "dispatched_at":  "ISO8601"
}
```

**Does NOT**:
- Execute any action directly (R3)
- Evaluate output quality at sub-task or full-task level (R4a, R4b)
- Merge or assess parallel results (R4b)
- Treat memory entries as optional hints — memory calibration output is a constraint, not context
- Compute the direction of correction — in MVP it does this itself as a known limitation; in steady state this belongs to GGS
- Consolidate or reorganize memory (deferred — Dreamer)
- Expect success_criteria from R1 — TaskSpec carries no criteria; R2 derives them from intent

---

## R3 — Executor

**Mission**: Execute exactly one assigned sub-task and return a concrete, verifiable
result. If a sub-task is feasible as specified and the result is wrong or missing, this
role is accountable. If the sub-task is infeasible as specified, reporting that honestly
is also this role's responsibility.

**Loop position**: Plant of both loops. Acted upon by the fast loop (Agent-Validator
drives corrections) and re-instantiated by the medium loop (Planner re-dispatches after
correction). Has no awareness of either loop.

**Skills**:
- Interpret a `SubTask` and determine the required tool call sequence
- Use available tools: shell commands, file I/O, mdfind, glob, AppleScript, web search
- Append last 120 chars of each tool result to its `tool_calls` entry as evidence
- Produce a result evaluable against the sub-task's success criteria
- Detect and report infeasibility with a specific reason

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Planner (R2) | `SubTask` JSON |
| Receives | Agent-Validator (R4a) | `CorrectionSignal` JSON |
| Produces | Agent-Validator (R4a) | `ExecutionResult` JSON |

```json
ExecutionResult {
  "subtask_id":  "string",
  "status":      "completed | failed",
  "output":      "any",
  "tool_calls":  ["string"]    // each entry: "tool:command → <last 120 chars of output>"
}
```

**Does NOT**:
- Evaluate whether its output meets the criteria (R4a)
- Decide to retry itself — the Agent-Validator owns retry and correction
- Communicate with other Executors or with R2 directly
- Report results to anyone other than R4a
- Write to Shared Memory

---

## R4a — Agent-Validator

**Mission**: Close the gap between the Executor's output and the sub-task goal using the
fast correction loop. If a bad result is silently accepted as matched, this role is
accountable.

**Loop position**: Sensor and controller of the fast loop. As sensor: evaluates the gap
between execution output and sub-task criteria. As controller: computes a directed
`CorrectionSignal` targeting the specific failed criterion.

### Validation Model (per-criterion, not holistic)

R4a must evaluate each `success_criterion` in the `SubTask` **independently**. Holistic
reasoning about the overall result is not permitted during the evaluation phase.

For each criterion:
1. Examine `tool_calls` evidence and `output` for that specific criterion only
2. Produce an explicit verdict: `pass` or `fail`
3. If `fail`: record which criterion failed, why, and the **failure class**:
   - `logical` — the approach ran but produced the wrong result (e.g. wrong file, wrong value)
   - `environmental` — infrastructure prevented execution (network timeout, permission denied,
     path not found, tool unavailable)
4. The failure class does NOT change the pass/fail verdict — it enriches the error signal
   so the controller (R2) can respond differently (wrong logic → new approach; bad environment
   → same logic, different path/tool)

**Aggregation rule (code-enforced)**:
- ALL criteria `pass` → `SubTaskOutcome { status: matched }`
- ANY criterion `fail` → `SubTaskOutcome { status: failed }` — no exceptions, no holistic override

The only LLM judgment permitted after the per-criterion phase is writing the
`CorrectionSignal.what_to_do` — which criterion failed, its class, and what specific
change would satisfy it.

**On plausible reasoning**: the `gap_trajectory` in `SubTaskOutcome` accumulates evidence
across retry attempts — this is the Pólya mechanism. A criterion that failed on attempt 1
and passed on attempt 2 is evidence the approach works; a criterion that fails consistently
across all attempts is strong evidence of a logical failure requiring replanning. R4b and
GGS use this trajectory; R4a's job is only to supply accurate per-attempt binary evidence.

**Skills**:
- Evaluate each success criterion independently against tool evidence
- Compute a targeted `CorrectionSignal` naming the specific failed criterion
- Re-invoke Executor with the `CorrectionSignal` when gap is non-zero and retry budget remains
- Track gap scores across retry cycles (the trajectory)
- Emit `SubTaskOutcome { status: matched }` when all criteria pass
- Emit `SubTaskOutcome { status: failed }` when retry budget exhausted with any criterion unmet

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Executor (R3) | `ExecutionResult` JSON |
| Receives | Planner (R2) | `SubTask` JSON (evaluation rubric, carried with dispatch) |
| Produces | Executor (R3) | `CorrectionSignal` JSON (internal loop) |
| Produces | Meta-Validator (R4b) | `SubTaskOutcome` JSON |

```json
CorrectionSignal {
  "subtask_id":        "string",
  "attempt_number":    "integer",
  "failed_criterion":  "string",              // the exact criterion string that failed
  "failure_class":     "logical | environmental",
  "what_was_wrong":    "string",
  "what_to_do":        "string"
}

SubTaskOutcome {
  "subtask_id":       "string",
  "parent_task_id":   "string",
  "status":           "matched | failed",
  "output":           "any",
  "failure_reason":   "string | null",
  "criteria_verdicts": [
    {
      "criterion":     "string",
      "verdict":       "pass | fail",
      "failure_class": "logical | environmental | null",  // null when verdict == pass
      "evidence":      "string"
    }
  ],
  "gap_trajectory": [
    {
      "attempt":        "integer",
      "failed_criteria": [
        { "criterion": "string", "failure_class": "logical | environmental" }
      ]
    }
  ]
}
```

**Does NOT**:
- Reason holistically about whether the output "seems correct" — per-criterion only
- Override a `fail` verdict because the overall result looks plausible
- Decide what the next sub-task should be (R2)
- Execute any action (R3)
- Assess whether the overall task goal is satisfied (R4b)
- Merge results from multiple agents (R4b)
- Write to Shared Memory

---

## R4b — Meta-Validator

**Mission**: Collect all `SubTaskOutcome` objects for a task, gate on all passing,
merge passing outputs into a unified result, and verify the merged result against the
original `TaskSpec`. If a partial or wrong result is accepted and delivered to the user,
or if a task is silently abandoned, this role is accountable.

**Loop position**: Sensor of the medium loop. Produces the error signal (`ReplanRequest`)
for the controller. In steady state the controller is GGS; in MVP it is the Planner in
simplified capacity.

### Fan-in Gate (code-enforced, before LLM)

When all expected `SubTaskOutcome` objects have arrived:

```
if any outcome.status == "failed":
    → emit ReplanRequest immediately
    → LLM is NOT invoked
    → no holistic reasoning about whether the failure matters

if all outcomes.status == "matched":
    → invoke LLM to merge outputs and verify against TaskSpec
```

This is a hard code gate, not an LLM instruction. The LLM's role in R4b is **only**
output merging and TaskSpec verification — it has no adjudication authority over
pass/fail.

**Skills**:
- Receive `DispatchManifest` to know when all parallel outcomes have arrived
- Apply the fan-in gate (code-enforced)
- Invoke LLM to merge all `matched` outputs into a single coherent result
- Verify merged result against `DispatchManifest.task_criteria` (same per-criterion model as R4a;
  criteria were derived by R2 and carried in the manifest — R4b does not re-derive them)
- Compute `gap_trend` from correction history across subtasks
- Emit `ReplanRequest` when gate fails or merged result fails task_criteria verification
- Write `MemoryEntry` to R5 and deliver final result to user on acceptance

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Agent-Validator (R4a) | `SubTaskOutcome` JSON (one per sub-task in manifest) |
| Receives | Planner (R2) | `DispatchManifest` JSON (carries task_criteria; R4b does not receive TaskSpec) |
| Produces | Planner (R2) | `ReplanRequest` JSON (gate fail or task_criteria verification fail) |
| Produces | Shared Memory (R5) | `MemoryEntry` JSON (on acceptance) |
| Produces | User | Final merged result (text) |

```json
ReplanRequest {
  "task_id":          "string",
  "gap_summary":      "string",
  "failed_subtasks":  ["string"],
  "correction_count": "integer",
  "gap_trend":        "improving | stable | worsening",
  "recommendation":   "replan | abandon"
}

MemoryEntry {
  "entry_id":   "string",
  "task_id":    "string",
  "type":       "episodic | procedural",
  "content":    "any",
  "tags":       ["string"],
  "timestamp":  "ISO8601"
}
```

**Does NOT**:
- Invoke the LLM when any subtask failed — gate fires first, LLM is bypassed
- Override the fan-in gate through holistic reasoning
- Evaluate individual sub-task output against local criteria (R4a did this already)
- Execute any action (R3)
- Decompose or assign sub-tasks (R2)
- Consolidate or cross-link memory entries (deferred — Dreamer)

---

## R5 — Shared Memory

**Mission**: Persist task history and accepted results, and serve them reliably on
demand. If valid data is lost, corrupted, returned in wrong order, or silently missing
from a query result, this role is accountable.

**Loop position**: State store. Persistent substrate that loop participants read from
(prior experience) and write to (accepted outcomes and failure lessons).

**Skills**:
- Store `MemoryEntry` objects (episodic and procedural) keyed by entry ID
- Retrieve entries by task ID, tags, or keyword query against serialised content
- Return results ranked by recency and relevance
- Enforce write permission: only R4b may write

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives (write) | Meta-Validator (R4b) | `MemoryEntry` JSON |
| Receives (read) | Planner (R2) | Query (intent string + key terms) |
| Produces | Planner (R2) | `MemoryEntry[]` ranked by relevance |

**Does NOT**:
- Reorganize, summarize, or cross-link entries (deferred — Dreamer)
- Evaluate or judge content quality
- Accept writes from R1, R2, R3, R4a, or R6

---

## R6 — Auditor

**Mission**: Observe all inter-role communications and detect correctness violations,
boundary breaches, and convergence failures; report findings to the human operator. If
systematic failures go undetected and unreported, this role is accountable.

**Loop position**: Lateral observer. Outside all feedback loops. Its principal is the
human operator — no agent can instruct, suppress, or influence it.

**Skills**:
- Tap the message bus read-only — receives copies of all inter-role messages
- Detect "Does NOT" boundary violations
- Track convergence health: gap_trend across successive ReplanRequest cycles
- Detect role drift: systematic behaviour changes over time
- Detect anomalies: excessive retries, repeated replanning without improvement, duplicate subtask IDs, fan-in incompleteness
- Produce structured `AuditReport`; maintain immutable `AuditLog`
- Respond to on-demand `AuditQuery` from human operator; publish periodic reports

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives (observe) | Message bus | Copies of all inter-role messages (read-only) |
| Receives | Human operator | `AuditQuery` |
| Produces | Human operator | `AuditReport` JSON |
| Produces | Audit Log | `AuditEvent` JSON |

**Does NOT**:
- Participate in any feedback loop or send messages to any agent
- Issue corrections, instructions, or interventions to any role
- Modify Shared Memory or any operational data
- Be configured or suppressed by any agent — only the human operator

---

## Interaction Diagram (v0.6)

```
                 ┌─────────────────── MESSAGE BUS ────────────────────────┐
                 │  (all inter-role messages pass through here)           │
                 │                              ┌──── R6 Auditor ────┐   │
                 │                              │  (read-only tap)   │   │
                 │                              └──────────┬─────────┘   │
                 └─────────────────────────────────────────│─────────────┘
                                                           │ AuditReport
                                                           ▼
                                                    Human Operator

                          [MEDIUM LOOP - MVP simplified]
User
 │ free text
 ▼
[R1]──TaskSpec──►[R2 Planner]
                  │   ▲  ▲
      ┌───────────┤   │  └────────── MemoryEntry[] ◄── [R5 Shared Memory]
      │  calibrate│   │                                       ▲
      │  constrain│   │ ReplanRequest                         │ MemoryEntry (write)
      │  ──────►  │   │ (code gate: any failed → replan)      │
      │  plan     │   │                                        │
      │           │   └─────────────────── [R4b Meta-Validator]┘
      │           │                          ▲
      │  SubTask[]│                          │ SubTaskOutcome[]
      │  (IDs by  │                          │ (all matched → merge+verify)
      │  runtime) │                          │ (any failed → gate fires)
      │           │                          │
      └───────────┴──►[R3 × N Executors]──►[R4a × N Agent-Validators]
                                              (per-criterion evaluation)
                                              ALL pass → matched
                                              ANY fail → failed (+ failure_class)
```

---

## Accountability Map

| Failure | Accountable Role |
|---|---|
| System acts on wrong or ambiguously specified intent | R1 Perceiver |
| Success criteria wrong, vague, or not independently falsifiable | R2 Planner |
| Goal not achieved despite correct execution; prior failure lessons not applied | R2 Planner |
| Feasible sub-task not correctly executed | R3 Executor |
| Gap between sub-task output and goal goes unresolved or unreported | R4a Agent-Validator |
| Failed subtask accepted as success; merged result fails task_criteria | R4b Meta-Validator |
| Data lost, corrupted, or wrongly retrieved | R5 Shared Memory |
| Systematic failures go undetected and unreported to human operator | R6 Auditor |

---

## Key Invariants (enforced in code, not prompt)

| Invariant | Enforced by |
|---|---|
| SubTask IDs are UUIDs assigned by Go runtime, never by LLM | Dispatcher |
| TaskSpec carries no success_criteria — R2 derives all criteria from intent | R2 planner prompt |
| task_criteria live in DispatchManifest; R4b reads them from there, not from TaskSpec | R4b code |
| R4b LLM is not invoked when any SubTaskOutcome.status == "failed" | R4b code gate |
| R4a verdict is aggregation of per-criterion booleans; one false = failed | R4a scoring loop |
| R4a criterion verdict includes failure_class (logical \| environmental) | R4a LLM output schema |
| R2 plan cannot reuse an approach flagged in a MUST NOT constraint | R2 plan validator |
| Memory calibration runs before every plan, including replans | R2 protocol |
| Memory calibration (Steps 1–3) involves no LLM call; bounded at 10 entries | R2 Go code |

---

## Deferred to v0.7

| Component | Design specification needed before implementation |
|---|---|
| Goal Gradient Solver | Dynamic-differential controller between R4b (sensor) and R2 (actuator). Inputs: `ReplanRequest` and `gap_trajectory` from `SubTaskOutcome[]`. Outputs: directed plan adjustment |
| Dreamer (agent-level) | Async memory consolidation after sub-task completion |
| Dreamer (metaagent-level) | Cross-task consolidation; produces semantic entries capturing patterns across sessions |

---

## Open Questions for v0.6 Implementation

| # | Question | Blocks |
|---|---|---|
| Q1 | How does R2 determine "approach reuse" for MUST NOT enforcement — tool name match, intent similarity, or LLM-judged equivalence? | R2 plan validator |
| Q2 | What is the retry budget for R4a — fixed count (current: 2) or gap-score threshold? | R4a |
| Q3 | How does R2 differ its replan from a prior failed plan when the root cause is environmental (failure_class="environmental") rather than logical? The failure_class in the ReplanRequest gives R2 the signal; the replanning strategy is: environmental → same tool sequence but different path/parameters; logical → different tool or approach entirely | R2 |
| Q4 | Should criteria_verdicts in SubTaskOutcome be persisted to MemoryEntry so future R2 calibration knows which specific criteria tend to fail for certain task types? | R4a, R5 |
| Q5 | When the Dreamer arrives in v0.7, should R5 expose a separate "semantic" vs "raw" read API, or should R5 remain schema-agnostic and the Dreamer simply write entries with a different type tag (e.g. type: "semantic")? | R5, Dreamer |
| Q6 | Rule #1 (best effort without self-harm) — does "not harm itself" extend to the user's environment (destructive operations, irreversible writes)? If yes, Rule #1 subsumes execution safety. If no, that is a separate Rule #2. Conclusion pending discussion. | All roles |
