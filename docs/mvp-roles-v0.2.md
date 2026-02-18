# MVP Role Definitions

**Version**: 0.2
**Status**: Draft
**Date**: 2026-02-19
**Scope**: Minimum viable organization — six roles sufficient to complete a full task cycle. Goal Gradient Solver, Dreamer, and Emergence are deferred to v0.3.

## Changelog from v0.1

| Change | Reason |
|---|---|
| Split R4 (Validator) into R4a (Agent-Validator) and R4b (Meta-Validator) | Original design has two validators at different levels with different missions; collapsing them violated orthogonality — they answer to different principals, operate on different scopes, and have different failure modes |
| Rewrote all Mission statements to include explicit accountability | v0.1 missions described capability ("execute one sub-task") rather than ownership ("if X goes wrong, this role is responsible"). Accountability must be stated, not implied |

---

## Role Definition Template

Each role is defined by four elements:

- **Mission** — the outcome this role owns. Includes explicit accountability: if this outcome fails, this role is responsible — not any other.
- **Skills** — what it is capable of doing (its toolkit)
- **Contract** — what it receives (input) and produces (output), including format and counterparty
- **Does NOT** — explicit boundary; what belongs to a different role

Orthogonality is enforced by the "Does NOT" field. Every edge case must be owned by exactly one role.

---

## Role Index

| ID | Role | Lives in | Mission Summary |
|---|---|---|---|
| R1 | Perceiver | Entry point | If the task is misunderstood, this role is responsible |
| R2 | Planner | Metaagent | If the goal is not achieved despite valid execution, this role is responsible |
| R3 | Executor | Effector Agent | If a feasible sub-task is not correctly executed, this role is responsible |
| R4a | Agent-Validator | Effector Agent | If a bad sub-task result is accepted locally, this role is responsible |
| R4b | Meta-Validator | Metaagent | If a failed or incomplete result is delivered to the user, this role is responsible |
| R5 | Shared Memory | Infrastructure | If valid data is lost, corrupted, or wrongly retrieved, this role is responsible |

---

## R1 — Perceiver

**Mission**: Translate raw user input into a structured, unambiguous task specification. If the downstream system acts on a wrong or underspecified goal, this role is accountable — not the Planner for planning poorly, not the Executor for executing the wrong thing.

**Skills**:
- Parse natural language into structured intent
- Detect ambiguity and ask the user exactly one clarifying question if required
- Extract measurable success criteria from the user's description
- Identify scope constraints (file paths, time bounds, domains)
- Produce a validated `TaskSpec` JSON

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | User | Free-text natural language |
| Produces | Planner (R2) | `TaskSpec` JSON |
| Produces | User | Clarifying question (at most one, only when ambiguous) |

```json
TaskSpec {
  "task_id":          "string",
  "intent":           "string",       // what the user wants to achieve
  "success_criteria": ["string"],     // measurable conditions for completion
  "constraints": {
    "scope":    "string | null",
    "deadline": "ISO8601 | null"
  },
  "raw_input": "string"               // original user text, preserved verbatim
}
```

**Does NOT**:
- Decompose the task into sub-tasks (R2)
- Evaluate whether a result satisfies the user (R4b)
- Access memory or prior history (R5)
- Make any decision about how the task will be executed

---

## R2 — Planner

**Mission**: Own the path from task specification to final result. If the overall goal is not achieved despite Executors completing their sub-tasks correctly — because decomposition was wrong, sequencing was wrong, or replanning was missed — this role is accountable.

**Skills**:
- Query Shared Memory (R5) for relevant prior experience before planning
- Decompose a `TaskSpec` into an ordered list of atomic `SubTask` objects
- Ensure each sub-task is self-contained and executable without peer-agent coordination
- Dispatch sub-tasks to Executors (R3)
- Receive `GapReport` from Meta-Validator (R4b) and adjust the plan accordingly
- Determine when the overall goal is satisfied and deliver the final result to the user

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Perceiver (R1) | `TaskSpec` JSON |
| Receives | Meta-Validator (R4b) | `GapReport` JSON |
| Receives | Shared Memory (R5) | `MemoryEntry[]` |
| Produces | Executor (R3) | `SubTask` JSON |
| Produces | Shared Memory (R5) | Read query |
| Produces | User (via Perceiver) | Final result (text) |

```json
SubTask {
  "subtask_id":       "string",
  "parent_task_id":   "string",
  "intent":           "string",       // what this sub-task achieves
  "success_criteria": ["string"],     // measurable criteria for this sub-task alone
  "context":          "string",       // background the Executor needs
  "deadline":         "ISO8601 | null",
  "sequence":         "integer"       // ordering index; parallel tasks share same value
}
```

**Does NOT**:
- Execute any action directly (R3)
- Evaluate output quality at either the sub-task or full-task level (R4a, R4b)
- Consolidate or reorganize memory (deferred — Dreamer, v0.3)
- Communicate with Executors after dispatch except through the Meta-Validator escalation path

---

## R3 — Executor

**Mission**: Execute exactly one assigned sub-task and return a concrete, verifiable result. If a sub-task is feasible as specified and the result is wrong or missing, this role is accountable. If the sub-task is infeasible as specified, the Agent-Validator escalates that — it is not the Executor's fault.

**Skills**:
- Interpret a `SubTask` and determine the required tool call sequence
- Use available tools: shell commands, file I/O, web search, API calls, code execution (tool set is configurable per deployment)
- Produce a structured result evaluable against the sub-task's success criteria
- Detect and report infeasibility with a specific reason

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Planner (R2) | `SubTask` JSON |
| Produces | Agent-Validator (R4a) | `ExecutionResult` JSON |

```json
ExecutionResult {
  "subtask_id": "string",
  "status":     "completed | uncertain | failed",
  "output":     "any",                // the actual result
  "uncertainty":"string | null",      // if status != completed: reason and what is missing
  "tool_calls": ["string"]            // log of tools used, for auditability
}
```

**Does NOT**:
- Decide what to do beyond its assigned sub-task (R2)
- Evaluate whether its own output meets the criteria (R4a)
- Communicate with other Executors
- Retry itself — the Agent-Validator owns the retry decision
- Write to Shared Memory

---

## R4a — Agent-Validator

**Mission**: Act as the local quality gate inside each effector agent. If a sub-task result fails its criteria and that failure goes undetected or unescalated, this role is accountable. It owns the retry loop and the escalation decision — the Executor owns only execution.

**Skills**:
- Score `ExecutionResult` against each criterion in the `SubTask`
- Re-invoke the Executor with specific feedback when the gap is non-zero and retry budget remains
- Escalate to the Meta-Validator (R4b) when: (a) retry budget is exhausted, or (b) the sub-task is structurally infeasible as specified

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Executor (R3) | `ExecutionResult` JSON |
| Receives | Planner (R2) | `SubTask` JSON (carried with dispatch; serves as evaluation rubric) |
| Produces | Executor (R3) | Retry signal + targeted feedback |
| Produces | Meta-Validator (R4b) | `GapReport` JSON (on escalation) |

```json
GapReport {
  "subtask_id":      "string",
  "parent_task_id":  "string",
  "criteria_scores": [
    { "criterion": "string", "met": "boolean", "reason": "string" }
  ],
  "overall_gap":     "string",        // summary of what is missing
  "retry_count":     "integer",
  "recommendation":  "replan | reassign | abandon"
}
```

**Does NOT**:
- Decide what the next sub-task should be (R2)
- Execute any action (R3)
- Assess whether the overall task goal is satisfied — only the local sub-task criteria (R4b owns global assessment)
- Write to Shared Memory (R4b owns this after global acceptance)

---

## R4b — Meta-Validator

**Mission**: Act as the global quality gate inside the metaagent. If the final result delivered to the user does not satisfy the original intent, or if a failed task is silently abandoned, this role is accountable. It receives escalations from Agent-Validators, assesses them against the full task goal, and decides whether to trigger replanning or accept and close the task.

**Skills**:
- Receive and interpret `GapReport` objects from Agent-Validators (R4a)
- Assess whether the gap is local (sub-task issue → replan that sub-task) or structural (goal issue → replan the whole task)
- Trigger replanning by sending a `GapReport` to the Planner (R2)
- Accept the final result when all sub-tasks pass and the overall `TaskSpec` criteria are met
- Write the final accepted result to Shared Memory (R5)
- Deliver the final result to the user

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Agent-Validator (R4a) | `GapReport` JSON |
| Receives | Planner (R2) | `TaskSpec` JSON (for global criteria reference) |
| Produces | Planner (R2) | `GapReport` JSON (replanning trigger) |
| Produces | Shared Memory (R5) | `MemoryEntry` JSON (on task acceptance) |
| Produces | User | Final result (text) |

```json
MemoryEntry {
  "entry_id":     "string",
  "task_id":      "string",
  "subtask_id":   "string | null",    // null for task-level entries
  "type":         "episodic",         // v0.2 only writes episodic; semantic deferred to Dreamer
  "content":      "any",
  "criteria_met": ["string"],
  "timestamp":    "ISO8601",
  "tags":         ["string"]
}
```

**Does NOT**:
- Evaluate individual sub-task output quality (R4a)
- Execute any action (R3)
- Decompose or assign tasks (R2)
- Consolidate or cross-link memory entries (deferred — Dreamer, v0.3)

---

## R5 — Shared Memory

**Mission**: Persist task history and accepted results, and serve them reliably on demand. If valid data is lost, corrupted, returned in wrong order, or silently missing from a query result, this role is accountable. It makes no decisions about content.

**Skills**:
- Store `MemoryEntry` objects keyed by entry ID
- Retrieve entries by task ID, tags, or semantic similarity query
- Return results ranked by relevance to the query
- Enforce write permissions: only Meta-Validator (R4b) may write in v0.2

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives (write) | Meta-Validator (R4b) | `MemoryEntry` JSON |
| Receives (read) | Planner (R2) | Query (task ID, tags, or natural language) |
| Produces | Planner (R2) | `MemoryEntry[]` ranked by relevance |

**Does NOT**:
- Reorganize, summarize, or cross-link entries (deferred — Dreamer, v0.3)
- Evaluate or judge the content of entries
- Accept writes from Executors, Planners, or Agent-Validators directly
- Expire or delete entries without explicit instruction

---

## Interaction Diagram (MVP v0.2)

```
User
 │ free text
 ▼
[R1 Perceiver] ── TaskSpec ──────────────────────► [R2 Planner]
                                                        │  ▲
                                                 SubTask│  │GapReport
                                                        │  │(replanning)
                                                        ▼  │
                                                  [R3 Executor]
                                                        │
                                              ExecutionResult
                                                        │
                                                        ▼
                                              [R4a Agent-Validator]
                                               │            │
                                    retry+feedback          │ GapReport
                                               │            │ (escalation)
                                               ▼            ▼
                                         [R3 Executor] [R4b Meta-Validator]
                                                            │  ▲
                                                 GapReport  │  │MemoryEntry[]
                                                 (replan)   │  │
                                                            ▼  │
                                                       [R2 Planner]
                                                            │
                                                     [R5 Shared Memory]
                                                            │
                                              Final result  │
                                                            ▼
                                                          User
```

---

## Accountability Map

Who is to blame for each failure mode:

| Failure | Accountable Role |
|---|---|
| System acts on wrong goal | R1 Perceiver |
| Goal not achieved despite correct execution | R2 Planner |
| Feasible sub-task not correctly executed | R3 Executor |
| Bad sub-task result accepted without retry | R4a Agent-Validator |
| Failed result delivered to user | R4b Meta-Validator |
| Data loss, corruption, or wrong retrieval | R5 Shared Memory |

---

## Deferred to v0.3

| Component | Reason deferred |
|---|---|
| Goal Gradient Solver | Requires formalized loss function and symbolic gradient — metrics contract must be proven in v0.2 first |
| Dreamer (agent-level) | Requires concrete consolidation algorithm and tagged memory schema |
| Dreamer (metaagent-level) | Depends on agent-level Dreamer output |
| Emergence / memory reorganization | Depends on both Dreamer levels |

---

## Open Questions for v0.2 Implementation

| # | Question | Blocks |
|---|---|---|
| Q1 | What is the retry budget per sub-task — fixed count or time-based? | R4a |
| Q2 | What tools are available to Executor in v0.2 — shell only, web, code execution? | R3 |
| Q3 | What backing store for Shared Memory — SQLite, vector DB, flat JSON? | R5 |
| Q4 | How does the Planner determine overall goal satisfaction beyond all sub-tasks passing? | R2, R4b |
| Q5 | Does the Perceiver allow multi-turn clarification or strictly one question? | R1 |
