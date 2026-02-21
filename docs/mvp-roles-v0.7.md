# MVP Role Definitions

**Version**: 0.7
**Status**: Draft
**Date**: 2026-02-21
**Scope**: Eight roles. Goal Gradient Solver (GGS) promoted from deferred to implemented.
Dreamer deferred to v0.8.

---

## Changelog from v0.6

| Change | Reason |
|---|---|
| GGS implemented as R7 — sits between R4b and R2 in the medium loop | v0.6 left R4b sending a plain "failed, retry" signal to R2 with no directional content. R2 had no way to know whether to change tools, change paths, or give up. The gradient was computed but delivered into a void |
| Loss function formalised: L = α·D(I, R_t) + β_eff·P(R_t) + λ·Ω(C_t) | Replanning without a loss signal is random search. The loss function converts the error signal into a directed gradient that R2 can follow autonomously, without human intervention |
| β_eff = β·(1 − Ω) — adaptive weight on process plausibility | As budget exhausts, whether the process is plausible matters less than whether D is decreasing. Static β would continue recommending "refine" even when Ω → 1 |
| Ω(C_t) now includes wall-clock time, not just replan count | Replan count alone does not capture latency cost. A fast-replanning task and a slow one consume different user patience. Ω must be two-dimensional |
| Local minimum detection added: plateau condition triggers break_symmetry | Without plateau detection, GGS would recommend "refine" indefinitely on a flat trajectory — equivalent to the current system's naive retry. Break-symmetry is the escape mechanism |
| R4b no longer computes gap_trend — GGS owns gradient computation | Separates observation (R4b) from control (GGS). R4b's job is fan-in and raw data delivery; gradient is control-theoretic work |
| R2 now receives PlanDirective (from GGS) instead of ReplanRequest (from R4b) | PlanDirective carries: loss breakdown, gradient signal, blocked_tools, directive type (refine / change_approach / break_symmetry / abandon), and rationale. R2 can now make a principled plan adjustment |
| Dynamic MUST NOT injection: GGS appends blocked_tools to R2's MUST NOT set | Memory-sourced MUST NOTs are static (recorded from prior tasks). GGS-sourced MUST NOTs are dynamic (derived from the current task's failure trajectory). Both feed R2's plan validator |

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

In v0.7 the medium loop is complete: R4b (sensor) → GGS (controller) → R2 (actuator).
The GGS replaces R2's self-directed replanning with gradient-directed planning.

---

## Architectural Constraint: Observable Message Bus

All inter-role communications must pass through a shared message bus that the Auditor
can tap as a read-only observer. Direct point-to-point calls between roles are not
permitted — every message must be routable.

---

## Role Index

| ID | Role | Lives in | Loop position | Mission Summary |
|---|---|---|---|---|
| R1 | Perceiver | Entry point | Reference signal | If the task is misunderstood, this role is responsible |
| R2 | Planner | Metaagent | Actuator | If the goal is not achieved despite valid execution, this role is responsible |
| R3 | Executor | Effector Agent | Plant | If a feasible sub-task is not correctly executed, this role is responsible |
| R4a | Agent-Validator | Effector Agent | Sensor + Controller (fast loop) | If a gap between outcome and sub-task goal goes unresolved or unreported, this role is responsible |
| R4b | Meta-Validator | Metaagent | Sensor (medium loop) | If the merged result is accepted outside plausible range or a task is silently abandoned, this role is responsible |
| R5 | Shared Memory | Infrastructure | State store | If valid data is lost, corrupted, or wrongly retrieved, this role is responsible |
| R6 | Auditor | Infrastructure | Lateral observer | If systematic failures go undetected and unreported to the human operator, this role is responsible |
| R7 | Goal Gradient Solver | Metaagent | Controller (medium loop) | If the replanning direction is wrong, too conservative, or too aggressive for the observed gradient, this role is responsible |

---

## R1 — Perceiver

*(Unchanged from v0.6. Reproduced for completeness.)*

**Mission**: Faithfully translate raw user input into a structured intent specification.
R1 is a transducer — it carries the user's signal into the system without interpretation
or elaboration.

**Does NOT**: Derive success criteria (R2). Access memory (R5). Elaborate the goal.

**Contract**: Receives free-text → produces `TaskSpec` JSON.

```json
TaskSpec {
  "task_id":    "string",
  "intent":     "string",
  "constraints": { "scope": "string | null", "deadline": "ISO8601 | null" },
  "raw_input":  "string"
}
```

---

## R2 — Planner

**Mission**: Own the path from task specification to final result. If the overall goal is
not achieved despite Executors performing correctly — because decomposition was wrong,
sequencing was wrong, or prior failures and GGS directives were ignored — this role is
accountable.

**Loop position**: Actuator of the medium loop. In v0.7 R2 no longer absorbs the
controller role — that belongs to GGS. R2 receives a `PlanDirective` and executes it.

**Changes from v0.6**:
- Receives `PlanDirective` from R7 (GGS) instead of `ReplanRequest` from R4b
- `PlanDirective.blocked_tools` extends the MUST NOT set dynamically — same code-enforced plan validator applies
- `PlanDirective.directive` field constrains what kind of change R2 must make (refine / change_approach / break_symmetry / abandon)
- R2 may not override a `break_symmetry` directive by generating a near-identical plan

**Memory Calibration Protocol**: Unchanged from v0.6. Runs before every plan and replan.
MUST NOT set = memory-sourced constraints ∪ GGS-sourced `blocked_tools`.

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Perceiver (R1) | `TaskSpec` JSON |
| Receives | GGS (R7) | `PlanDirective` JSON |
| Receives | Shared Memory (R5) | `MemoryEntry[]` |
| Produces | Executor (R3) | `SubTask` JSON (subtask_id assigned by runtime) |
| Produces | Meta-Validator (R4b) | `DispatchManifest` JSON |
| Produces | Shared Memory (R5) | Read query + `MemoryEntry` (on completion/failure) |

```json
SubTask {
  "parent_task_id":   "string",
  "intent":           "string",
  "success_criteria": [
    { "criterion": "string", "mode": "verifiable | plausible" }
  ],
  "context":  "string",
  "deadline": "ISO8601 | null",
  "sequence": "integer"
}

DispatchManifest {
  "task_id":       "string",
  "subtask_ids":   ["string"],
  "task_criteria": [
    { "criterion": "string", "mode": "verifiable | plausible" }
  ],
  "dispatched_at": "ISO8601"
}
```

**Does NOT**: Execute actions (R3). Evaluate output (R4a, R4b). Compute gradient or loss (R7). Override a `break_symmetry` directive with a near-identical plan.

---

## R3 — Executor

*(Unchanged from v0.6.)*

**Mission**: Execute exactly one assigned sub-task and return a concrete, verifiable result.

**Contract**: Receives `SubTask` + `CorrectionSignal` → produces `ExecutionResult`.

```json
ExecutionResult {
  "subtask_id": "string",
  "status":     "completed | failed",
  "output":     "any",
  "tool_calls": ["string"]
}
```

---

## R4a — Agent-Validator

*(Unchanged from v0.6.)*

**Mission**: Close the gap between the Executor's output and the sub-task goal via the fast correction loop.

Per-criterion independent evaluation. Mode set by R2 at planning time.
`failure_class` (logical | environmental) enriches the error signal but does not change pass/fail.

**Contract**: Receives `ExecutionResult` → produces `CorrectionSignal` | `SubTaskOutcome`.

```json
CorrectionSignal {
  "subtask_id":       "string",
  "attempt_number":   "integer",
  "failed_criterion": "string",
  "failure_class":    "logical | environmental",
  "what_was_wrong":   "string",
  "what_to_do":       "string"
}

SubTaskOutcome {
  "subtask_id":     "string",
  "parent_task_id": "string",
  "status":         "matched | failed",
  "output":         "any",
  "failure_reason": "string | null",
  "criteria_verdicts": [
    {
      "criterion":    "string",
      "mode":         "verifiable | plausible",
      "verdict":      "pass | fail",
      "failure_class": "logical | environmental | null",
      "evidence":     "string"
    }
  ],
  "gap_trajectory": [
    {
      "attempt": "integer",
      "failed_criteria": [
        { "criterion": "string", "failure_class": "logical | environmental" }
      ]
    }
  ]
}
```

---

## R4b — Meta-Validator

**Mission**: Collect all `SubTaskOutcome` objects for a task, gate on all passing, merge
passing outputs into a unified result, and verify the merged result against the original
task criteria. If a partial or wrong result is accepted, or a task is silently abandoned,
this role is accountable.

**Loop position**: Sensor of the medium loop. Delivers raw outcome data to GGS on failure.

**Changes from v0.6**:
- On gate failure: sends `ReplanRequest` **to R7 (GGS)**, not to R2 directly
- No longer computes `gap_trend` — GGS owns gradient computation
- `ReplanRequest` now carries full `criteria_verdicts` and `gap_trajectory` arrays so GGS has all raw data needed for gradient computation

**Fan-in Gate (code-enforced, before LLM)**: Unchanged.

```
if any outcome.status == "failed":
    → emit ReplanRequest to GGS immediately
    → LLM is NOT invoked

if all outcomes.status == "matched":
    → invoke LLM to merge outputs and verify against task_criteria
```

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Agent-Validator (R4a) | `SubTaskOutcome` JSON |
| Receives | Planner (R2) | `DispatchManifest` JSON |
| Produces | GGS (R7) | `ReplanRequest` JSON (gate fail or task_criteria fail) |
| Produces | Shared Memory (R5) | `MemoryEntry` JSON (on acceptance) |
| Produces | User | Final merged result |

```json
ReplanRequest {
  "task_id":           "string",
  "gap_summary":       "string",
  "failed_subtasks":   ["string"],
  "correction_count":  "integer",
  "elapsed_ms":        "integer",
  "outcomes":          [SubTaskOutcome],  // full data for GGS gradient computation
  "recommendation":    "replan | abandon"
}
```

**Does NOT**: Compute gradient or loss (R7). Send ReplanRequest to R2 directly. Override fan-in gate.

---

## R7 — Goal Gradient Solver (GGS)

**Mission**: Translate R4b's raw failure signal into a directed planning constraint for R2.
If the replanning direction is wrong — too conservative when convergence is possible, too
aggressive when refinement would suffice, or failing to escape a local minimum — this role
is accountable.

**Loop position**: Controller of the medium loop. Sits between R4b (sensor) and R2 (actuator).
Its output is a `PlanDirective` — a structured, gradient-informed instruction that tells R2
not just *that* replanning is needed but *what kind* of change to make.

### The Loss Function

```
L = α·D(I, R_t) + β_eff·P(R_t) + λ·Ω(C_t)

where:
  β_eff = β · (1 − Ω(C_t))   [process plausibility weight decays as budget exhausts]
```

**D(I, R_t) — intent-result distance** [0, 1]

Measures the gap between the user's intent and the current result. Aggregated from
`criteria_verdicts` across all subtasks:

- `verifiable` criterion with verdict `fail` → contributes 1.0 to numerator
- `plausible` criterion with verdict `fail` → weighted by trajectory consistency:
  - Failed on all N attempts → weight 1.0
  - Failed on k of N attempts → weight k/N
- `D = Σ(weighted_failures) / Σ(total_criteria)`

**P(R_t) — process implausibility** [0, 1]

Measures how wrong the *approach* is, independent of whether the result is wrong.
Derived from `failure_class` across all failed criteria:

```
logical_failures      = count of failed criteria with failure_class == "logical"
environmental_failures = count of failed criteria with failure_class == "environmental"
total_failures        = logical + environmental

P = logical_failures / total_failures   (0 when all environmental; 1 when all logical)
```

High P → the approach is fundamentally wrong (change it).
Low P → the approach is sound but the environment blocked it (change path/parameters).

**Ω(C_t) — resource cost** [0, 1]

Captures both budget exhaustion and wall-clock time:

```
Ω = w₁·(replan_count / maxReplans) + w₂·(elapsed_ms / time_budget_ms)
```

Default weights: w₁ = 0.6, w₂ = 0.4. As Ω → 1, the cost of another replan round
approaches the cost of the gap itself — the system should abandon rather than continue.

### Gradient Computation

The gradient ∇L is approximated by finite difference across consecutive replan rounds:

```
∇L_t = L_t − L_{t−1}
```

GGS maintains `L_prev` in memory across rounds for the same task_id.

**Plateau detection**: if `|∇L_t| < ε` (default ε = 0.1) AND `D_t > δ` (default δ = 0.3),
the system is in a local minimum. Naive refinement will not escape it. `break_symmetry`
directive is triggered.

### Directive Decision Table

| ∇L | D | P | Ω | Directive |
|---|---|---|---|---|
| < 0 (improving) | any | any | < 0.8 | `refine` — on the right track; tighten parameters |
| ≈ 0 (plateau) | > δ | high (logical) | < 0.8 | `break_symmetry` — block all tried tools; demand novel approach |
| ≈ 0 (plateau) | > δ | low (environmental) | < 0.8 | `change_path` — same tool sequence, different target/parameters |
| > 0 (worsening) | > δ | high (logical) | < 0.8 | `change_approach` — escalate; explicitly different tool class |
| > 0 (worsening) | > δ | low (environmental) | < 0.8 | `refine` with path hint — environment is the issue |
| any | any | any | ≥ 0.8 | `abandon` — budget pressure overrides gradient direction |

### Dynamic MUST NOT Injection

When directive is `break_symmetry` or `change_approach`, GGS appends all tools used
in the failing subtask(s) to `blocked_tools`. R2 adds these to its MUST NOT set for
the next plan. The plan validator rejects any plan that uses a blocked tool.

This extends the memory-sourced MUST NOT constraints (which are task-type-scoped and
persistent) with session-scoped, task-specific MUST NOTs derived from the live gradient.

**Skills**:
- Receive `ReplanRequest` from R4b carrying full `SubTaskOutcome[]` data
- Compute D, P, Ω, L for the current round
- Compute ∇L from previous round's L (maintained per task_id)
- Detect plateau condition
- Select directive from decision table
- Emit `PlanDirective` to R2
- Log loss breakdown and gradient to the bus (Auditor visibility)

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Meta-Validator (R4b) | `ReplanRequest` JSON (with full outcomes data) |
| Produces | Planner (R2) | `PlanDirective` JSON |

```json
PlanDirective {
  "task_id":          "string",
  "loss": {
    "D":     "float",   // intent-result distance [0, 1]
    "P":     "float",   // process implausibility [0, 1]
    "Omega": "float",   // resource cost [0, 1]
    "L":     "float"    // total weighted loss
  },
  "gradient":          "improving | stable | worsening | plateau",
  "directive":         "refine | change_path | change_approach | break_symmetry | abandon",
  "blocked_tools":     ["string"],        // tools R2 must not use in next plan
  "failed_criterion":  "string",          // primary criterion driving D
  "failure_class":     "logical | environmental | mixed",
  "budget_pressure":   "float",           // Ω value for display
  "rationale":         "string"           // human-readable explanation; logged by Auditor
}
```

**Does NOT**:
- Generate sub-tasks or modify the plan directly (R2)
- Observe individual tool calls (that is R4a's domain)
- Merge or verify outputs (R4b)
- Override the fan-in gate (R4b owns that)
- Write to Shared Memory
- Be bypassed: R2 must not receive a ReplanRequest from R4b directly in v0.7

---

## R5 — Shared Memory

*(Unchanged from v0.6.)*

File-backed JSON store. Keyword query. Drains on shutdown. Only metaagent roles
(R2, R4b, Dreamer) may query or write directly.

---

## R6 — Auditor

*(Unchanged from v0.6, but now also observes `PlanDirective` messages from R7.)*

**New detection target**: GGS thrashing — repeated `break_symmetry` directives without
D decreasing → signals that the loss landscape is degenerate for this task type.

---

## Interaction Diagram (v0.7)

```
                 ┌─────────────────── MESSAGE BUS ─────────────────────────┐
                 │  (all inter-role messages pass through here)             │
                 │                              ┌──── R6 Auditor ─────┐    │
                 │                              │  (read-only tap)     │    │
                 │                              └──────────┬──────────┘    │
                 └─────────────────────────────────────────│───────────────┘
                                                           │ AuditReport
                                                           ▼
                                                    Human Operator

                          [MEDIUM LOOP - v0.7 complete]

User
 │ free text
 ▼
[R1]──TaskSpec──►[R2 Planner]◄──────────────────────────── PlanDirective ──[R7 GGS]
                  │    ▲                                                       ▲
      ┌───────────┤    └──── MemoryEntry[] ◄── [R5 Shared Memory]              │
      │  calibrate│                                    ▲                       │
      │  constrain│                                    │ MemoryEntry (write)   │
      │  plan     │                                    │                       │
      │           │                          [R4b Meta-Validator]──ReplanReq──►┘
      │  SubTask[]│                                    ▲
      │  (IDs by  │                                    │ SubTaskOutcome[]
      │  runtime) │                                    │ (all matched → merge+verify)
      │           │                                    │ (any failed → gate → GGS)
      └───────────┴──►[R3 × N Executors]──►[R4a × N Agent-Validators]
                                              (per-criterion evaluation)
                                              ALL pass → matched
                                              ANY fail → failed + failure_class
```

---

## Key Invariants (enforced in code, not prompt)

| Invariant | Enforced by |
|---|---|
| SubTask IDs are UUIDs assigned by Go runtime, never by LLM | Dispatcher |
| TaskSpec carries no success_criteria — R2 derives all criteria | R2 planner prompt |
| task_criteria live in DispatchManifest; R4b reads them from there | R4b code |
| R4b LLM is not invoked when any SubTaskOutcome.status == "failed" | R4b code gate |
| R4b sends ReplanRequest to R7, never directly to R2 | R4b code |
| R4a verdict is aggregation of per-criterion booleans; one false = failed | R4a scoring loop |
| R4a criterion verdict includes failure_class (logical \| environmental) | R4a LLM output schema |
| GGS computes loss and gradient; R2 does not self-direct replanning | R7 owns PlanDirective |
| R2 plan cannot reuse a tool in blocked_tools from PlanDirective | R2 plan validator |
| R2 plan cannot reuse an approach flagged in memory MUST NOT | R2 plan validator |
| Memory calibration (Steps 1–3) involves no LLM call; bounded at 10 entries | R2 Go code |
| GGS emits `abandon` when Ω ≥ 0.8 regardless of gradient signal | R7 decision table |

---

## Loss Hyperparameters (v0.7 defaults)

| Parameter | Default | Meaning |
|---|---|---|
| α | 0.6 | Weight on intent-result distance D |
| β | 0.3 | Weight on process implausibility P (before adaptive scaling) |
| λ | 0.4 | Weight on resource cost Ω |
| w₁ | 0.6 | Ω sub-weight for replan count |
| w₂ | 0.4 | Ω sub-weight for elapsed time |
| ε | 0.1 | Plateau detection threshold for \|∇L\| |
| δ | 0.3 | Minimum D to trigger break_symmetry (below this, consider it converged) |
| abandon_Ω | 0.8 | Ω threshold above which directive becomes `abandon` regardless of gradient |
| time_budget_ms | 120000 | Default time budget per task (2 min); configurable |

These are initial values. They should be tuned empirically once GGS is deployed.
The Auditor's gap_trend data across sessions provides the signal for tuning.

---

## Open Questions for v0.7 Implementation

| # | Question | Blocks |
|---|---|---|
| Q1 | How does GGS handle the first replan round when L_prev is undefined? Use D as a proxy for L (∇L = 0, treat as stable/plateau if D is high) | R7 |
| Q2 | Should GGS persist L_prev across sessions (in R5) or only within a single task's lifetime? Cross-session persistence enables better gradient estimation for recurring task types | R7, R5 |
| Q3 | How should `change_path` directive communicate the *new* path hint to R2 — as free text in rationale, or as a structured `suggested_alternatives` field? | R7, R2 |
| Q4 | When multiple subtasks fail with different failure_classes, how does GGS pick the dominant class for the directive? Proposed: majority vote; tie → "mixed" class → `change_approach` | R7 |
| Q5 | Should the `abandon` directive from GGS be distinguishable from the `maxReplans` abandon in R4b? Current: both produce FinalResult with failure summary. Unifying them under GGS would give a consistent abandonment path | R7, R4b |
| Q6 | How does R2's plan validator check `blocked_tools` — exact string match on tool name, or keyword match on intent? Proposed: exact match on tool name; R3's tool_calls format already carries the tool name prefix | R2, R7 |

---

## Accountability Map

| Failure | Accountable Role |
|---|---|
| System acts on wrong or ambiguously specified intent | R1 Perceiver |
| Success criteria wrong, vague, or not independently falsifiable | R2 Planner |
| Goal not achieved despite correct execution; prior failures or GGS directives ignored | R2 Planner |
| Feasible sub-task not correctly executed | R3 Executor |
| Gap between sub-task output and goal goes unresolved or unreported | R4a Agent-Validator |
| Failed subtask accepted as success; merged result fails task_criteria | R4b Meta-Validator |
| Replanning direction wrong; local minimum not escaped; budget misjudged | R7 GGS |
| Data lost, corrupted, or wrongly retrieved | R5 Shared Memory |
| Systematic failures go undetected and unreported to human operator | R6 Auditor |

---

## Deferred to v0.8

| Component | Design specification needed before implementation |
|---|---|
| Dreamer (agent-level) | Async memory consolidation after sub-task completion |
| Dreamer (metaagent-level) | Cross-task consolidation; produces semantic entries capturing patterns across sessions |
| GGS hyperparameter tuning | Empirical calibration of α, β, λ, w₁, w₂, ε, δ from Auditor session data |
| Semantic memory layer in R5 | Separate read API for pre-curated semantic entries (Dreamer output); R2 calibration degrades to near-zero cost |
