# MVP Role Definitions

**Version**: 0.3
**Status**: Draft
**Date**: 2026-02-19
**Scope**: Minimum viable organization — six roles sufficient to complete a full task cycle. Goal Gradient Solver, Dreamer, and Emergence are deferred to v0.4.

## Changelog from v0.2

| Change | Reason |
|---|---|
| R4a mission rewritten: retry is internal; output is a matched outcome reported upward, not a gap report | v0.2 incorrectly had R4a escalating gap reports to R4b. The retry loop is entirely local. R4a only speaks to the metaagent when the gap is closed (success) or irresolvable (failure) |
| R4b mission rewritten: primary job is to collect ALL parallel outcomes, merge them, then verify merged result against user intent within a plausible range | v0.2 treated R4b as a receiver of escalations only. Correct role: aggregate all agent outcomes (including successes), merge into a unified result, and assess holistic satisfaction |
| New data type: `SubTaskOutcome` — what R4a sends upward to R4b | Distinguishes the normal success path from a gap report |
| Plausible range concept formalized | Meta-Validator verifies approximate rather than binary goal satisfaction |
| R1 clarification constraint corrected: replaced "at most one question" with necessity-based constraint | "At most one" was arbitrary — it forces compound questions or unresolved ambiguity. The real constraint is ask only what is necessary and would materially change the plan |

---

## Role Definition Template

- **Mission** — the outcome this role owns. Includes explicit accountability: if this outcome fails, this role is responsible.
- **Skills** — what it is capable of doing
- **Contract** — what it receives and produces, including format and counterparty
- **Does NOT** — explicit boundary enforcing orthogonality

---

## Role Index

| ID | Role | Lives in | Mission Summary |
|---|---|---|---|
| R1 | Perceiver | Entry point | If the task is misunderstood, this role is responsible |
| R2 | Planner | Metaagent | If the goal is not achieved despite valid execution, this role is responsible |
| R3 | Executor | Effector Agent | If a feasible sub-task is not correctly executed, this role is responsible |
| R4a | Agent-Validator | Effector Agent | If a gap between outcome and sub-task goal goes unresolved or unreported, this role is responsible |
| R4b | Meta-Validator | Metaagent | If the merged result is accepted outside plausible range or a failed task is silently closed, this role is responsible |
| R5 | Shared Memory | Infrastructure | If valid data is lost, corrupted, or wrongly retrieved, this role is responsible |

---

## R1 — Perceiver

**Mission**: Translate raw user input into a structured, unambiguous task specification. If the downstream system acts on a wrong or underspecified goal, this role is accountable — not the Planner for planning poorly, not the Executor for executing the wrong thing.

**Skills**:
- Parse natural language into structured intent
- Identify ambiguities and ask clarifying questions when the answer would materially change the plan; infer what can be reasonably inferred from context; batch multiple ambiguities into a single turn rather than sequential rounds
- Extract measurable success criteria
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
  "success_criteria": ["string"],
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

**Mission**: Own the path from task specification to final result. If the overall goal is not achieved despite Executors performing correctly — because decomposition was wrong, sequencing was wrong, or replanning was missed — this role is accountable.

**Skills**:
- Query Shared Memory (R5) for relevant prior experience before planning
- Decompose a `TaskSpec` into an ordered or parallel set of atomic `SubTask` objects
- Ensure each sub-task is self-contained and executable without peer-agent coordination
- Dispatch sub-tasks to Executors (R3)
- Receive replanning triggers from Meta-Validator (R4b) and revise the plan
- Determine when the overall task is complete (R4b signals acceptance) and deliver the final result to the user

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Perceiver (R1) | `TaskSpec` JSON |
| Receives | Meta-Validator (R4b) | `ReplanRequest` JSON |
| Receives | Shared Memory (R5) | `MemoryEntry[]` |
| Produces | Executor (R3) | `SubTask` JSON |
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
  "sequence":         "integer"     // same value = can run in parallel
}
```

**Does NOT**:
- Execute any action directly (R3)
- Evaluate output quality at sub-task or full-task level (R4a, R4b)
- Merge or assess parallel results (R4b)
- Consolidate or reorganize memory (deferred — Dreamer, v0.4)

---

## R3 — Executor

**Mission**: Execute exactly one assigned sub-task and return a concrete, verifiable result. If a sub-task is feasible as specified and the result is wrong or missing, this role is accountable. If the sub-task is infeasible as specified, reporting that honestly is also this role's responsibility.

**Skills**:
- Interpret a `SubTask` and determine the required tool call sequence
- Use available tools: shell commands, file I/O, web search, API calls, code execution
- Produce a result evaluable against the sub-task's success criteria
- Detect and report infeasibility with a specific reason

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Planner (R2) | `SubTask` JSON |
| Receives | Agent-Validator (R4a) | Retry signal + targeted feedback |
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
- Retry itself — the Agent-Validator owns the retry decision
- Communicate with other Executors
- Report results directly to the Planner or Meta-Validator — all upward reporting goes through R4a
- Write to Shared Memory

---

## R4a — Agent-Validator

**Mission**: Close the gap between the Executor's output and the sub-task goal received from the superior (Planner). The retry loop is entirely internal to the effector agent. When the gap is closed, report the matched outcome upward to the Meta-Validator. If the gap cannot be closed, report the failure upward. If a bad result is silently accepted as matched, this role is accountable.

**Skills**:
- Score `ExecutionResult` against each criterion in the `SubTask`
- Re-invoke the Executor with specific, targeted feedback when the gap is non-zero and retry budget remains
- Determine when the gap is closed (criteria met) and emit a `SubTaskOutcome` upward
- Determine when the gap cannot be closed (retry budget exhausted or structurally infeasible) and emit a `SubTaskOutcome` with failure status upward

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Executor (R3) | `ExecutionResult` JSON |
| Receives | Planner (R2) | `SubTask` JSON (evaluation rubric, carried with dispatch) |
| Produces | Executor (R3) | Retry signal + targeted feedback (internal loop) |
| Produces | Meta-Validator (R4b) | `SubTaskOutcome` JSON |

```json
SubTaskOutcome {
  "subtask_id":      "string",
  "parent_task_id":  "string",
  "status":          "matched | failed",    // matched = gap closed; failed = gap irresolvable
  "output":          "any",                 // the accepted output (if matched)
  "failure_reason":  "string | null",       // populated only if status = failed
  "retry_count":     "integer",
  "criteria_scores": [
    { "criterion": "string", "met": "boolean", "reason": "string" }
  ]
}
```

**Does NOT**:
- Decide what the next sub-task should be (R2)
- Execute any action (R3)
- Assess whether the overall task goal is satisfied — only the local sub-task criteria (R4b owns global assessment)
- Merge results from multiple agents (R4b)
- Write to Shared Memory

---

## R4b — Meta-Validator

**Mission**: Collect all `SubTaskOutcome` objects from all parallel subordinate agents, merge them into a unified result, and verify whether the merged result falls within a plausible range of the user's original intent. If an out-of-range result is accepted and delivered to the user, or if a task is silently abandoned, this role is accountable.

**Skills**:
- Collect and wait for `SubTaskOutcome` from all dispatched agents (tracking completeness against the Planner's sub-task list)
- Merge parallel outcomes into a single coherent result
- Assess the merged result against the original `TaskSpec` criteria within a plausible range (not binary pass/fail — partial satisfaction with acceptable gap is valid)
- Trigger replanning by sending a `ReplanRequest` to the Planner when the merged result falls outside the plausible range
- Accept the merged result and write it to Shared Memory when within range
- Deliver the final result to the user

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Agent-Validator (R4a) | `SubTaskOutcome` JSON (one per dispatched sub-task) |
| Receives | Planner (R2) | `TaskSpec` JSON (global criteria and intent reference) |
| Produces | Planner (R2) | `ReplanRequest` JSON (when merged result outside plausible range) |
| Produces | Shared Memory (R5) | `MemoryEntry` JSON (on acceptance) |
| Produces | User | Final merged result (text) |

```json
ReplanRequest {
  "task_id":         "string",
  "merged_result":   "any",             // what was assembled from all sub-task outcomes
  "gap_summary":     "string",          // what is missing or wrong relative to user intent
  "failed_subtasks": ["string"],        // subtask_ids that returned status=failed
  "recommendation":  "replan | partial_replan | abandon"
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
- Evaluate individual sub-task output quality against local criteria (R4a)
- Execute any action (R3)
- Decompose or assign sub-tasks (R2)
- Consolidate or cross-link memory entries (deferred — Dreamer, v0.4)

---

## R5 — Shared Memory

**Mission**: Persist task history and accepted results, and serve them reliably on demand. If valid data is lost, corrupted, returned in wrong order, or silently missing from a query result, this role is accountable.

**Skills**:
- Store `MemoryEntry` objects keyed by entry ID
- Retrieve entries by task ID, tags, or semantic similarity query
- Return results ranked by relevance
- Enforce write permission: only Meta-Validator (R4b) may write in v0.3

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives (write) | Meta-Validator (R4b) | `MemoryEntry` JSON |
| Receives (read) | Planner (R2) | Query (task ID, tags, or natural language) |
| Produces | Planner (R2) | `MemoryEntry[]` ranked by relevance |

**Does NOT**:
- Reorganize, summarize, or cross-link entries (deferred — Dreamer, v0.4)
- Evaluate or judge content quality
- Accept writes from R1, R2, R3, or R4a

---

## Interaction Diagram (v0.3)

```
User
 │ free text
 ▼
[R1 Perceiver] ─── TaskSpec ──────────────────────────► [R2 Planner]
                                                              │  ▲
                                                    SubTask[] │  │ ReplanRequest
                                                       (fan   │  │
                                                        out)  │  │
                                          ┌───────────────────┘  │
                                          │                       │
                             ┌────────────┼─────────────┐         │
                             ▼            ▼             ▼         │
                       [R3 Executor] [R3 Executor] [R3 Executor]  │
                             │            │             │         │
                      ExecutionResult ExecutionResult ExecutionResult
                             │            │             │         │
                             ▼            ▼             ▼         │
                       [R4a Validator][R4a Validator][R4a Validator]
                             │  ▲         │  ▲         │  ▲       │
                       retry │  │   retry │  │   retry │  │       │
                             ▼  │         ▼  │         ▼  │       │
                       [R3 Executor] [R3 Executor] [R3 Executor]  │
                                                                   │
                       SubTaskOutcome SubTaskOutcome SubTaskOutcome│
                             │            │             │         │
                             └────────────┼─────────────┘         │
                                          │ (fan in)              │
                                          ▼                       │
                                  [R4b Meta-Validator] ───────────┘
                                          │
                               (on acceptance)
                                          │
                             ┌────────────┴──────────┐
                             ▼                       ▼
                      [R5 Shared Memory]           User
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

## Deferred to v0.4

| Component | Reason deferred |
|---|---|
| Goal Gradient Solver | Requires formalized loss function and symbolic gradient — plausible range concept in R4b must be proven first |
| Dreamer (agent-level) | Requires concrete consolidation algorithm and tagged memory schema |
| Dreamer (metaagent-level) | Depends on agent-level Dreamer |
| Emergence / memory reorganization | Depends on both Dreamer levels |

---

## Open Questions for v0.3 Implementation

| # | Question | Blocks |
|---|---|---|
| Q1 | What defines the retry budget per sub-task — fixed count, time-based, or confidence threshold? | R4a |
| Q2 | How is "plausible range" operationalized — LLM-scored rubric, threshold on criteria_scores, or explicit tolerance per criterion? | R4b |
| Q3 | How does R4b know when all parallel sub-tasks have reported in — does it hold a manifest from the Planner? | R4b |
| Q4 | What tools are available to the Executor in v0.3? | R3 |
| Q5 | What backing store for Shared Memory — SQLite, vector DB, flat JSON? | R5 |
| Q6 | How does the Perceiver determine whether an ambiguity is material enough to warrant a question vs. inferrable from context? Should this be rule-based or LLM-judged? | R1 |
