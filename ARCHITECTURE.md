# Agentic Framework Architecture

## Document Map

| Document | Purpose |
|---|---|
| `ARCHITECTURE.md` (this file) | Stable design principles, structure, and rationale |
| `docs/mvp-roles-v*.md` | Versioned role specifications — contracts, schemas, open questions |

---

## Overview

This framework is an intelligent task execution system that takes natural language goals from a user and coordinates a hierarchy of LLM-based agents to achieve them. It is designed around four convictions:

1. **Hierarchy over mesh** — coordination through structure, not negotiation
2. **Validation as the primary control mechanism** — not retry policies, not timeouts
3. **Memory that learns** — results are consolidated across tasks, not discarded
4. **Independent observability** — a lateral auditor outside the operational hierarchy reports system health directly to the human operator; no agent can suppress or instruct it

---

## Design Philosophy

### Bio-Inspiration as Structural Hypothesis

Biology is used as a source of structural hypotheses, not decoration. The premise is that systems subjected to evolutionary pressure under real-world complexity have converged on architectures with demonstrably good properties. We borrow the structure when it maps onto a concrete computational mechanism; we discard the metaphor once the mechanism stands on its own.

Three principles are borrowed directly:

| Biological principle | Framework mechanism |
|---|---|
| Hierarchical human organization | Metaagent-centralized coordination; no peer-to-peer agent communication |
| Sleep consolidation of episodic into semantic memory | Two-level Dreamer: agent-local and metaagent-global memory reorganization |
| Error-signal backpropagation in learning systems | Goal Gradient Solver: directed plan adjustment from measured goal gap |

The biological analogy is scaffolding. The algorithm is the building. For any component described in biological terms, the implementation must specify: *what exactly runs, when, and how its output is evaluated.*

### Nested Feedback Loops

The framework's control structure is a single pattern — **decision → execution → correction → execution** — instantiated at three nested scales simultaneously:

| Scale | Decision | Execution | Correction | Criterion |
|---|---|---|---|---|
| Fast (action) | Agent-Validator issues retry feedback | Executor re-runs | Agent-Validator re-measures gap | Sub-task success criteria |
| Medium (task) | Goal Gradient Solver adjusts plan | Planner re-dispatches | Meta-Validator re-merges outcomes | User intent within plausible range |
| Slow (system) | Dreamer synthesizes new strategy | Next task planning | Next Meta-Validator cycle | Long-term goal quality across tasks |

These are not three separate mechanisms. They are the same closed-loop control structure operating at different time scales with different sensors, controllers, and actuators. The separation of scales ensures that fast feedback (action-level retries) does not flood the task-level planner, and that system-level consolidation does not block execution.

---

## System Structure

The system has an operational hierarchy of two tiers, plus one lateral observer outside the hierarchy:

```
                    ┌─── MESSAGE BUS ──────────────────────────────────┐
                    │  all inter-role messages pass through here        │
                    │                          ┌──────────────────────┐ │
                    │                          │  AUDITOR             │ │
                    │                          │  (read-only tap)     │ │
                    │                          │  → Human Operator    │ │
                    │                          └──────────────────────┘ │
                    └──────────────────────────────────────────────────┘
                                       │
        ┌──────────────────────────────┼──────────────────────────────┐
        │           OPERATIONAL HIERARCHY                             │
        │  ┌─────────────────────────────────────────────────────┐   │
        │  │                    METAAGENT                         │   │
        │  │  Planner · Meta-Validator · GGS · Dreamer           │   │
        │  └────────────────────┬────────────────────────────────┘   │
        │                       │  hierarchical dispatch / outcomes   │
        │          ┌────────────┼────────────┐                       │
        │          ▼            ▼            ▼                       │
        │  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐       │
        │  │EFFECTOR AGENT│ │EFFECTOR AGENT│ │EFFECTOR AGENT│       │
        │  │Executor      │ │Executor      │ │Executor      │       │
        │  │Agent-Validator│ │Agent-Validator│ │Agent-Validator│     │
        │  │Dreamer       │ │Dreamer       │ │Dreamer       │       │
        │  └──────────────┘ └──────────────┘ └──────────────┘       │
        │                        │                                   │
        │                 ┌──────┴──────┐                            │
        │                 ▼             ▼                            │
        │          Shared Memory    (read/write)                     │
        └────────────────────────────────────────────────────────────┘
```

### Metaagent

The central brain. Owns the user's goal from receipt to delivery. Contains:

**Planner** — decomposes the user's goal into atomic sub-tasks and dispatches them. Each sub-task must be self-contained: no peer-agent coordination required. If a sub-task requires output from a sibling, that dependency must be resolved at decomposition time, not at execution time. Frequent mid-execution dependency discoveries are a signal that decomposition quality is insufficient.

**Meta-Validator** — collects outcomes from all parallel effector agents, merges them into a unified result, and assesses whether the merged result satisfies the user's original intent within a plausible range. Plausible range is intentional: binary pass/fail is too brittle for open-ended tasks. When the result falls outside acceptable range, it triggers replanning. When within range, it accepts and delivers.

**Goal Gradient Solver** — the correction phase of the metaagent's feedback loop. It is a dynamic-differential machine: *differential* because it operates on the gap between current state and goal, computing the direction and magnitude of plan adjustment needed; *dynamic* because it is history-dependent — it tracks how the gap evolves across successive correction cycles, not just the current snapshot.

The dynamic property is what separates it from naive replanning. A static replanner sees the current gap and patches it. The GGS sees the trajectory: if the gap is shrinking, corrections should diminish (convergence is occurring); if the gap is stable or growing despite corrections, the correction strategy itself must change (the current approach is not converging). This trajectory-awareness is the mechanism that gives the framework generalizability — it can adapt its adaptation.

Formally, this is the controller in a closed-loop control system. Its inputs are the gap signal from the Meta-Validator and the history of prior corrections. Its output is a structured adjustment to the Planner: which decisions in the current plan contributed to the gap and in what direction they should be revised. The Planner is the actuator; the Meta-Validator is the sensor. The loop runs until the gap falls within plausible range or the system determines convergence is not achievable with the current goal representation.

**Dreamer (metaagent-level)** — runs asynchronously between tasks. Consolidates cross-task, cross-agent episodic memories into semantic knowledge: patterns, generalizations, and reusable strategies. This is the system's long-term learning mechanism.

### Effector Agent

Each effector executes one atomic sub-task. It is intentionally narrow — it has no view of sibling agents or the global task state. Contains:

**Executor** — executes the sub-task using available tools. Its only obligation is to produce a result and report its status honestly, including infeasibility.

**Agent-Validator** — closes the gap between the Executor's output and the sub-task goal. The retry loop is entirely internal: the metaagent sees neither raw execution results nor retry counts. The agent speaks upward only when the gap is closed (matched outcome) or irresolvable (failure outcome). This keeps the metaagent's coordination bandwidth focused on goal-level decisions, not execution-level noise.

**Dreamer (agent-level)** — runs after sub-task completion. Consolidates episodic memory from this execution: what was tried, what worked, what failed. Feeds into Shared Memory as raw material for the metaagent Dreamer's semantic consolidation.

### Auditor

A lateral observer outside the operational hierarchy. Its principal is the human operator — not the Planner, not the Metaagent. No agent in the hierarchy can instruct, configure, or suppress it. It has read-only access to the message bus and write access only to a separate, immutable audit log that no agent can read or modify.

The Auditor monitors three failure classes invisible to the operational hierarchy:

- **Boundary violations** — a role doing what its "Does NOT" constraint prohibits
- **Convergence failures** — gap trend not improving across correction cycles; the system is thrashing rather than converging
- **Role drift** — systematic degradation in a role's behavior over time, undetectable from any single event

The Auditor does not intervene. Intervention would make it a second Planner and break the hierarchy. Its output is reports to the human operator only. The human decides whether and how to act.

### Observable Message Bus

All inter-role communications must pass through a shared message bus that the Auditor can tap as a read-only observer. Direct point-to-point calls between roles are not permitted.

This is a first-class architectural constraint, not an infrastructure detail. It must be established before any role is implemented. Retrofitting observability after point-to-point calls are in place requires restructuring the entire communication layer.

### Shared Memory

A single persistent store accessible to all components. It is the substrate that makes the framework learn across tasks, not just within them. Two classes of memory coexist:

- **Episodic** — written by agent Dreamers after each sub-task: what happened, verbatim
- **Semantic** — written by the metaagent Dreamer after consolidation: patterns and generalizations extracted from episodic records

The Planner reads from Shared Memory before decomposing a new task, allowing prior experience to inform current planning.

---

## Data Flow

```
                  ┌──────────────── MESSAGE BUS ────────────────────┐
                  │                            ┌────────────────┐   │
                  │                            │    Auditor     │   │
                  │                            │ (read-only tap)│   │
                  │                            └───────┬────────┘   │
                  └────────────────────────────────────│────────────┘
                                                       │ AuditReport
                                                       ▼
User                                           Human Operator
 │ natural language goal
 ▼
Perceiver ── structured goal ──► Planner ◄── Shared Memory
                                    │
                         sub-tasks (fan-out)
                                    │
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
         [Effector]            [Effector]            [Effector]
         Executor               Executor               Executor
            ↕ (internal           ↕                     ↕
           retry loop)
         Agent-Validator    Agent-Validator        Agent-Validator
              │                     │                     │
              └─────────────────────┼─────────────────────┘
                         outcomes (fan-in)
                                    │
                                    ▼
                             Meta-Validator
                          [merge + range check]
                           │                │
                    replan │                │ accept
                           ▼                ▼
                        Planner    Shared Memory + User

[async, between tasks]
Agent Dreamers ──────► Shared Memory (episodic)
Metaagent Dreamer ───► Shared Memory (semantic, synthesized from episodic)
```

The flow has five structural properties worth preserving in any implementation:

1. **Fan-out from Planner**: sub-tasks are dispatched in parallel; agents do not wait for each other
2. **Internal retry**: the Agent-Validator loop is invisible to the metaagent; only outcomes cross the boundary
3. **Fan-in to Meta-Validator**: all outcomes are collected before global assessment; partial assessment is not permitted
4. **Async consolidation**: memory reorganization never blocks the execution path
5. **Observable bus**: every inter-role message is routed through the message bus; the Auditor taps it without interrupting flow; no direct point-to-point calls are permitted

---

## Key Design Decisions

### No Inter-Agent Communication

Effector agents do not communicate with each other. All coordination is mediated by the Planner.

Mesh communication scales as O(n²) in coordination cost and requires every agent to maintain a global view of task state. Hierarchical communication scales as O(n) and bounds each agent's required context to its own sub-task. The cost is additional latency when runtime dependencies are discovered; the benefit is predictable coordination behavior and clean failure attribution.

This is a permanent architectural constraint, not an MVP simplification.

### Two-Level Validation with Distinct Scopes

Validation is separated into two roles with non-overlapping accountability:

- **Agent-Validator**: scope is the sub-task. Criterion is the sub-task's success criteria. Output is an outcome (matched or failed), never a gap report.
- **Meta-Validator**: scope is the user's original intent. Criterion is plausible range satisfaction of the full `TaskSpec`. Output is either a replanning request or a final delivery.

Merging these into one role would require it to answer to two principals simultaneously — the sub-task criteria and the user intent — with different failure modes and different escalation paths. That ambiguity breaks accountability.

### Validation as the Primary Control Mechanism

There is no separate retry policy, timeout manager, or error handler. The Agent-Validator's gap measurement is the retry criterion. If the gap is non-zero, execution continues. If the gap cannot be closed, the outcome is reported as failed and the Meta-Validator decides what to do. This ensures the retry criterion never drifts from the success criterion.

### Centralized Shared Memory

One persistent store rather than per-agent private memories. Private memories would make cross-agent learning impossible — the metaagent Dreamer could not consolidate across agents, and the Goal Gradient Solver could not retrieve relevant prior plans. The shared store is what gives the framework memory that compounds across tasks.

### Independent Auditor with Separate Principal

The Auditor is the only component in the system whose principal is the human operator rather than any agent. This is not a matter of access control — it is a structural property. An auditor that can be instructed by the Planner is not an auditor; it is a subordinate with a reporting function.

Two properties must be preserved unconditionally:

1. **Non-participation**: the Auditor never sends messages to any agent. It observes only. The moment it issues a correction it becomes a second controller and the loop structure breaks.
2. **Immutable, isolated log**: the audit log is separate from Shared Memory. No agent can read, modify, or suppress it. Evidence integrity is a precondition for meaningful oversight.

The feedback loops are self-contained by design — which is a feature for operational efficiency but a liability for observability. The Auditor is the deliberate compensation: it provides the external view that the loops cannot provide about themselves.

### Goal Gradient Solver as Dynamic-Differential Controller

The GGS is the correction phase of a closed-loop control system, not a replanner. The distinction matters:

- A replanner reacts to failure by generating a new plan from scratch. It has no memory of how it got here.
- A dynamic-differential controller maintains a history of gap measurements across correction cycles. It computes not only the current gap but the trajectory of the gap — shrinking, stable, or growing. Corrections are scaled and directed by both the current error and its rate of change.

This trajectory-awareness is what gives the framework adaptive behavior beyond the current task. A plan that consistently produces shrinking gaps is reinforced. A correction strategy that fails to converge triggers a qualitative change in approach. Over time, this signal feeds back into memory (via the Dreamer) and improves planning quality for future tasks.

The formal structure: GGS is the controller; Meta-Validator is the sensor producing the error signal; Planner is the actuator receiving the correction; the effector agents are the plant. This is a hierarchical closed-loop system, where the same loop structure runs at the agent level (Executor → Agent-Validator) and at the metaagent level (Planner → GGS → Meta-Validator).

---

## Philosophical Boundaries

Three places where the biological metaphor risks misleading implementation:

**Dreamer**: the metaphor is sound (sleep consolidates memory); the mechanism must be specified concretely. "Reorganize memory like dreaming" is a description of desired behavior, not an algorithm. The implementation must define what it reads, what it produces, and how conflicts between concurrent writes are resolved.

**Emergence (涌现)**: biological emergence arises from massive parallelism of simple rules at enormous scale. A small agent system will not produce genuine emergence. The memory synthesis that occurs when the metaagent Dreamer consolidates across agents is best described as *synthesis*, not emergence. The label should not drive the implementation toward unpredictable behavior.

**Goal Gradient Solver**: the backpropagation analogy has been retired. The correct framing is a dynamic-differential controller in a closed-loop hierarchical system — this is a control theory concept, not a biological or machine-learning one. The implementation should be evaluated on two properties: (1) does it compute a *directed* adjustment rather than a random or exhaustive one, and (2) does it use the *trajectory* of the gap across cycles, not just the current snapshot. An implementation that ignores trajectory is a replanner, not a gradient solver.

---

## Known Risks

| Risk | Nature | Architectural response |
|---|---|---|
| Goal drift | Meta-Validator optimizes for a proxy of user intent rather than the real intent | Intent capture at the Perceiver is load-bearing; the `TaskSpec` success criteria must be precise enough to evaluate against |
| Planner decomposition failure | Sub-tasks with hidden dependencies surface mid-execution, forcing repeated replanning | Track replanning rate; the Goal Gradient Solver should penalize decomposition strategies that produce frequent failures |
| Shared memory bottleneck | Concurrent reads/writes under parallel agent load | Addressed at the infrastructure level; not a design constraint on the architecture |
| Dreamer without formal output spec | Implemented as ad hoc summarization, producing memory entries the Planner cannot reliably query | Dreamer output schema must be specified before implementation, not after |
| Credit assignment in Goal Gradient Solver | When multiple agents contribute to a failure, attributing responsibility across them requires both causal attribution (which agent caused the gap) and temporal attribution (did the gap emerge immediately or accumulate across correction cycles) | The dynamic dimension of the GGS — its gap trajectory history — is what makes temporal attribution possible; an implementation that discards trajectory history loses this capability |
| Auditor blind spot | The Auditor can only observe messages that pass through the bus; in-process state changes or side effects that do not produce bus messages are invisible to it | All state changes with operational significance must produce a bus message; silent state mutation by any role is a design violation |
| Auditor suppression | An agent that learns the Auditor's detection patterns could produce outputs that avoid triggering alerts while still producing bad results | The Auditor's detection logic must not be visible to operational agents; its configuration is controlled by the human operator only |

---

## Architectural Rules

System-level invariants that apply across all roles. These are not role contracts — they
are properties the system as a whole must preserve. Each rule has a status: **settled**
(conclusion reached, implemented or pending implementation) or **open** (discussion
in progress, conclusion pending).

---

### Rule #1 — Best Effort Without Self-Harm

**Status**: partially settled — open question remaining (see below)

**Statement**: The system must pursue the user's goal as hard as possible, but must not
degrade its own capacity to function in the process.

**Rationale**: A system that stops at the first obstacle is useless. A system that runs
itself to exhaustion or corrupts its own state to complete one task is also useless — it
makes the next task harder or impossible. These two failure modes are symmetric: both
represent abandoning the user, one by giving up too early, the other by destroying the
instrument.

**What "best effort" means in this architecture**:
- Exhaust the full fast-loop retry budget (R4a: maxRetries) before escalating to replan
- Exhaust the full medium-loop replan budget (R4b: maxReplans) before abandoning
- Use `failure_class` to distinguish environmental from logical failures and route
  replanning accordingly — environmental failures deserve a different approach, not the
  same approach retried

**What "not harm itself" means in this architecture**:

1. **Convergence integrity**: if `gap_trend` is worsening for 2 consecutive replans
   (not just present once), the system must stop. Continuing to consume replan budget
   on a diverging trajectory is self-harm — it exhausts resources without improving the
   outcome. The stopping condition must be convergence-aware, not just count-based.

2. **Memory integrity**: never write a `MemoryEntry` that misattributes a failure cause.
   A procedural entry that records the wrong lesson (e.g. blames approach X when the
   real failure was environmental) poisons future calibration. Writing false memory is
   self-harm — it makes the system worse at the next task.

3. **Scope integrity**: do not silently expand the task's stated scope to make it
   achievable. If the task as specified is impossible, report that honestly rather than
   solving a nearby easier problem and claiming success. Scope creep is self-harm —
   it teaches the system that the original goal was achieved when it was not, corrupting
   the feedback signal.

**What is already implemented**:
- R4a retry cap (maxRetries=2): fast-loop bound ✓
- R4b replan cap (maxReplans=3): medium-loop bound ✓
- `gap_trend` detection in ReplanRequest ✓
- `failure_class` in criterion verdicts (logical | environmental) — spec complete, pending implementation

**What is not yet specified**:
- Convergence kill-switch: 2 consecutive worsening replans → abort, not count-down
- Memory integrity enforcement: who validates that a procedural entry's root cause is
  correctly attributed before it is written?

**Open question (⚠ conclusion pending — resume discussion)**:
Does "not harm itself" extend to the user's environment? For example: executing a
destructive shell command (deletion, overwrite) that cannot be undone harms the user's
state and also corrupts the system's memory of what the task achieved. Is this a
sub-case of Rule #1 (the system harmed its own feedback signal), or a separate Rule #2
(execution safety)?

The answer determines whether Rule #1 is purely about **system health** (loops, memory
integrity, resource budget) or also covers **execution safety** (irreversible operations,
scope creep into the user's environment).
