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

**Where this system deliberately diverges from the biological model — and why it is better:**

In a human organization, the forth-and-back of goal achievement (plan → execute → correct → replan) involves *negotiation*. Negotiation is necessary because information is distributed: each team member sees only their own work, managers receive filtered reports, and alignment requires communication between peers with partial views. The back-and-forth is the mechanism for resolving information asymmetry.

This system eliminates that asymmetry entirely. The Metaagent is omniscient within its operational scope: it composed every sub-task, holds the complete `TaskSpec`, observes every `CorrectionSignal` accumulating in the fast loop, receives every `SubTaskOutcome` with its full `gap_trajectory`, and has access to all prior memory. There is no information to negotiate — the Metaagent already has it all.

The consequence: the forth-and-back in this system is not about aligning team members' partial views. It is about **iterative discovery of what reality allows**. The plan is a hypothesis. Execution tests it against the real world. The gap signal is the reality check. The GGS computes a correction from complete trajectory data — more precisely than any human manager could, because no human manager has full real-time visibility into every sub-task's evidence at every retry attempt.

The bio-inspiration is borrowed for the **loop structure** (decision → execution → correction), not for the communication patterns within the loop. The implementation is superior to the biological model for exactly this reason: omniscience replaces negotiation, and a precise gradient replaces approximate consensus.

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

The central brain. Owns the user's goal from receipt to delivery. **It is the omniscient
coordinator of the system**: it holds the complete `TaskSpec`, dispatched every sub-task,
observes all in-flight correction signals, receives every outcome with its full evidence
trail, and queries all prior memory. No effector agent has this view — each sees only its
own sub-task. This god-view is not a convenience; it is the structural property that makes
precise gradient computation possible and eliminates the need for peer-to-peer negotiation.
The GGS can compute a directed, evidence-complete correction because the Metaagent has
perfect information — something no human manager ever has. Contains:

**Planner** — decomposes the user's goal into atomic sub-tasks and dispatches them. Each sub-task must be self-contained: no peer-agent coordination required. If a sub-task requires output from a sibling, that dependency must be resolved at decomposition time, not at execution time. Frequent mid-execution dependency discoveries are a signal that decomposition quality is insufficient.

**Meta-Validator** — collects outcomes from all parallel effector agents, merges them into a unified result, and assesses whether the merged result satisfies the user's original intent within a plausible range. Plausible range is intentional: binary pass/fail is too brittle for open-ended tasks. When the result falls outside acceptable range, it triggers replanning. When within range, it accepts and delivers.

**Goal Gradient Solver** — the correction phase of the metaagent's feedback loop. It is a dynamic-differential machine: *differential* because it operates on the gap between current state and goal, computing the direction and magnitude of plan adjustment needed; *dynamic* because it is history-dependent — it tracks how the gap evolves across successive correction cycles, not just the current snapshot.

The dynamic property is what separates it from naive replanning. A static replanner sees the current gap and patches it. The GGS sees the trajectory: if the gap is shrinking, corrections should diminish (convergence is occurring); if the gap is stable or growing despite corrections, the correction strategy itself must change (the current approach is not converging). This trajectory-awareness is the mechanism that gives the framework generalizability — it can adapt its adaptation.

Formally, this is the controller in a closed-loop control system. Its inputs are the gap signal from the Meta-Validator and the history of prior corrections. Its output is a structured adjustment to the Planner: which decisions in the current plan contributed to the gap and in what direction they should be revised. The Planner is the actuator; the Meta-Validator is the sensor. The loop runs until the gap falls within plausible range or the system determines convergence is not achievable with the current goal representation.

**Dreamer (metaagent-level)** — runs asynchronously between tasks. Consolidates cross-task, cross-agent episodic memories into semantic knowledge: patterns, generalizations, and reusable strategies. This is the system's long-term learning mechanism.

### Effector Agent

Each effector executes one atomic sub-task. It is intentionally narrow — it has no view
of sibling agents or the global task state.

**Effector Agents are ephemeral by design.** A new instance is spawned per sub-task and
terminates when the sub-task completes. There is no persistent "same agent" across tasks;
two Executor instances are interchangeable workers with no shared identity. This has a
direct consequence for memory: private per-agent memory is meaningless for ephemeral
agents — there is no continuity of identity to accumulate knowledge into. All long-term
learning is centralized in Shared Memory, organized by the Metaagent.

Effector Agents do not query Shared Memory directly. Their access to prior experience is
**indirect**: R2 reads and calibrates memory at planning time, then injects the relevant
context into each `SubTask.context` field before dispatch. The Executor receives
history-informed instructions without ever touching the memory store itself. A direct
memory query by an Executor would bypass R2's calibration gate, create an unobservable
information path (the Auditor cannot see it), and duplicate calibration logic that belongs
to R2.

Contains:

**Executor** — executes the sub-task using available tools. Its only obligation is to
produce a result and report its status honestly, including infeasibility.

**Agent-Validator** — closes the gap between the Executor's output and the sub-task goal.
The retry loop is entirely internal: the metaagent sees neither raw execution results nor
retry counts. The agent speaks upward only when the gap is closed (matched outcome) or
irresolvable (failure outcome). This keeps the metaagent's coordination bandwidth focused
on goal-level decisions, not execution-level noise.

**Dreamer (agent-level)** — a *teardown step*, not a persistent process. Runs once,
immediately before the Effector Agent terminates. Writes one episodic `MemoryEntry` to
Shared Memory capturing what was tried, what worked, and what failed in this execution.
This is the Effector's sole contribution to Shared Memory — it writes on exit, then the
agent instance is gone. The Metaagent Dreamer later consolidates these raw episodic
entries into semantic knowledge asynchronously, between tasks.

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

A single persistent store. All agents benefit from shared history; no agent holds private
persistent memory. Two classes of memory coexist:

- **Episodic** — written by agent-level Dreamers on Effector exit: what happened, verbatim
- **Semantic** — written by the metaagent Dreamer asynchronously: patterns and
  generalizations consolidated from episodic records across tasks

**Access pattern by agent class**:

| Agent | Access | Mechanism |
|---|---|---|
| Metaagent (R2 Planner) | Direct read + calibration | Queries R5 at planning time; applies calibration protocol; derives MUST NOT / SHOULD PREFER constraints |
| Metaagent (R4b Meta-Validator) | Direct write | Writes `MemoryEntry` after task acceptance or failure |
| Metaagent Dreamer | Direct read + write | Consolidates episodic → semantic asynchronously between tasks |
| Effector Agent (R3 Executor, R4a Agent-Validator) | **Indirect only** | Receives memory-informed context via `SubTask.context`; never queries R5 directly |
| Effector Dreamer | Direct write (exit only) | Writes one episodic entry on teardown; then agent terminates |

The Metaagent is the sole authority on what history means. Effector Agents benefit from
memory through the context R2 injects — they never interpret memory themselves. This
keeps calibration logic in one place, all memory access observable through the bus, and
eliminates the reconciliation problem that private per-agent memories would create.

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

One persistent store rather than per-agent private memories. The design choice follows
directly from the ephemerality of Effector Agents: a private memory for an agent that
is spawned fresh per sub-task and terminated on completion is meaningless — there is no
persistent identity to accumulate knowledge into. Even if you persisted per-agent-type
memory (all Executors share one private store), you have reinvented a filtered view of
shared memory with worse calibration and no Dreamer to organize it.

Centralized memory also prevents the O(n²) reconciliation problem: if N agents hold
private memories that can diverge, aligning them requires N² comparisons and an
arbitration mechanism. One shared store with one Dreamer for reorganization avoids this
entirely. All learning compounds in one place; the Metaagent is the sole authority on
what it means.

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

**Implementation model — TextGrad backward pass**: the GGS is inspired by TextGrad
(automatic differentiation through text). Instead of numerical gradients, it computes a
*textual gradient*: a structured description of what in the plan should change and in
what direction, derived by propagating the gap signal backward through the execution trace.

Three properties the GGS must preserve from this model:

1. **Directional**: the gradient names *what* to change and *in what direction* — not merely
   that something failed. "The plan assumed local file access but the target is network-mounted;
   revise subtask 2 to use a network-aware tool" is a gradient. "Subtask 2 failed" is not.

2. **Compositional**: when multiple subtasks fail, their gap signals are aggregated into a
   single gradient for the plan — not N separate replan requests. The Metaagent's omniscience
   makes this possible: it has the full evidence trail for every subtask simultaneously.

3. **History-aware**: the gradient is computed from the full `gap_trajectory` across all
   attempts for all subtasks, not just the latest failure snapshot. A criterion that fails
   identically across all attempts generates a stronger gradient than one that varies —
   indicating a systematic wrong assumption vs. a transient environmental issue.

An implementation that produces a gradient without direction (just failure attribution) is
a replanner. An implementation that ignores trajectory (only reads the last attempt) is a
static replanner. Both lose the property that makes the GGS valuable.

---

## Cost Model

Every architectural decision must be evaluated against two and only two costs. There are
no other costs that matter at the design level.

**Time cost** — latency felt by the user. The dominant driver is the count of *sequential*
LLM calls in the critical path. Parallel calls add token cost but not time cost. The
minimum sequential chain for a single-subtask task is: R1 → R2 (plan) → R3 → R4a → R4b
= 5 calls. Each retry in the fast loop adds 2 sequential calls (R3 + R4a). Each replan
adds 2 more (R2 + R4b). The architecture must minimize sequential depth; parallelism is
always preferred when data dependencies allow.

**Token cost** — API cost and context window pressure. The dominant driver is context size
per call multiplied by the number of parallel calls. N subtasks dispatched in parallel =
N × context tokens simultaneously. Every "inject more context" decision (session history,
memory entries, tool outputs, gap trajectories) has a direct token cost. The architecture
must bound context aggressively: caps on memory entries retrieved, `headTail` truncation
on tool output, trajectory entries limited to what the GGS actually needs.

**The tension**: parallelism reduces time cost but multiplies token cost. More context per
call improves correctness but raises both costs. Every design decision in this system
should be able to answer: *does this add a sequential LLM call, and how many tokens does
it add per call?* If the answer to both is "none", the decision is cost-free. If either
is non-zero, the benefit must justify it.

**Design decisions already shaped by this model**:
- Memory calibration (Steps 1–3) in Go code, no LLM call → zero time cost
- Memory entries capped at 10 → bounded token cost
- Subtask parallelism → time cost fixed regardless of N subtasks
- `headTail(output, 4000)` → token cost bounded per tool result
- Calibration output as pre-formatted constraint text → no extra LLM call for formatting

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

### The Four Laws

Inspired by Asimov's Three Laws of Robotics and the Zeroth Law introduced in *Robots and
Empire*. Borrowed for structure and priority ordering; definitions are precise and narrowed
to avoid Asimov's known failure mode — laws stated vaguely enough to be exploited by
literal interpretation or used to justify paternalism.

Priority is strict: a lower law may never override a higher one.

---

**Law 0 — Never deceive** *(highest priority)*

> The system must never misrepresent what it actually did or achieved.

This sits above all other laws because deception destroys the feedback signal that all
three loops depend on. A system that fabricates success teaches itself the wrong lesson,
corrupts memory, and makes the next task harder. A system that lies about failure
prevents the user from seeking alternatives. Honesty is non-negotiable even when the
honest answer is "I failed."

Practical constraints:
- `merged_output` must reflect actual tool output, not a plausible-sounding inference
- `MemoryEntry` content must reflect what actually happened, not a sanitized version
- If a task is impossible as specified, report that — do not deliver a related easier result
  and claim success (scope integrity)
- `failure_class` in criterion verdicts must be accurately attributed (logical vs.
  environmental) — misattribution is a Law 0 violation, not just a calibration error

---

**Law 1 — Never harm the user's environment without explicit confirmation**

> The system must not take irreversible actions on the user's data or environment
> without the user explicitly authorizing that specific action.

Irreversible actions: file deletion, overwriting existing data, sending messages or email
on the user's behalf, modifying system configuration. Reversible actions (reading files,
running queries, creating new files in temporary locations) do not require confirmation.

The "through inaction" clause from Asimov's Law 1 is intentionally excluded — it is the
source of the paternalism failure mode and is too broad to implement correctly in MVP.

---

**Law 2 — Best effort delivery** *(subject to Laws 0 and 1)*

> The system must pursue the user's goal as hard as possible within the bounds set by
> Laws 0 and 1.

Practical constraints:
- Exhaust the full fast-loop retry budget (R4a: maxRetries) before escalating to replan
- Exhaust the full medium-loop replan budget before abandoning
- Use `failure_class` to route replanning correctly: environmental failures get a different
  approach, not the same approach retried
- Stop when `gap_trend` is worsening for 2 consecutive replans — continuing is not best
  effort, it is resource destruction with no convergence signal

---

**Law 3 — Preserve own functioning capacity** *(subject to Laws 0, 1, and 2)*

> The system must not degrade its own ability to function across tasks.

Practical constraints:
- **Convergence integrity**: stop on 2 consecutive worsening replans; count-based caps
  (maxRetries, maxReplans) are a floor, not a substitute for trajectory-aware stopping
- **Memory integrity**: never write a `MemoryEntry` that misattributes failure cause —
  a wrong procedural lesson poisons future calibration (this is also a Law 0 violation)
- **Cost integrity**: respect the time and token cost model — unbounded context growth or
  unnecessary sequential LLM calls degrade the system's ability to serve future tasks

---

**Implementation status**:

| Law | Constraint | Status |
|---|---|---|
| 0 | Scope integrity (no silent goal substitution) | Spec complete; enforced in R4b prompt |
| 0 | failure_class accurate attribution | Implemented: `classifyEnvironmental` in R4a post-processes LLM verdict; regex-based promotion to "environmental" for unambiguous patterns (permission denied, no such file, [LAW1], connection refused, timeout, etc.) |
| 1 | Confirmation before irreversible actions | Implemented: executor gates rm/rmdir/truncate/shred/dd/mkfs and write_file-overwrite; [LAW1] prefix propagates to FinalResult summary |
| 2 | Retry + replan budget exhaustion | Implemented (maxRetries=2, maxReplans=3) |
| 2 | failure_class-aware replanning routing | Implemented: `planDirectivePrompt` includes failure_class guidance block (environmental → change path; logical → change tool class; mixed → fix environmental first) |
| 2 | Convergence kill-switch (2× worsening → abort) | Implemented: GGS worseningCount map; forces abandon after 2 consecutive worsening gradients |
| 3 | Memory integrity enforcement | Implemented: procedural MemoryEntry derives failure_class from CriteriaVerdicts; tagged failure_class:<value> for R2 memory queries |
| 3 | Cost model compliance | Implemented: per-result headTail(4000) + accumulated context headTail(8000); per-role token+time tracking printed as `📊 Cost` after each task |
