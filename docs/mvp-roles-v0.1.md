# MVP Role Definitions

**Version**: 0.1
**Status**: Draft
**Date**: 2026-02-18
**Scope**: Minimum viable organization — five roles sufficient to complete a full task cycle. Goal Gradient Solver, Dreamer, and Emergence are deferred to v0.2.

---

## Role Definition Template

Each role is defined by four elements:

- **Mission** — the outcome this role owns and is accountable for
- **Skills** — what it is capable of doing (its toolkit)
- **Contract** — what it receives (input) and produces (output), including format and counterparty
- **Does NOT** — explicit boundary; what belongs to a different role

Orthogonality is enforced by the "Does NOT" field. Every edge case must be owned by exactly one role.

---

## Role Index

| ID | Role | Type | Mission Summary |
|---|---|---|---|
| R1 | Perceiver | LLM Agent | Translate user input into a structured task spec |
| R2 | Planner | LLM Agent | Decompose task and coordinate execution to goal |
| R3 | Executor | LLM Agent + Tools | Execute one atomic sub-task and return a result |
| R4 | Validator | LLM Agent | Measure output against criteria; retry or escalate |
| R5 | Shared Memory | Data Store | Persist and serve task history and results |

---

## R1 — Perceiver

**Mission**: Transform raw user input into a structured, unambiguous task specification that the Planner can act on without further clarification.

**Skills**:
- Parse natural language into structured intent
- Identify and surface ambiguity (ask the user exactly one clarifying question if required)
- Extract measurable success criteria from the user's description
- Identify scope constraints (e.g., file paths, time bounds, domains)
- Format the task spec as a validated JSON object

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | User | Free-text natural language |
| Produces | Planner (R2) | `TaskSpec` JSON |
| Produces | User | Clarifying question (when ambiguous, at most one) |

```json
TaskSpec {
  "task_id": "string",
  "intent": "string",          // what the user wants to achieve
  "success_criteria": ["string"],  // measurable conditions for completion
  "constraints": {             // optional bounds
    "scope": "string",
    "deadline": "ISO8601 | null"
  },
  "raw_input": "string"        // original user text, preserved verbatim
}
```

**Does NOT**:
- Decompose the task into sub-tasks (R2)
- Evaluate whether a result satisfies the user's intent (R4)
- Access memory or prior task history (R5)
- Make any decisions about how the task will be executed

---

## R2 — Planner

**Mission**: Own the path from task specification to final result — decompose the task into atomic sub-tasks, coordinate Executors, replan on feedback, and deliver the final result to the user.

**Skills**:
- Read relevant prior task history from Shared Memory (R5) before planning
- Decompose a `TaskSpec` into an ordered list of `SubTask` objects
- Ensure each sub-task is atomic, self-contained, and executable without peer-agent coordination
- Dispatch sub-tasks to Executors (R3) with full context
- Receive phased feedback (gap reports) from Validators (R4) and adjust the plan
- Detect when all sub-tasks are complete and the goal is satisfied
- Deliver the final consolidated result to the user via the Perceiver (R1)

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Perceiver (R1) | `TaskSpec` JSON |
| Receives | Validator (R4) | `GapReport` JSON |
| Receives | Shared Memory (R5) | `MemoryEntry[]` (query results) |
| Produces | Executor (R3) | `SubTask` JSON |
| Produces | Shared Memory (R5) | Read query |
| Produces | User (via Perceiver) | Final result (text) |

```json
SubTask {
  "subtask_id": "string",
  "parent_task_id": "string",
  "intent": "string",            // what this sub-task achieves
  "success_criteria": ["string"],// measurable criteria for this sub-task
  "context": "string",           // relevant background the Executor needs
  "deadline": "ISO8601 | null",
  "sequence": "integer"          // ordering index; parallel tasks share same value
}
```

**Does NOT**:
- Execute any action directly (R3)
- Evaluate output quality or score results (R4)
- Consolidate or reorganize memory (deferred — Dreamer, v0.2)
- Communicate with Executors after dispatch except through Validator escalation

---

## R3 — Executor

**Mission**: Execute exactly one assigned sub-task using available tools and produce a concrete, verifiable output.

**Skills**:
- Interpret a `SubTask` and determine the required sequence of tool calls
- Use tools: shell commands, file I/O, web search, API calls, code execution (tool set is configurable per deployment)
- Produce a structured result that can be evaluated against the sub-task's success criteria
- Detect when a sub-task is impossible as specified and report uncertainty with a reason

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Planner (R2) | `SubTask` JSON |
| Produces | Validator (R4) | `ExecutionResult` JSON |

```json
ExecutionResult {
  "subtask_id": "string",
  "status": "completed | uncertain | failed",
  "output": "any",               // the actual result (text, file path, data, etc.)
  "uncertainty": "string | null",// if status != completed: reason and what is missing
  "tool_calls": ["string"]       // log of tools used, for auditability
}
```

**Does NOT**:
- Decide what to do beyond the scope of its assigned sub-task (R2)
- Evaluate whether its own output is good enough (R4)
- Communicate with other Executors (by design — all coordination through R2)
- Retry itself — if output is inadequate, the Validator decides whether to retry
- Write to Shared Memory

---

## R4 — Validator

**Mission**: Measure the gap between an Executor's output and the sub-task's success criteria, and own the decision to retry locally, escalate to the Planner, or accept and persist the result.

**Skills**:
- Score `ExecutionResult` against each success criterion in the `SubTask`
- Produce a structured gap report describing which criteria are unmet and why
- Re-invoke the Executor (retry) when the gap is non-zero and retry budget remains
- Escalate to the Planner when: (a) retry budget is exhausted, or (b) uncertainty is structural (the sub-task as specified is unresolvable)
- Accept the result and write it to Shared Memory when all criteria are met

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives | Executor (R3) | `ExecutionResult` JSON |
| Receives | Planner (R2) | `SubTask` JSON (carried with dispatch, used as evaluation rubric) |
| Produces | Executor (R3) | Retry signal + feedback (what to improve) |
| Produces | Planner (R2) | `GapReport` JSON (on escalation) |
| Produces | Shared Memory (R5) | `MemoryEntry` JSON (on acceptance) |

```json
GapReport {
  "subtask_id": "string",
  "parent_task_id": "string",
  "criteria_scores": [
    { "criterion": "string", "met": "boolean", "reason": "string" }
  ],
  "overall_gap": "string",       // summary of what is missing
  "retry_count": "integer",
  "recommendation": "replan | reassign | abandon"
}

MemoryEntry {
  "entry_id": "string",
  "task_id": "string",
  "subtask_id": "string",
  "type": "episodic",            // v0.1 only writes episodic; semantic deferred to Dreamer
  "content": "any",              // the accepted output
  "criteria_met": ["string"],
  "timestamp": "ISO8601",
  "tags": ["string"]
}
```

**Does NOT**:
- Decide what the next sub-task should be (R2)
- Execute any action (R3)
- Consolidate or cross-link memory entries (deferred — Dreamer, v0.2)
- Evaluate the final holistic result against the original user intent (R2 owns this)

---

## R5 — Shared Memory

**Mission**: Provide a persistent, queryable store of task history and accepted results that any role can read and the Validator can write to.

**Skills**:
- Store `MemoryEntry` objects keyed by entry ID
- Retrieve entries by task ID, tags, or semantic similarity query
- Return ordered, relevance-ranked results for a given query
- Enforce write permissions: only the Validator (R4) may write in v0.1

**Contract**:

| Direction | Counterparty | Format |
|---|---|---|
| Receives (write) | Validator (R4) | `MemoryEntry` JSON |
| Receives (read) | Planner (R2) | Query (task ID, tags, or natural language) |
| Produces | Planner (R2) | `MemoryEntry[]` ranked by relevance |

**Does NOT**:
- Reorganize, summarize, or cross-link entries autonomously (deferred — Dreamer, v0.2)
- Make decisions or evaluate content quality
- Accept writes from Executors or Planners directly
- Expire or delete entries without explicit instruction (retention policy is out of scope for v0.1)

---

## Interaction Diagram (MVP)

```
User
 │
 │ free text
 ▼
Perceiver (R1) ─── TaskSpec ──────────────────────► Planner (R2)
                                                         │  ▲
                                          MemoryEntry[]  │  │ GapReport
                                                         │  │ (escalation)
                              Shared Memory (R5) ◄───────┘  │
                                         │                   │
                              MemoryEntry[]                  │
                                         │                   │
                                         └──► Planner (R2)   │
                                                  │           │
                                            SubTask           │
                                                  │           │
                                                  ▼           │
                                           Executor (R3)      │
                                                  │           │
                                        ExecutionResult       │
                                                  │           │
                                                  ▼           │
                                           Validator (R4) ────┘
                                                  │
                                        (on accept)
                                                  │
                                                  ▼
                                         Shared Memory (R5)
```

Retry loop (local, within Validator → Executor):
```
Validator (R4) ── retry + feedback ──► Executor (R3)
      ▲                                      │
      └──────── ExecutionResult ─────────────┘
```

---

## Deferred to v0.2

| Component | Reason deferred |
|---|---|
| Goal Gradient Solver | Requires formalized loss function and symbolic gradient representation — not implementable until the metrics contract is proven in v0.1 |
| Dreamer (agent-level) | Requires concrete consolidation algorithm and tagged memory schema — memory schema must be validated in v0.1 first |
| Dreamer (metaagent-level) | Depends on agent-level Dreamer output |
| Emergence / memory reorganization | Depends on both Dreamer levels |
| meta-Validator | Merged into Planner for v0.1; separation justified only when Planner complexity grows |

---

## Open Questions for v0.1 Implementation

| # | Question | Blocks |
|---|---|---|
| Q1 | What is the retry budget per sub-task? Fixed count or time-based? | R4 implementation |
| Q2 | What tools are available to the Executor in v0.1? (shell only? web? code execution?) | R3 implementation |
| Q3 | What backing store for Shared Memory — SQLite, vector DB, flat JSON? | R5 implementation |
| Q4 | How does the Planner determine when the overall goal is satisfied vs. just all sub-tasks completed? | R2 implementation |
| Q5 | Does the Perceiver ask at most one clarifying question, or is multi-turn clarification allowed? | R1 implementation |
