# MVP Role Definitions

**Version**: 0.4
**Status**: Draft
**Date**: 2026-02-19
**Scope**: Minimum viable organization — six roles. Goal Gradient Solver and Dreamer deferred to v0.5.

## Changelog from v0.3

| Change | Reason |
|---|---|
| Each role now declares its position in the nested feedback loop (sensor / controller / actuator / plant) | The architecture established that the same decision→execution→correction→execution pattern runs at every level; roles must know which part they play |
| R4a produces `gap_trajectory` in `SubTaskOutcome` | The Goal Gradient Solver's dynamic (history-dependent) property requires gap trajectory data; instrumenting it now means GGS slots in without schema changes |
| `ReplanRequest` gains `correction_count` and `gap_trend` | R4b can compute these from the trajectory it receives; GGS will consume them when added — forward-compatible instrumentation |
| R2 MVP note made explicit: it acts as both actuator and simplified controller | Without GGS, R2 applies naive replanning directly from ReplanRequest; this is a known MVP limitation, not the intended steady-state design |
| R4b description sharpened: it is the sensor of the medium loop, not the controller | The controller role belongs to GGS; in MVP R4b fills in by passing the error signal directly to R2 |
| Deferred GGS description rewritten as dynamic-differential controller | Backprop analogy retired; correct framing is control theory |

---

## Feedback Loop Structure

The framework is built on one pattern instantiated at two scales in MVP:

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
```

In steady state (post-MVP), GGS sits between R4b (sensor) and R2 (actuator) in the medium loop. In MVP, R2 absorbs the controller role directly from R4b's error signal, producing naive replanning instead of directed gradient correction.

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

---

## R1 — Perceiver

**Mission**: Translate raw user input into a structured, unambiguous task specification. If the downstream system acts on a wrong or underspecified goal, this role is accountable — not the Planner for planning poorly, not the Executor for executing the wrong thing.

**Loop position**: Reference signal. The `TaskSpec` it produces is the target that all feedback loops are driving toward. Its quality is the ceiling for every correction the system can make.

**Skills**:
- Parse natural language into structured intent
- Identify ambiguities and ask clarifying questions when the answer would materially change the plan; infer what can be reasonably inferred from context; batch multiple ambiguities into a single turn rather than sequential rounds
- Extract measurable success criteria precise enough to be scored by validators
- Identify scope constraints (file paths, time bounds, domains)
- Produce a validated `TaskSpec` JSON

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | User | Free-text natural language |
| Produces | Planner (R2) | `TaskSpec` JSON |
| Produces | User | Clarifying questions (only when answers would materially change the plan; batched into one turn when possible) |

```json
TaskSpec {
  "task_id":          "string",
  "intent":           "string",
  "success_criteria": ["string"],   // must be scoreable, not vague
  "constraints": {
    "scope":    "string | null",
    "deadline": "ISO8601 | null"
  },
  "raw_input": "string"
}
```

**Does NOT**:
- Decompose the task into sub-tasks (R2)
- Evaluate whether a result satisfies the user (R4b)
- Access memory or prior history (R5)
- Make any decision about how the task will be executed

---

## R2 — Planner

**Mission**: Own the path from task specification to final result. If the overall goal is not achieved despite Executors performing correctly — because decomposition was wrong, sequencing was wrong, or corrections were not applied — this role is accountable.

**Loop position**: Actuator of the medium loop. It receives corrections and re-dispatches sub-tasks accordingly. In MVP (no Goal Gradient Solver), it also absorbs the controller role: it interprets the `ReplanRequest` error signal directly and decides what to change. This is a known MVP limitation — naive replanning rather than directed gradient correction.

**Skills**:
- Query Shared Memory (R5) for relevant prior experience before planning
- Decompose a `TaskSpec` into an ordered or parallel set of atomic `SubTask` objects, each self-contained and requiring no peer-agent coordination
- Dispatch sub-tasks to Executors (R3) with a dispatch manifest (list of expected subtask IDs sent to R4b)
- Receive `ReplanRequest` from Meta-Validator (R4b) and apply corrections — in MVP this is naive replanning; in steady state this will be GGS-directed adjustment
- Recognize when all sub-tasks are accepted by R4b and deliver the final result to the user

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Perceiver (R1) | `TaskSpec` JSON |
| Receives | Meta-Validator (R4b) | `ReplanRequest` JSON |
| Receives | Shared Memory (R5) | `MemoryEntry[]` |
| Produces | Executor (R3) | `SubTask` JSON |
| Produces | Meta-Validator (R4b) | `DispatchManifest` JSON |
| Produces | Shared Memory (R5) | Read query |
| Produces | User (via Perceiver) | Final result (text) |

```json
SubTask {
  "subtask_id":       "string",
  "parent_task_id":   "string",
  "intent":           "string",
  "success_criteria": ["string"],
  "context":          "string",
  "deadline":         "ISO8601 | null",
  "sequence":         "integer"       // same value = can run in parallel
}

DispatchManifest {
  "task_id":      "string",
  "subtask_ids":  ["string"],         // R4b uses this to know when fan-in is complete
  "dispatched_at":"ISO8601"
}
```

**Does NOT**:
- Execute any action directly (R3)
- Evaluate output quality at sub-task or full-task level (R4a, R4b)
- Merge or assess parallel results (R4b)
- Compute the direction of correction — in MVP it does this itself as a limitation; in steady state this belongs to GGS
- Consolidate or reorganize memory (deferred — Dreamer)

---

## R3 — Executor

**Mission**: Execute exactly one assigned sub-task and return a concrete, verifiable result. If a sub-task is feasible as specified and the result is wrong or missing, this role is accountable. If the sub-task is infeasible as specified, reporting that honestly is also this role's responsibility.

**Loop position**: Plant of both loops. It is acted upon by the fast loop (Agent-Validator drives retries) and is re-instantiated by the medium loop (Planner re-dispatches after correction). It has no awareness of either loop — it only receives a sub-task and produces a result.

**Skills**:
- Interpret a `SubTask` and determine the required tool call sequence
- Use available tools: shell commands, file I/O, web search, API calls, code execution
- Produce a result evaluable against the sub-task's success criteria
- Detect and report infeasibility with a specific reason

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Planner (R2) | `SubTask` JSON |
| Receives | Agent-Validator (R4a) | `CorrectionSignal` (targeted feedback on what to improve) |
| Produces | Agent-Validator (R4a) | `ExecutionResult` JSON |

```json
ExecutionResult {
  "subtask_id":  "string",
  "status":      "completed | uncertain | failed",
  "output":      "any",
  "uncertainty": "string | null",
  "tool_calls":  ["string"]
}
```

**Does NOT**:
- Evaluate whether its output meets the criteria (R4a)
- Decide to retry itself — the Agent-Validator owns the retry and correction decisions
- Communicate with other Executors
- Report results to anyone other than R4a
- Write to Shared Memory

---

## R4a — Agent-Validator

**Mission**: Close the gap between the Executor's output and the sub-task goal. The fast feedback loop is entirely internal to the effector agent — the metaagent sees neither raw execution results nor correction cycles. When the gap is closed, report the matched outcome upward. When it cannot be closed, report the failure upward. If a bad result is silently accepted as matched, this role is accountable.

**Loop position**: Sensor and controller of the fast loop. As sensor: it measures the gap between execution output and sub-task criteria. As controller: it computes a directed correction signal — not "try again" but "here is specifically what was wrong and what to do differently" — and re-invokes the Executor. It records the gap score at each cycle, producing a trajectory that is included in the `SubTaskOutcome` for the Goal Gradient Solver's eventual use.

**Skills**:
- Score `ExecutionResult` against each criterion in the `SubTask`
- Compute a `CorrectionSignal`: specific, targeted feedback identifying what was wrong and how to improve — not a generic retry prompt
- Re-invoke the Executor with the `CorrectionSignal` when gap is non-zero and retry budget remains
- Track gap scores across retry cycles (the trajectory)
- Determine when gap is closed and emit `SubTaskOutcome { status: matched }` upward
- Determine when gap cannot be closed and emit `SubTaskOutcome { status: failed }` upward

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
  "what_was_wrong":    "string",     // specific failure description
  "what_to_do":        "string"      // specific instruction for next attempt
}

SubTaskOutcome {
  "subtask_id":      "string",
  "parent_task_id":  "string",
  "status":          "matched | failed",
  "output":          "any",
  "failure_reason":  "string | null",
  "gap_trajectory":  [               // gap score at each attempt — feeds GGS dynamic property
    { "attempt": "integer", "score": "float", "unmet_criteria": ["string"] }
  ]
}
```

**Does NOT**:
- Decide what the next sub-task should be (R2)
- Execute any action (R3)
- Assess whether the overall task goal is satisfied (R4b)
- Merge results from multiple agents (R4b)
- Write to Shared Memory

---

## R4b — Meta-Validator

**Mission**: Collect all `SubTaskOutcome` objects from all parallel subordinate agents, merge them into a unified result, and verify whether the merged result falls within a plausible range of the user's original intent. If an out-of-range result is accepted and delivered to the user, or if a task is silently abandoned, this role is accountable.

**Loop position**: Sensor of the medium loop. It measures the gap between the merged result and the user's original intent, and produces an error signal (`ReplanRequest`) for the controller. In steady state the controller is the Goal Gradient Solver; in MVP it is the Planner acting in a simplified capacity. R4b's role does not change between MVP and steady state — it is always the sensor. It also computes `gap_trend` from correction history, making the error signal richer without requiring GGS to be present.

**Skills**:
- Receive the `DispatchManifest` from R2 and use it to know when all parallel sub-tasks have reported in (fan-in completeness)
- Merge parallel `SubTaskOutcome` objects into a single coherent result
- Assess the merged result against the `TaskSpec` criteria within a plausible range (partial satisfaction with acceptable gap is valid — not binary pass/fail)
- Compute `gap_trend` from prior `ReplanRequest` correction count and current gap
- Trigger replanning by sending a `ReplanRequest` to R2 when outside plausible range
- Accept the merged result and write to Shared Memory when within plausible range
- Deliver the final result to the user

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Agent-Validator (R4a) | `SubTaskOutcome` JSON (one per sub-task in manifest) |
| Receives | Planner (R2) | `TaskSpec` JSON + `DispatchManifest` JSON |
| Produces | Planner (R2) | `ReplanRequest` JSON (when outside plausible range) |
| Produces | Shared Memory (R5) | `MemoryEntry` JSON (on acceptance) |
| Produces | User | Final merged result (text) |

```json
ReplanRequest {
  "task_id":          "string",
  "merged_result":    "any",
  "gap_summary":      "string",          // what is missing relative to user intent
  "failed_subtasks":  ["string"],
  "correction_count": "integer",         // how many replanning cycles so far
  "gap_trend":        "improving | stable | worsening",  // computed from trajectory
  "recommendation":   "replan | partial_replan | abandon"
}

MemoryEntry {
  "entry_id":     "string",
  "task_id":      "string",
  "type":         "episodic",
  "content":      "any",
  "criteria_met": ["string"],
  "timestamp":    "ISO8601",
  "tags":         ["string"]
}
```

**Does NOT**:
- Evaluate individual sub-task output against local criteria (R4a)
- Execute any action (R3)
- Decompose or assign sub-tasks (R2)
- Compute the correction direction — it produces the error signal; the controller (GGS or MVP-Planner) computes what to do with it
- Consolidate or cross-link memory entries (deferred — Dreamer)

---

## R5 — Shared Memory

**Mission**: Persist task history and accepted results, and serve them reliably on demand. If valid data is lost, corrupted, returned in wrong order, or silently missing from a query result, this role is accountable.

**Loop position**: State store. Shared Memory is not part of the feedback loop itself — it is the persistent substrate that all loop participants read from (prior experience) and write to (accepted outcomes). It makes the feedback loops accumulate learning across tasks rather than resetting on each task.

**Skills**:
- Store `MemoryEntry` objects keyed by entry ID
- Retrieve entries by task ID, tags, or semantic similarity query
- Return results ranked by relevance
- Enforce write permission: only Meta-Validator (R4b) may write in v0.4

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives (write) | Meta-Validator (R4b) | `MemoryEntry` JSON |
| Receives (read) | Planner (R2) | Query (task ID, tags, or natural language) |
| Produces | Planner (R2) | `MemoryEntry[]` ranked by relevance |

**Does NOT**:
- Reorganize, summarize, or cross-link entries (deferred — Dreamer)
- Evaluate or judge content quality
- Accept writes from R1, R2, R3, or R4a

---

## Interaction Diagram (v0.4)

```
                          [MEDIUM LOOP - MVP simplified]
 ┌────────────────────────────────────────────────────────────────────────┐
 │                                                                        │
User                                                                      │
 │ free text                                                              │
 ▼                                                                        │
[R1 Perceiver]──TaskSpec──►[R2 Planner]◄──MemoryEntry[]──[R5 Shared Mem] │
                                │  ▲                                      │
                  SubTask[]     │  │ ReplanRequest                        │
                  + Manifest    │  │ (error signal)                       │
                  (fan-out)     │  │                                      │
              ┌─────────────────┘  │                                      │
              │                    │                                      │
    ┌─────────┼──────────┐         │                                      │
    │         │          │         │                                      │
    ▼         ▼          ▼         │       [FAST LOOP × N]                │
  [R3]      [R3]       [R3]        │  ┌──────────────────────┐            │
Executor  Executor   Executor      │  │ R3 Executor          │            │
    │         │          │         │  │   ↕ CorrectionSignal │            │
    ▼         ▼          ▼         │  │ R4a Agent-Validator  │            │
  [R4a]     [R4a]      [R4a]       │  └──────────────────────┘            │
    │         │          │         │                                      │
    └─────────┼──────────┘         │                                      │
    SubTaskOutcome[]  (fan-in)     │                                      │
              │                    │                                      │
              ▼                    │                                      │
       [R4b Meta-Validator]────────┘                                      │
       (sensor: measures gap,                                             │
        computes gap_trend)                                               │
              │                                                           │
     (on acceptance)                                                      │
              ├──────────────────────────────────────────────────────────┘
              │
    ┌─────────┴──────────┐
    ▼                    ▼
[R5 Shared Memory]     User
```

---

## Accountability Map

| Failure | Accountable Role |
|---|---|
| System acts on wrong or underspecified goal | R1 Perceiver |
| Goal not achieved despite correct execution | R2 Planner |
| Feasible sub-task not correctly executed | R3 Executor |
| Gap between sub-task output and goal goes unresolved or unreported | R4a Agent-Validator |
| Merged result accepted outside plausible range, or task silently abandoned | R4b Meta-Validator |
| Data lost, corrupted, or wrongly retrieved | R5 Shared Memory |

---

## Deferred to v0.5

| Component | Design specification needed before implementation |
|---|---|
| Goal Gradient Solver | A dynamic-differential controller sitting between R4b (sensor) and R2 (actuator) in the medium loop. Inputs: `ReplanRequest` (current gap + `gap_trend` + `correction_count`) and gap trajectory history from `SubTaskOutcome[]`. Outputs: a directed plan adjustment to R2 specifying which decisions to change and in what direction — not a full replan. The dynamic property requires it to use trajectory (not just current gap) to determine whether the system is converging and scale corrections accordingly. Schemas are already forward-compatible: no changes to R4a, R4b, or R2 contracts are needed to add GGS |
| Dreamer (agent-level) | Async memory consolidation within each effector after sub-task completion. Reads episodic execution events; produces a structured summary written to Shared Memory |
| Dreamer (metaagent-level) | Async cross-task consolidation. Reads agent-level episodic summaries; produces semantic entries capturing patterns across tasks |

---

## Open Questions for v0.4 Implementation

| # | Question | Blocks |
|---|---|---|
| Q1 | What defines the retry budget per sub-task — fixed count, time-based, or gap-score threshold? | R4a |
| Q2 | How is "plausible range" operationalized — LLM-scored rubric, threshold on criteria scores, or explicit tolerance per criterion in TaskSpec? | R4b |
| Q3 | How does R2 determine what to change when it receives a ReplanRequest in MVP (no GGS)? Full replan, or heuristic partial replan? | R2 (MVP limitation) |
| Q4 | What tools are available to the Executor? | R3 |
| Q5 | What backing store for Shared Memory — SQLite, vector DB, flat JSON? | R5 |
| Q6 | How does the Perceiver determine whether an ambiguity is material — rule-based or LLM-judged? | R1 |
