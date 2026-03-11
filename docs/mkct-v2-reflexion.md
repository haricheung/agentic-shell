# MKCT v2: Reflexion-Quality Memory — Design Spec

**Problem**: Megram content stores execution traces (`"Succeeded. Tools: mdfind:foo. Result: ..."`),
not knowledge. The Dreamer faithfully promotes garbage to timeless garbage. Three root causes:

1. **Content is useless** — tool logs, not insights. Even keyword matching fails.
2. **Retrieval is siloed** — query key `(intent:<task_id>, env:local)` means cross-cutting facts
   (persona, preferences, environment) are invisible to unrelated tasks.
3. **Promotion is too slow** — λ_att=5.0 requires ~6 identical-space Megrams before C-level.
   A user-declared fact shouldn't need 6 repetitions to become durable.

**Inspiration**: Reflexion (Shinn et al., 2023) — the reflection *is* the memory. Not the
trajectory, not the tool log — the insight. Artoo already has the structural advantage
(typed levels, decay, trust bankruptcy). What's missing is content quality.

---

## 1. Reflexion at Write Time (Content Quality)

### Current flow
```
GGS terminal state → buildTerminalContent() → "Succeeded. Tools: X. Result: Y"
                                                ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                                                execution trace, not knowledge
```

### Proposed flow
```
GGS terminal state → reflectOnTrajectory(LLM) → structured Insight
                                                  ^^^^^^^^^^^^^^^^
                                                  what was learned
```

### 1.1 The `Insight` struct

Replace free-text `Content` with a structured reflection. The Megram tuple stays unchanged
(Content remains a string field), but the string now contains a JSON-encoded Insight:

```go
type Insight struct {
    // What was learned — the reusable takeaway (Reflexion-style).
    // Success: "User's name preference is Artoo; stored in identity.json"
    // Failure: "mdfind cannot search inside .zip archives; need unzip first"
    Lesson string `json:"lesson"`

    // Semantic tags for cross-cutting retrieval (see §2).
    // Extracted by LLM during reflection. Examples:
    //   ["persona", "identity", "user-preference"]
    //   ["file-search", "mdfind", "spotlight", "cjk"]
    Tags []string `json:"tags"`

    // Category classifies the knowledge type for routing.
    //   "fact"        — declarative (persona, preferences, environment state)
    //   "procedure"   — how to do X (tool patterns, command sequences)
    //   "constraint"  — what NOT to do (anti-patterns, dead ends)
    Category string `json:"category"` // "fact" | "procedure" | "constraint"
}
```

### 1.2 The reflection LLM call

`reflectOnTrajectory` replaces `buildTerminalContent`. Called once per task completion
in GGS, same position as today. Uses tool-model (cheap, fast).

**Input to LLM:**
```
You are distilling a task trajectory into a reusable memory entry.

Task intent: {intent}
Outcome: {accept|success|abandon|change_approach|...}
Tool calls (with outputs):
  1. mdfind: {"query":"identity.json"} → /Users/.../identity.json
  2. read_file: {"path":"..."} → {"name":"Artoo","role":"dedicated AI Assistant"}
  3. ...
Summary: {metaval summary}
Gap (if failed): {gap_summary}

Produce a JSON object with exactly three fields:
- "lesson": One sentence. What reusable knowledge was gained? For success: what worked
  and why. For failure: what went wrong and what should be tried differently.
  Be specific — include concrete values (names, paths, commands) not vague summaries.
- "tags": 2-5 lowercase semantic keywords for retrieval. Include the domain
  (e.g. "file-search"), the specific entities (e.g. "mdfind", "spotlight"),
  and any cross-cutting concerns (e.g. "persona", "user-preference", "cjk").
- "category": One of "fact", "procedure", or "constraint".
  "fact" = declarative knowledge (X is Y).
  "procedure" = how to do X (use tool A then B).
  "constraint" = what not to do (never X because Y).

Output ONLY the JSON object.
```

**Example outputs:**

Success (persona):
```json
{
  "lesson": "User's AI assistant name is 'Artoo' with role 'dedicated AI Assistant', persisted at ~/artoo_workspace/memory/identity.json",
  "tags": ["persona", "identity", "user-preference", "artoo"],
  "category": "fact"
}
```

Failure (tool limitation):
```json
{
  "lesson": "mdfind -name cannot find CJK-named files with extensions; must search stem only then post-filter by extension",
  "tags": ["mdfind", "spotlight", "cjk", "file-search", "extension"],
  "category": "constraint"
}
```

Success (procedure):
```json
{
  "lesson": "To download videos from bilibili, use yt-dlp with --cookies-from-browser safari flag; direct curl fails due to anti-hotlink headers",
  "tags": ["download", "video", "bilibili", "yt-dlp", "cookies"],
  "category": "procedure"
}
```

### 1.3 Cost analysis

One tool-model LLM call per task completion. Current system already calls LLM 4-6 times
per task (R1, R2, R3, R4a, R4b, GGS). Adding one more (~100 input tokens, ~80 output tokens)
is <5% overhead. This replaces `buildTerminalContent()` which is zero-cost but produces
zero-value content.

### 1.4 Fallback

If the reflection LLM call fails (timeout, quota), fall back to the current
`buildTerminalContent()` format. The system never blocks on memory writes.

---

## 2. Semantic Tag Index (Retrievability)

### Current retrieval
```
Planner → queryMKCTConstraints("intent:update_identity", "env:local")
                                ^^^^^^^^^^^^^^^^^^^^^^^^
                                exact match only — misses cross-cutting facts
```

### Problem
A weather task queries `(intent:weather_check, env:local)`. The persona Megram lives at
`(intent:update_identity, env:local)`. No match. No recall.

### 2.1 New index: tag-based inverted index

Add a new LevelDB key prefix for tag-based lookup:

```
t|<tag>|<id>  → nil    (tag inverted index)
```

When a Megram with an Insight is persisted, write one `t|` key per tag:
```
t|persona|62267c1f     → nil
t|identity|62267c1f    → nil
t|user-preference|62267c1f → nil
t|artoo|62267c1f       → nil
```

### 2.2 `QueryByTags` — new MemoryService method

```go
// QueryByTags returns Megrams matching ANY of the given tags, scored by
// relevance (number of matching tags × live attention potential).
// Used for cross-cutting retrieval where (space, entity) is too narrow.
QueryByTags(ctx context.Context, tags []string, limit int) ([]Megram, error)
```

Implementation: for each tag, prefix-scan `t|<tag>|`, collect Megram IDs, count
matches per ID, fetch top-N by (match_count × attention_decay), return.

### 2.3 Tag extraction for queries

The Planner needs tags to query. Two sources:

**Source A: TaskSpec tags** — R1 (Perceiver) already produces `task_id` and `intent`.
Add a `Tags []string` field to TaskSpec. R1 extracts 2-5 semantic keywords from the
user input. Example: "Your name is Artoo" → `["persona", "identity", "name", "artoo"]`.

**Source B: Static global tags** — The Planner always queries a set of global tags
on every task: `["persona", "user-preference", "environment"]`. This ensures
cross-cutting facts are always recalled regardless of task intent.

### 2.4 Updated query flow

```go
func (p *Planner) queryMKCTConstraints(ctx context.Context, taskID string, tags []string, tl *tasklog.TaskLog) string {
    space := "intent:" + taskID
    entity := "env:local"

    // Layer 0 (NEW) — cross-cutting facts via tag index
    globalTags := []string{"persona", "user-preference", "environment"}
    allTags := append(globalTags, tags...)
    tagResults, _ := p.mem.QueryByTags(ctx, allTags, 5)

    // Layer 1 — C-level SOPs (unchanged)
    sops, _ := p.mem.QueryC(ctx, space, entity)

    // Layer 2 — Recent M/K (unchanged)
    recent, _ := p.mem.QueryRecent(ctx, space, entity, 3)

    // Layer 3 — Potentials (unchanged)
    pots, _ := p.mem.QueryMK(ctx, space, entity)

    return calibrateMKCT(sops, pots, recent, tagResults)
}
```

Layer 0 is injected as a new block in the planning prompt:

```
CONTEXT (cross-cutting knowledge):
  - [fact] User's AI assistant name is 'Artoo' with role 'dedicated AI Assistant'
  - [fact] User prefers Chinese for casual conversation, English for technical
```

This block appears on EVERY task, regardless of task_id match.

---

## 3. Acceleration (Promotion/Demotion Velocity)

### Current thresholds
- **Upward**: λ_att=5.0 (~6 Megrams at f=0.9 before any decay)
- **Downward (Trust Bankruptcy)**: M_decision < 0.0

### Problem
- A persona fact needs 6 identity tasks to become C-level. Absurd.
- A user correction ("no, use yarn not npm") is a single event but needs
  to immediately override a C-level "use npm" SOP.

### 3.1 Category-aware promotion thresholds

Facts and constraints promote faster than procedures (procedures need more evidence):

| Category | λ_att | λ_dec | Rationale |
|---|---|---|---|
| `fact` | **1.5** | **1.0** | Facts are stated, not discovered. 2 confirmations suffice. |
| `procedure` | **5.0** | **3.0** | Procedures need repeated success to trust. (Unchanged.) |
| `constraint` | **3.0** | **−2.0** | Constraints need moderate evidence. Negative σ accumulates faster. |

This means a persona fact (f=0.9, σ=+1.0) promotes to C-level after just **2 successful
identity tasks** (1.8 ≥ 1.5 attention, 1.8 ≥ 1.0 decision).

### 3.2 Instant promotion for user-declared facts

When R1 detects a declarative user statement ("my name is X", "always use Y",
"remember that Z"), it sets a flag `TaskSpec.Declarative = true`. GGS checks
this flag: if the task succeeds AND the Insight category is `"fact"`, write
directly at **C-level** (k=0.0) instead of M-level. The user IS the authority.

```go
// In GGS processAccept:
if spec.Declarative && insight.Category == "fact" {
    meg.Level = "C"
    meg.K = 0.0
    meg.F = 1.0
    meg.Sigma = +1.0
}
```

No Dreamer cycle needed. No threshold. The user said it, it's law.

### 3.3 Instant demotion on contradiction

When a new M-level Megram contradicts an existing C-level SOP (same tags, opposite σ),
the Dreamer triggers an **immediate trust bankruptcy check** for that SOP instead of
waiting for the next periodic cycle.

Implementation: after `reflectOnTrajectory`, if the Insight contains tags that match
any C-level entry AND the new σ is opposite to the SOP's σ, call
`RecordNegativeFeedback` synchronously (already exists).

### 3.4 Tuned Dreamer cycle

Current: 5-minute periodic + post-task debounce.

Add: **tag-triggered consolidation**. When a new Megram is written, check if its
(space, entity) group NOW exceeds the promotion threshold. If so, run consolidation
for that single group immediately instead of waiting for the full scan. This is O(1)
per write instead of O(N) full scan.

---

## 4. R1 Cross-Session Context (Episodic Continuity)

### Current architecture
```
REPL session
  └─ volatile history[] (5 entries, in-memory, gone on restart)
       └─ buildSessionContext() → flat string
            └─ R1 Perceiver (only context source)

R2 Planner ← MKCT queries (persistent, cross-session)
R1 Perceiver ← session history only (volatile, current session only)
```

### Problem
A human remembers yesterday's conversation. Artoo doesn't. The volatile 5-entry
session history dies on restart. When the user says "用中文回答" in a new session,
R1 has zero context about what was previously discussed.

Even within a session, the history is limited: `firstN(summary, 300)` truncates
results, and only 5 turns are kept. A task from 6 turns ago is forgotten.

### Root cause
**R1 has no access to persistent memory.** Only R2 queries MKCT. But R1 is where
conversational context matters most — it's the role that must resolve "用中文回答"
into "redo the Hefei weather task in Chinese."

### 4.1 Give R1 access to MemoryService

Add `mem types.MemoryService` to the Perceiver struct. R1 queries memory before
calling the LLM, same as R2 does.

```go
type Perceiver struct {
    llm     *llm.Client
    b       *bus.Bus
    mem     types.MemoryService  // NEW
    clarify func(question string) (string, error)
}
```

### 4.2 `QueryRecentGlobal` — new MemoryService method

```go
// QueryRecentGlobal returns the N most recent Megrams across ALL spaces,
// sorted newest-first. Used by R1 for cross-session conversational context.
// Unlike QueryRecent, this is not scoped to a (space, entity) pair.
QueryRecentGlobal(ctx context.Context, n int) ([]Megram, error)
```

Implementation: scan all M/K-level Megrams by `created_at` descending, return top N.
This is a full scan but N is small (5-10) and the Dreamer's GC keeps the store lean.

Optimization: maintain a `c|<timestamp>|<id>` chronological index key on write.
Reverse iteration on this prefix gives newest-first without scanning all entries.

### 4.3 R1 context assembly

Replace `buildSessionContext` with MKCT-backed context:

```go
func (p *Perceiver) perceive(ctx context.Context, input, sessionContext string) (...) {
    // Cross-session context from MKCT (persistent)
    var mkcContext string
    if p.mem != nil {
        recent, _ := p.mem.QueryRecentGlobal(ctx, 5)
        mkcContext = formatRecentForR1(recent)
    }

    // Merge: MKCT context (cross-session) + volatile session context (current session)
    // Volatile session history has higher priority (more recent, richer detail)
    userPrompt := input
    if mkcContext != "" || sessionContext != "" {
        userPrompt = ""
        if mkcContext != "" {
            userPrompt += "Recent task history (cross-session):\n" + mkcContext + "\n\n"
        }
        if sessionContext != "" {
            userPrompt += "Current session history:\n" + sessionContext + "\n\n"
        }
        userPrompt += "New input: " + input
    }
    ...
}
```

`formatRecentForR1` extracts the lesson from each Megram's Insight:

```go
func formatRecentForR1(megrams []types.Megram) string {
    var sb strings.Builder
    for i, m := range megrams {
        ins, ok := ParseInsight(m.Content)
        if ok {
            fmt.Fprintf(&sb, "[%d] %s — %s\n", i+1, m.Space, ins.Lesson)
        } else {
            // Legacy Megram: use raw content (truncated)
            fmt.Fprintf(&sb, "[%d] %s — %s\n", i+1, m.Space, firstN(m.Content, 200))
        }
    }
    return sb.String()
}
```

Example context R1 sees:
```
Recent task history (cross-session):
[1] intent:hefei_weather_pm25_today — Hefei weather: Partly Cloudy 11°C, PM2.5 80.57 µg/m³ (Unhealthy AQI 165)
[2] intent:update_identity — User's AI assistant name is 'Artoo' with role 'dedicated AI Assistant'
[3] intent:download_video — yt-dlp with --cookies-from-browser safari works for bilibili videos

Current session history:
[1] User: 合肥今天空气怎么样
    Result: Hefei Weather & Air Quality (2026-03-11) Partly Cloudy 11°C PM2.5 80.57...

New input: 用中文回答
```

Now R1 can resolve "用中文回答" even if the weather task was yesterday, in a different
session. The Insight content ("Hefei weather: Partly Cloudy 11°C...") carries the actual
knowledge, not a tool trace.

### 4.4 Volatile history becomes optional supplement

`buildSessionContext` remains for current-session detail (richer than Megram summaries —
includes raw user input and full result text). But it's no longer the sole context
source. On a fresh session start, R1 still has cross-session memory from MKCT.

Over time, as Insight quality improves, volatile session history may become redundant
entirely — MKCT would be the single source of conversational continuity.

### 4.5 Chronological index key

To avoid full-scanning all Megrams for `QueryRecentGlobal`, add a chronological
index key on every write:

```
c|<rfc3339-timestamp>|<id>  → nil
```

Reverse iteration on prefix `c|` yields newest-first. The Dreamer's GC pass
deletes the corresponding `c|` key when garbage-collecting a Megram.

---

## 5. Migration & Backward Compatibility

### 5.1 Content field

The `Content` field remains a string. Old Megrams have free-text content.
New Megrams have JSON-encoded `Insight`. The reader checks if Content starts
with `{` — if so, parse as Insight; otherwise, treat as legacy free-text.

```go
func ParseInsight(content string) (Insight, bool) {
    if !strings.HasPrefix(strings.TrimSpace(content), "{") {
        return Insight{}, false
    }
    var ins Insight
    if err := json.Unmarshal([]byte(content), &ins); err != nil {
        return Insight{}, false
    }
    return ins, true
}
```

### 5.2 Tag index backfill

On startup, if any M/K/C Megrams lack `t|` index entries, the Dreamer's first
cycle scans all Megrams and writes tag index keys for those with parseable Insights.
Legacy Megrams without Insights are left as-is (they'll be GC'd naturally by decay).

### 5.3 TaskSpec.Tags

Add `Tags []string` to TaskSpec. R1 prompt updated to extract tags. Old R1 output
without tags is handled by defaulting to `[]string{}` (only global tags queried).

---

## 6. Summary of Changes

| Component | Current | Proposed | Effort |
|---|---|---|---|
| `ggs.go: buildTerminalContent` | Tool trace string | `reflectOnTrajectory` LLM call → Insight JSON | Medium |
| `ggs.go: writeTerminalMegram` | Always Level="M" | Level="C" for declarative facts | Small |
| `types.go: Megram` | Content=free text | Content=Insight JSON (backward-compatible) | Small |
| `types.go: TaskSpec` | No tags | `Tags []string` + `Declarative bool` | Small |
| `memory.go: persistMegram` | Index on (space, entity) | Also index on Insight.Tags + chronological `c\|` key | Small |
| `memory.go: QueryByTags` | Does not exist | New method: tag-based cross-cutting retrieval | Medium |
| `memory.go: QueryRecentGlobal` | Does not exist | New method: newest N Megrams across all spaces | Medium |
| `memory.go: consolidationPass` | Fixed λ_att=5.0 | Category-aware thresholds (1.5/3.0/5.0) | Small |
| `planner.go: queryMKCTConstraints` | (space, entity) only | + QueryByTags for global + task tags | Medium |
| `perceiver/perceiver.go` | No memory access; volatile session history only | Queries MKCT `QueryRecentGlobal` for cross-session context; extracts Tags + Declarative | Medium |
| `cmd/artoo/main.go` | `buildSessionContext` is sole R1 context | MKCT primary, volatile history supplementary | Small |
| `memory.go: distilSOP` | Distils tool traces | Distils Insights (better input quality) | None (input quality improves output) |
| Dreamer: GC pass | Deletes `m\|`, `x\|`, `l\|`, `r\|` keys | Also deletes `t\|` (tag) and `c\|` (chronological) keys | Small |
| Dreamer cycle | Periodic only | + Tag-triggered single-group consolidation | Medium |

### Data flow (new)

```
User input
  └─ R1 Perceiver
       ├─ QueryRecentGlobal(5)                 ← NEW: cross-session episodic context
       ├─ volatile session history              ← supplementary (current session detail)
       └─ → TaskSpec { task_id, intent, Tags, Declarative, ... }
            └─ R2 Planner
                 ├─ QueryByTags(globalTags ∪ Tags)     ← NEW: cross-cutting facts
                 ├─ QueryC(space, entity)                ← unchanged
                 ├─ QueryRecent(space, entity)           ← unchanged (but Insight content now)
                 └─ QueryMK(space, entity)               ← unchanged
                 └─ plan + dispatch
                      └─ R3 Executor → R4a → R4b → GGS
                           └─ reflectOnTrajectory(LLM)  ← NEW: Reflexion-style distillation
                           └─ Write Megram {
                                Content: Insight JSON,
                                Level: "C" if Declarative+fact, else "M",
                                Tags indexed in t|<tag>|<id>,
                                Chronological index c|<ts>|<id>
                              }
                           └─ Tag-triggered consolidation check  ← NEW: immediate if threshold met
```

---

## 7. Design Principles Preserved

- **Fire-and-forget writes**: reflection LLM call is async; falls back to trace on failure
- **Append-only**: no UPDATE; negative feedback still appends reverse-σ Megrams
- **GGS sole writer**: unchanged; tags come from GGS's LLM call, not from other roles
- **Dreamer still runs**: GC, Trust Bankruptcy, and consolidation all unchanged in logic;
  thresholds tuned; tag-triggered consolidation is additive
- **Bus architecture**: no new bus messages; reflection happens inside GGS before Write()
