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
| v0.6 criteria ownership design implemented in code | R1 no longer outputs success_criteria (it was still doing so in code despite the v0.6 spec). R2 now outputs `{"task_criteria":[...],"subtasks":[...]}` wrapper. task_criteria are plain strings in `DispatchManifest`; R4b reads them from there. |
| PlanDirective pipeline line shows D, P, ∇L, Ω; spinner shows rationale | All four GGS metrics are now visible in the terminal pipeline display. The gradient arrow (↑ ↓ ⊥ →) encodes direction; D, P, ∇L, Ω are shown numerically. While R2 replans, the spinner shows the human-readable rationale explaining why the directive was chosen. |
| `<think>` blocks stripped from all LLM output before JSON parsing | Reasoning models (e.g. deepseek-r1) emit `<think>...</think>` blocks in raw completions. These caused `json.Unmarshal` to fail with `invalid character '<'`, which R4a classified as infrastructure errors and marked subtasks failed without retry. `StripThinkBlocks()` is now called as the first step of `StripFences()` for all roles. |
| `FinalResult` is a trajectory record; Loss/GradL/Replans populated on all GGS paths | GGS computed D/∇L/Ω on every cycle but previously discarded them from `FinalResult`. Now both `processAccept` (D=0) and the abandon path (D>0) populate `FinalResult.Loss`, `FinalResult.GradL`, and `FinalResult.Replans`. This closes the observability loop: every medium-loop cycle — including the final one — is visible in the pipeline display. Resolves Q5 (see below). |
| Pipeline checkpoints at all key message types | `SubTaskOutcome` failed now shows R4a's final score. `ReplanRequest` shows N/M failed count and total. `FinalResult` always renders a flow line showing D/∇L/Ω. Abandon detection uses `Loss.D > 0` (code-driven, not text-parsing). |

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

**Mission**: Receive the user's signal and carry it into the system with full fidelity.
R1 is a receiver, not a resolver. Its responsibility is to preserve the user's intent —
including fuzziness — in structured form. Resolving ambiguity into a precise plan is R2's
domain. R1 must not over-specify, elaborate, or pre-interpret what the user said.

**Clarification policy**: Ask the user a clarifying question ONLY when the input is so
underspecified that no reasonable interpretation is possible (e.g. a single word with no
context). Any fuzziness that R2 can reasonably resolve from planning-domain knowledge
must pass through untouched. Over-asking erodes user trust and moves accountability for
interpretation from R2 (where it belongs) to R1 (where it does not).

**Reference signal**: `raw_input` preserves the user's original words verbatim. It is the
independent reference signal for the entire task lifecycle — R2's interpretation
(expressed as `task_criteria`) can always be compared against it to detect translation
drift. R1 must not clean up, rephrase, or summarise `raw_input`.

**Does NOT**: Resolve ambiguity (R2). Derive criteria (R2). Access memory (R5). Interpret intent.

**Contract**: Receives free-text → produces `TaskSpec` JSON.

```json
TaskSpec {
  "task_id":    "string",
  "intent":     "string",   // one-sentence faithful restatement; may still be fuzzy
  "constraints": { "scope": "string | null", "deadline": "ISO8601 | null" },
  "raw_input":  "string"    // verbatim — never summarised or rephrased
}
```

---

## R2 — Planner

**Mission**: Interpret the user's intent and own the path to its realisation. R2 is the
"wise translator" — the first step is not to decompose but to understand. It reads the
(possibly fuzzy) intent from `TaskSpec`, draws on memory of prior tasks, and produces a
precise operational interpretation in the form of `task_criteria`. Decomposition follows
from that interpretation. If the goal is not achieved — because interpretation was wrong,
decomposition was wrong, sequencing was wrong, or GGS directives were ignored — this
role is accountable.

**Two-step operation**:
1. **Interpret**: translate `TaskSpec.intent` into concrete, falsifiable `task_criteria`.
   This is where fuzziness is resolved. The criteria are R2's commitment to what success
   means — and the primary surface on which R4b will hold R2 accountable.
2. **Decompose**: break the interpreted goal into the minimum set of `SubTask` objects
   that, when executed and combined, satisfy the `task_criteria`.

**Graceful failure (on GGS `abandon` directive)**: R2 must not produce a bare failure
message. It must instead synthesise:
- A partial result: what was actually accomplished across completed subtasks.
- Next-move suggestions: 2–3 concrete actions the user could take to make progress,
  given what failed and why.
This turns the worst-case path from a dead end into a collaborative handoff.

**Loop position**: Actuator of the medium loop. In v0.7 R2 no longer absorbs the
controller role — that belongs to GGS. R2 receives a `PlanDirective` and executes it.

**Changes from v0.6**:
- Receives `PlanDirective` from R7 (GGS) instead of `ReplanRequest` from R4b
- `PlanDirective.blocked_tools` extends the MUST NOT set dynamically — same code-enforced plan validator applies
- `PlanDirective.directive` field constrains what kind of change R2 must make (refine / change_approach / break_symmetry / abandon)
- R2 may not override a `break_symmetry` directive by generating a near-identical plan
- Graceful failure on `abandon`: partial result + next-move suggestions (deferred to v0.8 implementation)

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
  "subtask_id":       "string",         // UUID assigned by runtime, never by LLM
  "parent_task_id":   "string",
  "intent":           "string",
  "success_criteria": ["string"],        // plain string assertions; {criterion, mode} deferred to v0.8
  "context":          "string",
  "deadline":         "ISO8601 | null",
  "sequence":         "integer"
}

DispatchManifest {
  "task_id":       "string",
  "subtask_ids":   ["string"],
  "task_spec":     "TaskSpec | null",    // embedded for R4b context
  "dispatched_at": "ISO8601",
  "task_criteria": ["string"]            // plain string assertions about COMBINED output; R4b validates against these
}
```

**Note on criteria format**: v0.6 spec proposed `{criterion, mode}` objects to distinguish `verifiable` from `plausible` criteria. This was simplified to plain strings in the current implementation; `failure_class` in `SubTaskOutcome` partially captures the same signal. Structured mode is deferred to v0.8.

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
passing outputs into a unified result, and verify the merged result against the task
criteria written by R2. If a partial or wrong result is accepted, or a task is silently
abandoned, this role is accountable.

**Intelligence constraint**: R4b's reasoning capability must be **≥ R2's**. This is a
structural requirement, not a preference.

The reason: R4b is the independent check on R2's interpretation. R2 writes both the plan
and the `task_criteria` — a less capable R4b can be outmaneuvered, accepting results that
satisfy the letter of R2's criteria but not the spirit of the user's intent. The
independence of the validation is only meaningful if the validator is at least as capable
as the planner it is checking.

**Bias asymmetry**: R2 must be optimistic and action-oriented to commit to a plan. R4b
must be conservative and skeptical to protect result quality. The asymmetric cost makes
this correct: a false-accept delivers a wrong result to the user (unrecoverable without
user intervention); a false-replan wastes one round but the system corrects itself. When
in doubt, R4b should reject.

**Implication for model selection**: when assigning models to roles, R4b's model tier
must be ≥ R2's. Deploying a cheaper model at R4b to save cost undermines the validation
contract.

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

Ω ≥ 0.8 (`abandon`) always wins regardless of gradient. Otherwise:

| ∇L | D | P | Directive |
|---|---|---|---|
| < 0 (improving) | any | any | `refine` |
| ≈ 0 (plateau) | > δ | ≤ 0.5 (environmental) | `change_path` |
| ≈ 0 (plateau) | > δ | > 0.5 (logical) | `break_symmetry` |
| > 0 (worsening) | > δ | ≤ 0.5 (environmental) | `refine` (with path hint) |
| > 0 (worsening) | > δ | > 0.5 (logical) | `change_approach` |

**The P = 0.5 pivot**: P measures whether failures are logical (same approach keeps failing — the
logic is wrong) or environmental (approach is sound but the specific target/path is blocked).
First replan → P is typically low (one failure could be bad luck). Repeated failure with the same
approach → P rises above 0.5 → logical origin → escalate.

### Directive Semantics

**`refine`** — Triggered by improving gradient (loss decreasing) or worsening with environmental
origin. The approach is correct or at worst mis-aimed. R2 keeps the same tool sequence and
tightens parameters: narrower query, more precise path, different search terms.
- `blocked_tools`: none (tools are working, just not perfectly)
- Rationale shows: `"Loss decreasing (∇L=X) — on the right track"` or `"Environmental issue (P≤0.5) — adjust path"`

**`change_path`** — Triggered by plateau with environmental origin (P ≤ 0.5). The approach is
sound but something about the specific path is wrong (file moved, search returned nothing, API
returned empty). Same tool class, different target or parameters.
- `blocked_tools`: none
- Rationale shows: `"Plateau (|∇L|<ε, D>δ). Environmental origin (P=X). Same approach, different target/parameters."`

**`break_symmetry`** — Triggered by plateau with logical origin (P > 0.5). The approach itself is
fundamentally wrong — the same tools keep being called with the same logic and keep failing.
Must escape the local minimum by switching to a completely different tool class.
- `blocked_tools`: **all tools used in failing subtasks** (populated by GGS, enforced by R2's plan validator)
- Rationale shows: `"Local minimum (|∇L|<ε, D>δ, P=X). Block all tried tools; demand novel approach."`

**`change_approach`** — Triggered by worsening gradient with logical origin (P > 0.5). Loss is
actively increasing — not just stuck, but getting worse — and the failures are logical.
Must escalate to an explicitly different tool class.
- `blocked_tools`: tools from failing subtasks
- Rationale shows: `"Loss worsening (∇L=X) with logical failures (P>0.5). Use explicitly different tool class."`

**`abandon`** — Triggered when Ω ≥ 0.8 regardless of gradient. Budget pressure overrides all
other signals. GGS delivers `FinalResult` with failure summary directly; R2 is not invoked.

### First-Round Behaviour (Q1 resolved)

On the first replan round, `L_prev` is undefined so `∇L = 0`. This is treated as a plateau
condition: if D > δ, the directive selection proceeds with `∇L ≈ 0`. In practice the first round
is almost always `change_path` or `break_symmetry` depending on P, since D is typically high
on the first failure. This is the correct behaviour — the system should not `refine` on the very
first failure signal without any prior loss baseline.

### Dynamic MUST NOT Injection

When directive is `break_symmetry` or `change_approach`, GGS appends all tools used
in the failing subtask(s) to `blocked_tools`. R2 adds these to its MUST NOT set for
the next plan. The plan validator rejects any plan that uses a blocked tool.

This extends the memory-sourced MUST NOT constraints (which are task-type-scoped and
persistent) with session-scoped, task-specific MUST NOTs derived from the live gradient.

**Skills**:
- Receive `ReplanRequest` from R4b carrying full `SubTaskOutcome[]` data
- Receive `OutcomeSummary` from R4b when all subtasks matched (happy path)
- Compute D, P, Ω, L for the current round
- Compute ∇L from previous round's L (maintained per task_id)
- Detect plateau condition
- Select directive from decision table
- Emit `PlanDirective` to R2 (all non-abandon directives)
- Emit `FinalResult` to User on **both** paths: accept (D=0) and abandon (D>0, Ω≥0.8)
- Populate `FinalResult.Loss`, `FinalResult.GradL`, `FinalResult.Replans` on every emission — this is the trajectory closure that makes every medium-loop cycle observable
- Log loss breakdown and gradient to the bus (Auditor visibility)

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Meta-Validator (R4b) | `ReplanRequest` JSON (with full outcomes data) |
| Receives | Meta-Validator (R4b) | `OutcomeSummary` JSON (all subtasks matched) |
| Produces | Planner (R2) | `PlanDirective` JSON (all non-abandon directives) |
| Produces | User (via bus) | `FinalResult` JSON (accept and abandon paths) |

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
  "budget_pressure":   "float",           // Ω for display (same as loss.Omega)
  "grad_l":            "float",           // ∇L = L_t − L_{t-1}; 0 on first replan round
  "rationale":         "string"           // human-readable explanation; logged by Auditor; shown in UI spinner
}

// FinalResult is the trajectory closure — GGS is the sole emitter on all paths.
// Loss.D == 0.0 on the accept path (all subtasks matched).
// Loss.D > 0.0 on the abandon path (Ω ≥ 0.8 with at least one failed subtask).
// The display layer uses Loss.D > 0 as the programmatic signal to show ❌ vs ✅.
FinalResult {
  "task_id":  "string",
  "summary":  "string",
  "output":   "any",                  // null on abandon path
  "loss": {
    "D":     "float",                 // 0.0 on accept; > 0 on abandon
    "P":     "float",
    "Omega": "float",                 // ≥ 0.8 on abandon
    "L":     "float"
  },
  "grad_l":   "float",               // ∇L across the final round; 0 on first-try accept
  "replans":  "integer"              // number of GGS-directed replan rounds; 0 on first-try accept
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
| TaskSpec carries no success_criteria — R2 derives all criteria | R1 prompt (field removed); R2 planner prompt |
| task_criteria live in DispatchManifest as plain strings; R4b reads them from there | R2 wrapper output; R4b code |
| R4b reasoning capability must be ≥ R2's — validator cannot be weaker than the planner it checks | Model selection policy |
| R4b defaults to reject when evidence is ambiguous — false-replan is recoverable; false-accept is not | R4b LLM prompt |
| R4b LLM is not invoked when any SubTaskOutcome.status == "failed" | R4b code gate |
| R4b sends ReplanRequest to R7, never directly to R2 | R4b code |
| R4a verdict is aggregation of per-criterion booleans; one false = failed | R4a scoring loop |
| R4a criterion verdict includes failure_class (logical \| environmental) | R4a LLM output schema |
| GGS computes loss and gradient; R2 does not self-direct replanning | R7 owns PlanDirective |
| R2 plan cannot reuse a tool in blocked_tools from PlanDirective | R2 plan validator |
| R2 plan cannot reuse an approach flagged in memory MUST NOT | R2 plan validator |
| Memory calibration (Steps 1–3) involves no LLM call; bounded at 10 entries | R2 Go code |
| GGS emits `abandon` when Ω ≥ 0.8 regardless of gradient signal | R7 decision table |
| GGS is the sole emitter of `FinalResult` on all paths (accept and abandon); R4b never emits it directly | R7 code; R4b sends OutcomeSummary/ReplanRequest |
| `FinalResult.Loss.D == 0.0` iff accept path; `> 0.0` iff abandon path | R7 `processAccept` sets D=0; `process()` abandon uses `computeD(outcomes) > 0` |

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
| time_budget_ms | 300000 | Default time budget per task (5 min); raised from 120 s after issue #46 (real-world LLM latency exceeds 2 min budget) |

These are initial values. They should be tuned empirically once GGS is deployed.
The Auditor's gap_trend data across sessions provides the signal for tuning.

---

## Open Questions for v0.7 Implementation

| # | Question | Blocks |
|---|---|---|
| Q2 | Should GGS persist L_prev across sessions (in R5) or only within a single task's lifetime? Cross-session persistence enables better gradient estimation for recurring task types | R7, R5 |
| Q3 | How should `change_path` directive communicate the *new* path hint to R2 — as free text in rationale, or as a structured `suggested_alternatives` field? | R7, R2 |
| Q4 | When multiple subtasks fail with different failure_classes, how does GGS pick the dominant class for the directive? Proposed: majority vote; tie → "mixed" class → `change_approach` | R7 |
| Q5 | ~~Should the `abandon` directive from GGS be distinguishable from the `maxReplans` abandon in R4b?~~ **RESOLVED**: GGS is the sole emitter of `FinalResult` on all paths. R4b sends `ReplanRequest` (with `recommendation: abandon`) to R7; GGS decides based on Ω. Consistent abandonment path achieved. | — |
| Q6 | How does R2's plan validator check `blocked_tools` — exact string match on tool name, or keyword match on intent? Proposed: exact match on tool name; R3's tool_calls format already carries the tool name prefix | R2, R7 |

---

## Accountability Map

| Failure | Accountable Role |
|---|---|
| User's original intent not preserved faithfully; raw_input modified or clarification over-asked | R1 Perceiver |
| Fuzzy intent mis-interpreted; task_criteria do not reflect what the user actually wanted | R2 Planner |
| Goal not achieved despite correct execution; prior failures or GGS directives ignored | R2 Planner |
| On abandon: bare failure message with no partial result or next-move suggestions | R2 Planner |
| Feasible sub-task not correctly executed | R3 Executor |
| Gap between sub-task output and goal goes unresolved or unreported | R4a Agent-Validator |
| Failed subtask accepted as success; merged result fails task_criteria | R4b Meta-Validator |
| task_criteria technically satisfied but user's intent (raw_input) not actually met | R4b Meta-Validator |
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
