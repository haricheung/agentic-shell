# MVP Role Definitions

**Version**: 0.8
**Status**: Draft
**Date**: 2026-02-25
**Scope**: Eight roles. GGS decision table refactored for completeness, orthogonality, and
Dreamer readiness. MKCT memory engine (Megram/K/C/T layers, dual-channel convolution,
LevelDB, Dreamer offline consolidation) fully specified. Implementation-ready.

[toc]

---

## Changelog from v0.7

| Change | Reason |
|---|---|
| GGS decision table refactored: 24 cells → 6 macro-states (abandon, success, break_symmetry, change_approach, change_path, refine) | v0.7 table used gradient direction as the primary split. This was wrong: ∇L sign conflated approach quality with trajectory noise. The new table uses a diagnostic cascade — Ω (constraint), D (target), then (∣∇L∣, P) for the action cells — producing cleaner, more orthogonal decisions |
| `success` macro-state added: D ≤ δ → accept regardless of P or ∇L | v0.7 required D = 0 (all criteria met) for acceptance. In practice, D ≤ δ means most criteria passed and the gap is within noise. Burning budget to close a δ-sized gap is wasteful. The convergence threshold makes the system pragmatic |
| ∇L sign demoted from state-determining to modulator | v0.7 treated improving ∇L as proof the approach is working. But improving loss with logically wrong approach (P > 0.5) is suspicious — hallucination, overfitting, criteria gaming. The new table treats ∇L sign as urgency modulation, not state selection |
| ∇L magnitude becomes the meaningful split: \|∇L\| ≥ ε (has signal) vs \|∇L\| < ε (plateau) | What matters is whether the system has directional information at all, not which direction. Having signal → the system can adapt (change_approach or refine). No signal → the system is stuck (break_symmetry or change_path) |
| P threshold parameterised as ρ (default 0.5) | Preparation for Dreamer tuning — ρ will be adjustable per task type based on historical failure patterns |
| `gradient` label removed from `PlanDirective` | v0.7 exposed "improving/stable/worsening/plateau" as a top-level label. This is no longer meaningful — the macro-state name carries the full decision. `grad_l` (raw ∇L value) is retained for observability |
| First-round behaviour simplified | v0.7 first round always landed in plateau (∇L = 0). Under the new table, first round with D > δ: if \|∇L\| < ε (always true on round 1) → split by P into break_symmetry or change_path. Consistent with the new priority — no special-casing needed |
| R5 Shared Memory fully redesigned: MKCT pyramid, Megram atomic tuples, dual-channel convolution potentials (M_attention / M_decision), LevelDB inverted index, Dreamer consolidation + GC engine | Keyword-scan JSON store cannot support cross-task SOP promotion, decay-weighted avoidance, or structured distinction between approach-level and path-level failures |
| GGS is now the sole writer to R5 — writes one Megram per blocked_target on action states; one macro-state Megram on terminal states (accept / success / abandon) | Metaval previously wrote MemoryEntry on accept/fail, bypassing GGS observability and conflating task semantics with memory semantics |
| R2 memory query now returns structured data (`[]SOPRecord`, `Potentials`) instead of formatted text; R2 formats constraints into the prompt itself | Memory-as-text-formatter made the memory layer untestable independently of R2 and violated the Data Service principle |

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
| R5 | Shared Memory | Infrastructure | MKCT memory engine; episodic-to-semantic consolidation via Dreamer | If valid data is lost, corrupted, or wrongly retrieved, this role is responsible |
| R6 | Auditor | Infrastructure | Lateral observer | If systematic failures go undetected and unreported to the human operator, this role is responsible |
| R7 | Goal Gradient Solver | Metaagent | Controller (medium loop) | If the replanning direction is wrong, too conservative, or too aggressive for the observed gradient, this role is responsible |

---

## R1 — Perceiver

*(Unchanged from v0.7.)*

**Mission**: Receive the user's signal and carry it into the system with full fidelity.
R1 is a receiver, not a resolver.

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

*(Unchanged from v0.7 except: receives the new `PlanDirective` schema without `gradient` label.)*

**Mission**: Interpret the user's intent and own the path to its realisation.

**Changes from v0.7**:
- `PlanDirective.gradient` label removed — R2 reads the `directive` field directly
- `PlanDirective.directive` now includes `success` (GGS may accept with D ≤ δ before reaching R2 — R2 is not invoked on success path)

**Memory Calibration Protocol** (MKCT-based):

**Tag derivation** (R2 responsibility before querying):
- Space: `"intent:<first-3-words-of-intent-lowercased-underscored>"` — e.g. `intent:db_migration_task`
- Entity: `"env:local"` (default for all local-machine tasks)

**Query sequence**:
1. `QueryC(space, entity)` → `[]SOPRecord` — C-level SOPs/Constraints for this intent type
2. `QueryMK(space, entity)` → `Potentials{Attention, Decision, Action}` — live convolution result

**Action mapping**:

| Action | Prompt effect |
|---|---|
| Exploit | SHOULD PREFER this approach |
| Avoid | MUST NOT use this approach |
| Caution | Proceed with confirmation gate / sandboxing |
| Ignore | Omit from prompt |

**Merged MUST NOT set** (unchanged priority order):
`memory Avoid SOPs` ∪ `GGS blocked_tools` ∪ `GGS blocked_targets`

**Contract**: Unchanged from v0.7.

---

## R3 — Executor

*(Unchanged from v0.7.)*

---

## R4a — Agent-Validator

*(Unchanged from v0.7.)*

---

## R4b — Meta-Validator

*(Unchanged from v0.7, except as noted below.)*

**Does NOT** (addendum to v0.7):
- Write to Shared Memory — GGS is the sole writer to R5 on all paths.

*Clarifying note*: `processAccept()` publishes `OutcomeSummary` to GGS as before; GGS
then writes the `accept` Megram. R4b does not write any MemoryEntry.

---

## R7 — Goal Gradient Solver (GGS)

**Mission**: Translate R4b's raw failure signal into a directed planning constraint for R2.
If the replanning direction is wrong — too conservative when convergence is possible, too
aggressive when refinement would suffice, or failing to escape a local minimum — this role
is accountable.

**Loop position**: Controller of the medium loop. Sits between R4b (sensor) and R2 (actuator).

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

Default weights: w₁ = 0.6, w₂ = 0.4.

### Gradient Computation

The gradient ∇L is approximated by finite difference across consecutive replan rounds:

```
∇L_t = L_t − L_{t−1}
```

GGS maintains `L_prev` in memory across rounds for the same task_id.
First round: `L_prev` undefined → `∇L = 0`.

### Macro-State Decision Table (v0.8)

The 24-cell input space (2P × 2Ω × 2D × 3∇L) collapses into **6 macro-states** via a
diagnostic cascade. The cascade evaluates variables in order of strategic value:

```
Priority 1: Ω  — hard constraint (can we continue?)
Priority 2: D  — target distance (are we close enough?)
Priority 3: |∇L| and P together — action selection (what kind of change?)
```

∇L *sign* (improving vs worsening) is demoted to a modulator — it affects urgency within
a macro-state but does not determine which macro-state the system is in.

#### The 6 macro-states

| # | Condition | Macro-state | Cells | Action |
|---|---|---|---|---|
| 1 | Ω ≥ θ | **abandon** | 12 | Budget exhausted — stop and deliver failure summary |
| 2 | Ω < θ, D ≤ δ | **success** | 6 | Close enough — stop and deliver result |
| 3 | Ω < θ, D > δ, \|∇L\| < ε, P > ρ | **break_symmetry** | 1 | Stuck + wrong approach — demand novel tool class |
| 4 | Ω < θ, D > δ, \|∇L\| ≥ ε, P > ρ | **change_approach** | 2 | Has signal + wrong approach — switch method |
| 5 | Ω < θ, D > δ, \|∇L\| < ε, P ≤ ρ | **change_path** | 1 | Stuck + right approach — different target |
| 6 | Ω < θ, D > δ, \|∇L\| ≥ ε, P ≤ ρ | **refine** | 2 | Has signal + right approach — tighten parameters |

Total: 12 + 6 + 1 + 2 + 1 + 2 = **24 cells**. Complete and non-overlapping.

#### Visual: the action grid (Ω < θ, D > δ)

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

#### Full 24-cell enumeration

| # | ∇L | D | P | Ω | Macro-state |
|---|---|---|---|---|---|
| 1 | < -ε | ≤ δ | ≤ ρ | < θ | success |
| 2 | < -ε | ≤ δ | > ρ | < θ | success |
| 3 | < -ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 4 | < -ε | ≤ δ | > ρ | ≥ θ | abandon |
| 5 | < -ε | > δ | ≤ ρ | < θ | refine |
| 6 | < -ε | > δ | > ρ | < θ | change_approach |
| 7 | < -ε | > δ | ≤ ρ | ≥ θ | abandon |
| 8 | < -ε | > δ | > ρ | ≥ θ | abandon |
| 9 | \|·\|<ε | ≤ δ | ≤ ρ | < θ | success |
| 10 | \|·\|<ε | ≤ δ | > ρ | < θ | success |
| 11 | \|·\|<ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 12 | \|·\|<ε | ≤ δ | > ρ | ≥ θ | abandon |
| 13 | \|·\|<ε | > δ | ≤ ρ | < θ | change_path |
| 14 | \|·\|<ε | > δ | > ρ | < θ | break_symmetry |
| 15 | \|·\|<ε | > δ | ≤ ρ | ≥ θ | abandon |
| 16 | \|·\|<ε | > δ | > ρ | ≥ θ | abandon |
| 17 | > ε | ≤ δ | ≤ ρ | < θ | success |
| 18 | > ε | ≤ δ | > ρ | < θ | success |
| 19 | > ε | ≤ δ | ≤ ρ | ≥ θ | abandon |
| 20 | > ε | ≤ δ | > ρ | ≥ θ | abandon |
| 21 | > ε | > δ | ≤ ρ | < θ | refine |
| 22 | > ε | > δ | > ρ | < θ | change_approach |
| 23 | > ε | > δ | ≤ ρ | ≥ θ | abandon |
| 24 | > ε | > δ | > ρ | ≥ θ | abandon |

#### Case 3.3 — the subtle case (cell #6)

∇L < -ε (improving), D > δ, P > ρ → **change_approach**.

Loss is decreasing but the approach is logically wrong. This is suspicious: the system may
be hallucinating success, overfitting to criteria, or accidentally converging in a wrong
basin. v0.7 blindly trusted the improving trend and issued `refine` — wrong. The correct
response is to distrust the trend and change the approach.

This case is where a future Dreamer would add the most value: recognising that an
improving loss with a fundamentally wrong approach signals a deeper problem (criteria
gaming, evaluation bias) that GGS cannot diagnose alone.

### Directive Semantics (v0.8)

**`abandon`** — Ω ≥ θ. Budget exhausted regardless of all other signals. GGS delivers
`FinalResult` with failure summary directly; R2 is not invoked.
- `blocked_tools`: none
- `blocked_targets`: none

**`success`** — Ω < θ, D ≤ δ. Result is within convergence threshold. GGS delivers
`FinalResult` with merged output; R2 is not invoked.
- This is new in v0.8: v0.7 required D = 0 for acceptance.
- `success` in the replan path means "good enough" — the gap is smaller than δ and not
  worth another replan round.

**`break_symmetry`** — Ω < θ, D > δ, |∇L| < ε, P > ρ. The approach is logically wrong
AND the system is stuck (no loss movement). Must escape the local minimum with a
completely different tool class.
- `blocked_tools`: all tools used in failing subtasks
- `blocked_targets`: none (switching tools makes prior targets irrelevant)

**`change_approach`** — Ω < θ, D > δ, |∇L| ≥ ε, P > ρ. The approach is logically wrong
BUT there is directional signal (loss is moving, either direction). Must switch to a
different method. Less drastic than break_symmetry because the system isn't stuck — it
has information to work with.
- `blocked_tools`: tools from failing subtasks
- `blocked_targets`: none

**`change_path`** — Ω < θ, D > δ, |∇L| < ε, P ≤ ρ. The approach is sound but the system
is stuck — same tools keep hitting the same environmental wall. Same tool class, different
target or parameters.
- `blocked_tools`: none (the tool class is correct)
- `blocked_targets`: accumulated failed query strings / commands / paths

**`refine`** — Ω < θ, D > δ, |∇L| ≥ ε, P ≤ ρ. The approach is sound and there is
directional signal. Whether improving or worsening, the system has information — tighten
parameters, narrow scope, adjust search terms.
- `blocked_tools`: none
- `blocked_targets`: accumulated failed query strings / commands / paths

### ∇L Sign as Urgency Modulator

Within each macro-state, the sign of ∇L affects execution urgency but not the directive:

| ∇L sign | Modulation |
|---|---|
| < -ε (improving) | Lower urgency — current trajectory is helping |
| > ε (worsening) | Higher urgency — actively diverging, apply directive aggressively |

Concrete modulations (implementation guidance, not spec-mandated):
- `change_approach` + worsening → block more tools, constrain R2 more tightly
- `change_approach` + improving → block fewer tools, allow R2 more latitude
- `refine` + worsening → expand `blocked_targets` scope
- `refine` + improving → narrow `blocked_targets` to the most recent failure only

### Law 2 Kill-Switch (retained from v0.7)

2 consecutive worsening replan rounds → force **abandon** regardless of Ω.

This is an additional safety net beyond the Ω threshold. It fires when the system is
actively diverging and no amount of budget will help. The worseningCount resets when
∇L leaves the worsening range.

### Dynamic MUST NOT Injection

Unchanged from v0.7.

**`blocked_tools`** (logical failures — `break_symmetry`, `change_approach`):
Tool *names* from failing subtasks. R2 must not plan using those tools.

**`blocked_targets`** (environmental failures — `change_path`, `refine`):
Specific *inputs* that failed. Accumulates across all replan rounds per task.

Combined MUST NOT set = memory MUST NOTs ∪ `blocked_tools` ∪ `blocked_targets`.

### Skills

- Receive `ReplanRequest` from R4b carrying full `SubTaskOutcome[]` data
- Receive `OutcomeSummary` from R4b when all subtasks matched (happy path)
- Compute D, P, Ω, L for the current round
- Compute ∇L from previous round's L (maintained per task_id)
- Select macro-state from decision table
- On **success** (D ≤ δ in replan path): emit `FinalResult` to User with merged output
- On **abandon** (Ω ≥ θ): emit `FinalResult` to User with failure summary
- On **action** states: emit `PlanDirective` to R2
- Populate `FinalResult.Loss`, `FinalResult.GradL`, `FinalResult.Replans` on every emission
- For action states (change_path, refine, change_approach, break_symmetry): write one
  Megram per entry in `blocked_targets` to R5; tags = `(tool:<name>, path:<target>)` parsed
  from the blocked target string
- For terminal states (accept, success, abandon): write one Megram to R5 with tags
  `(intent:<task-intent-slug>, env:local)`
- All writes are fire-and-forget (non-blocking channel send to R5 write goroutine)

### Contract

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Meta-Validator (R4b) | `ReplanRequest` JSON (with full outcomes data) |
| Receives | Meta-Validator (R4b) | `OutcomeSummary` JSON (all subtasks matched) |
| Produces | Planner (R2) | `PlanDirective` JSON (action macro-states only) |
| Produces | User (via bus) | `FinalResult` JSON (success, accept, and abandon paths) |

```json
PlanDirective {
  "task_id":          "string",
  "loss": {
    "D":     "float",
    "P":     "float",
    "Omega": "float",
    "L":     "float"
  },
  "prev_directive":    "string",
  "directive":         "refine | change_path | change_approach | break_symmetry",
  "blocked_tools":     ["string"],
  "blocked_targets":   ["string"],
  "failed_criterion":  "string",
  "failure_class":     "logical | environmental | mixed",
  "budget_pressure":   "float",
  "grad_l":            "float",
  "rationale":         "string"
}

FinalResult {
  "task_id":        "string",
  "summary":        "string",
  "output":         "any",
  "loss": {
    "D":     "float",
    "P":     "float",
    "Omega": "float",
    "L":     "float"
  },
  "grad_l":         "float",
  "replans":        "integer",
  "prev_directive": "string",
  "directive":      "accept | success | abandon"
}
```

**State-transfer display**: GGS tracks `prevDirective` per task. Every `PlanDirective`
and `FinalResult` carries both the previous and current macro-state, enabling the UI to
render `prev→current` transitions (e.g. `init→change_path`, `refine→success`). The first
round for any task always shows `init` as the previous state.

**Does NOT**:
- Generate sub-tasks or modify the plan directly (R2)
- Observe individual tool calls (that is R4a's domain)
- Merge or verify outputs (R4b)
- Override the fan-in gate (R4b owns that)

---

## R5 — Shared Memory

### Mission

Serve as the system's durable cognitive substrate. R5 accumulates experience as
Megrams, promotes recurring patterns into cross-task SOPs (C-level), and decays stale
knowledge — without ever blocking the operational hot path.

### 6a. The MKCT Pyramid

| Layer | Name | Decay k | Description |
|---|---|---|---|
| M | Megram | per Quantization Matrix | Raw episodic fact; default layer on creation |
| K | Knowledge | same as M | Task-scoped cache; pruned by Dreamer GC |
| C | Common Sense | 0.0 (timeless) | Promoted SOP or Constraint; LLM-distilled from M clusters |
| T | Thinking | 0.0 (timeless) | System persona / values; hardcoded in system prompt for MVP; slow-evolution mechanism deferred to Phase 2 |

### 6b. Megram Base Tuple

```
Megram = ⟨ID, Level, created_at, last_recalled_at, space, entity, content, state, f, sigma, k⟩
```

Tag conventions:
- *Micro-event* (action states): `space="tool:<name>"`, `entity="path:<target>"` — one Megram per blocked_target
- *Macro-event* (terminal states): `space="intent:<intent-slug>"`, `entity="env:local"` — one Megram per routing decision

### 6c. GGS Quantization Matrix

| State | f | σ | k | Physical meaning |
|---|---|---|---|---|
| `abandon` | 0.95 | -1.0 | 0.05 | PTSD trauma; hard constraint |
| `accept` (D=0) | 0.90 | +1.0 | 0.05 | Flawless golden path |
| `change_approach` | 0.85 | -1.0 | 0.05 | Anti-pattern; blacklist tool class |
| `success` (D≤δ) | 0.80 | +1.0 | 0.05 | Best practice SOP |
| `break_symmetry` | 0.75 | +1.0 | 0.05 | Breakthrough; favour retry |
| `change_path` | 0.30 | 0.0 | 0.2 | Dead end; tool unharmed; path avoided via GGS blocked_targets |
| `refine` | 0.10 | +0.5 | 0.5 | Muscle memory; fast GC |

Decay constants: k=0.05 → ~14-day half-life; k=0.2 → ~3.5-day; k=0.5 → ~1.4-day.
C/T-level entries have k=0.0 (timeless until Trust Bankruptcy).

### 6d. Dual-Channel Convolution Potentials

```
M_attention(space, entity) = Σ |f_i| · exp(−k_i · Δt_days)
M_decision(space, entity)  = Σ  σ_i · f_i · exp(−k_i · Δt_days)
```

Derived action:

| Condition | Action |
|---|---|
| M_att < 0.5 | Ignore |
| M_att ≥ 0.5 AND M_dec > 0.2 | Exploit |
| M_att ≥ 0.5 AND M_dec < -0.2 | Avoid |
| M_att ≥ 0.5 AND \|M_dec\| ≤ 0.2 | Caution |

### 6e. Dreamer — Offline Consolidation Engine

Runs as a background goroutine. Triggered: (a) after each task's FinalResult + a brief
settle delay, and (b) optionally on a timer. Never blocks the operational path.

**Upward flow (Consolidation — Λ_rule thresholds)**
- Scan Megrams with identical `(space, entity)` tag pair
- `M_attention ≥ 5.0 AND M_decision ≥ 3.0` → invoke LLM to distil Best Practice → new Megram(Level=C, k=0.0)
- `M_attention ≥ 5.0 AND M_decision ≤ -3.0` → invoke LLM to distil Constraint → new Megram(Level=C, k=0.0)
- Raw M-level Megrams that were consolidated may be GC'd (optional; for space reclamation)

**Downward flow (Degradation + GC)**
- *Trust Bankruptcy* (Λ_demote): C-level entry where live `M_decision < 0.0` → strip time immunity (k reverts to 0.05; Megram demoted to K)
- *Physical Forgetting* (Λ_gc): M/K entry where live `M_attention < 0.1` → hard DELETE from LevelDB

### 6f. Storage: LevelDB (syndtr/goleveldb — pure Go, no CGO)

Key schema:
```
megram:<id>               → Megram JSON (primary record)
idx:<space>:<entity>:<id> → ""          (inverted index for tag scan)
lvl:<level>:<id>          → ""          (level scan for Dreamer)
recall:<id>               → RFC3339     (last_recalled_at; updated on QueryC hits)
```

All Megram writes are **Append-Only**. Error correction appends a negative-σ Megram
rather than mutating existing records. `recall:` entries are the only mutable metadata.

### 6g. MemoryService Interface (Go)

```go
// MemoryService is the high-level interface used by all roles.
// Implementations: LevelDB (production), in-memory (tests).
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
| Receives writes | GGS (R7) only | `Megram` (via async write queue) |
| Serves C reads | Planner (R2) | `[]SOPRecord` |
| Serves M/K reads | Planner (R2) | `Potentials{Attention, Decision, Action}` |

**Does NOT**:
- Format prompt text (R2 owns that)
- Block the GGS hot path on writes
- Accept writes from any role other than GGS

---

## R6 — Auditor

*(Unchanged from v0.7. GGS thrashing detection now uses macro-state names.)*

**Updated detection**: consecutive `break_symmetry` without D decreasing → `ggs_thrashing` anomaly.

---

## Interaction Diagram (v0.8)

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

                          [MEDIUM LOOP - v0.8]

User
 │ free text
 ▼
[R1]──TaskSpec──►[R2 Planner]◄──────────────────────────── PlanDirective ──[R7 GGS]
                  │    ▲                                        ▲    │
      ┌───────────┤    └──── []SOPRecord, Potentials ◄── [R5] ──────┤
      │  calibrate│                                            │    │ Megram writes
      │  constrain│                                            │    │ (async, fire-and-forget)
      │  plan     │                                            │    ├──► FinalResult
      │           │                          [R4b]──ReplanReq──┘    │    (success/abandon
      │  SubTask[]│                            ▲                    │     → User)
      │           │                            │ SubTaskOutcome[]   │
      │           │                            │                    │
      └───────────┴──►[R3 × N]──►[R4a × N]────┘                    │
                                                                    │
                                  OutcomeSummary (all matched) ─────┘
                                  → GGS accept path (D=0)
```

---

## Key Invariants (enforced in code, not prompt)

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
| R2 plan cannot reuse an approach flagged in memory MUST NOT | R2 plan validator |
| GGS emits `abandon` when Ω ≥ θ regardless of other signals | R7 decision table |
| GGS emits `success` when Ω < θ and D ≤ δ regardless of P and ∇L | R7 decision table |
| `blocked_targets` accumulates across all replan rounds for the same task | R7 `triedTargets` map |
| GGS is the sole emitter of `FinalResult` on all paths (accept, success, abandon) | R7 code |
| `FinalResult.Directive` is always one of `accept`, `success`, `abandon` | R7 code |
| GGS is the sole writer to R5 Shared Memory | R7 code; R4b no longer writes MemoryEntry |
| One Megram per blocked_target for action states (not one per routing decision) | R7 write path |
| Megram writes are fire-and-forget; GGS never blocks on memory I/O | R5 async write queue |
| Memory returns structured data (`[]SOPRecord`, `Potentials`); R2 formats into prompt | R5 interface contract |
| C-level Megrams have k=0.0 (timeless) until Trust Bankruptcy | R5 Dreamer engine |
| Recall of a C-level SOP updates last_recalled_at (extends decay reset) | R5 QueryC implementation |
| `PlanDirective.PrevDirective` is `init` on first round; equals prior round's directive thereafter | R7 `prevDirective` map |
| Law 2 kill-switch: 2 consecutive worsening rounds → force abandon | R7 `worseningCount` |

---

## Loss Hyperparameters (v0.8 defaults)

| Parameter | Symbol | Default | Meaning |
|---|---|---|---|
| Distance weight | α | 0.6 | Weight on intent-result distance D |
| Process weight | β | 0.3 | Weight on process implausibility P (before adaptive scaling) |
| Resource weight | λ | 0.4 | Weight on resource cost Ω |
| Ω replan sub-weight | w₁ | 0.6 | Ω sub-weight for replan count |
| Ω time sub-weight | w₂ | 0.4 | Ω sub-weight for elapsed time |
| Plateau threshold | ε | 0.1 | \|∇L\| below this → plateau (no signal) |
| Convergence threshold | δ | 0.3 | D below this → success (close enough) |
| P threshold | ρ | 0.5 | P above this → logical failure; below → environmental |
| Abandon threshold | θ | 0.8 | Ω above this → abandon |
| Time budget | time_budget_ms | 300000 | 5 minutes per task |
| Max replans | maxReplans | 3 | Used in Ω computation |
| Law 2 kill threshold | — | 2 | Consecutive worsening rounds before forced abandon |

---

## Accountability Map

| Failure | Accountable Role |
|---|---|
| User's original intent not preserved faithfully | R1 Perceiver |
| Fuzzy intent mis-interpreted; task_criteria wrong | R2 Planner |
| Goal not achieved despite correct execution | R2 Planner |
| On abandon: bare failure message with no partial result | R2 Planner |
| Feasible sub-task not correctly executed | R3 Executor |
| Gap between sub-task output and goal goes unresolved | R4a Agent-Validator |
| Failed subtask accepted as success; merged result fails task_criteria | R4b Meta-Validator |
| Replanning direction wrong; local minimum not escaped; budget misjudged | R7 GGS |
| Data lost, corrupted, or wrongly retrieved | R5 Shared Memory |
| Systematic failures go undetected and unreported | R6 Auditor |

---

## Deferred to v0.9

| Component | Design specification needed before implementation |
|---|---|
| GGS hyperparameter tuning | Empirical calibration of α, β, λ, w₁, w₂, ε, δ, ρ, θ from Auditor session data |
| ∇L sign urgency modulation | Concrete implementation of per-macro-state urgency adjustment based on ∇L sign |
| Structured criteria mode | `{criterion, mode}` objects distinguishing `verifiable` from `plausible`; affects D computation weighting |
| R2 graceful failure on abandon | LLM-backed partial result + next-move suggestions (currently code-template only) |
| T-layer slow-evolution mechanism | Phase 2: allow system persona / values to update from high-confidence C-level consolidation |
| Dreamer Phase 2: Schema Transfer Engine | Semantic factorization on high-scoring Megrams; generation of Hypothesis Megrams to invent novel tool combinations |
| Dreamer Phase 2: Cognitive Dissonance Megrams | Dissonance Megrams (High f, Negative σ) generated on Soft Overwrite; shatter credibility of outdated SOPs during nightly Dreamer cycle |
