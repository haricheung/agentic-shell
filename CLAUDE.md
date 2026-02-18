# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
go build ./...                                      # build all packages
go run ./cmd/agsh                                   # REPL mode
go run ./cmd/agsh "list all go files"               # one-shot mode
```

Cache / log files written to `~/.cache/agsh/`:

| File | Contents |
|---|---|
| `memory.json` | Persistent episodic + procedural memory |
| `audit.jsonl` | Structured audit events |
| `debug.log` | Internal role debug logs (redirected from stderr at startup) |

To watch debug output live: `tail -f ~/.cache/agsh/debug.log`

## Environment Configuration

Copy one of the pre-configured env files to `.env` before running:

- `.env` — Volcengine/Ark endpoint (`ark.cn-beijing.volces.com`)
- `.env.ds` — DeepSeek API (`api.deepseek.com`)

Both use the OpenAI-compatible convention: `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`.

## Architecture

Seven roles communicate exclusively via an observable message bus. No role calls another directly.

```
User input
  └─ R1 Perceiver       cmd/agsh/main.go → perceiver/
       └─ R2 Planner    planner/          (queries R5 before planning)
            └─ R3 Executor (N goroutines, one per subtask)
                 ↕ CorrectionSignal (fast loop)
            └─ R4a AgentValidator (paired with each Executor)
                 └─ R4b MetaValidator   metaval/   (fan-in; replan or accept)
                      └─ R5 Memory      memory/    (file-backed JSON)
R6 Auditor  auditor/   (read-only bus tap, JSONL log)
```

**Message bus** (`internal/bus/`): every inter-role message passes through it. Multiple consumers
can register independent tap channels via `bus.NewTap()` (Auditor and UI each hold one). Publish
is non-blocking — slow subscribers drop messages with a log warning.

**Subtask dispatcher** (`cmd/agsh/main.go:runSubtaskDispatcher`): bridges the bus to per-subtask
goroutines. Each SubTask spawns a paired `Executor + AgentValidator`; `ExecutionResult` bus
messages are routed by subtask ID to the correct AgentValidator channel.

**Correction dual-publish**: `CorrectionSignal` is published to the bus (for Auditor observability)
AND sent via a direct channel (for routing to the paired Executor). Both are required.

## Key Files

| Path | Role | Notes |
|---|---|---|
| `cmd/agsh/main.go` | Entry point | REPL + one-shot; wires all roles; session history |
| `internal/types/types.go` | Shared schemas | All message and data types |
| `internal/bus/bus.go` | Message bus | Foundation; all roles depend on this |
| `internal/llm/client.go` | LLM client | Single `Chat(ctx, system, user)` method; `StripFences()` helper |
| `internal/roles/perceiver/` | R1 | Translates input → TaskSpec; uses session history for follow-up context |
| `internal/roles/planner/` | R2 | TaskSpec → SubTask[]; queries memory first; handles ReplanRequest |
| `internal/roles/executor/` | R3 | Executes one SubTask via tool loop; correction-aware |
| `internal/roles/agentval/` | R4a | Scores ExecutionResult; drives retry loop; maxRetries=2 |
| `internal/roles/metaval/` | R4b | Fan-in; merges outcomes; accept or replan |
| `internal/roles/memory/` | R5 | File-backed JSON; keyword query; drains on shutdown |
| `internal/roles/auditor/` | R6 | Bus tap; JSONL audit log; boundary + convergence checks |
| `internal/ui/display.go` | Terminal UI | Sci-fi pipeline visualizer; reads its own bus tap; `Abort()` sets `suppressed=true` to block stale post-abort messages; `Resume()` lifts it before each new task |
| `internal/tools/mdfind.go` | Tool | macOS Spotlight wrapper; `RunMdfind(ctx, query)` → `mdfind -name <query>` |

## Tools Available to Executor

| Tool | Input fields | When to use |
|---|---|---|
| `mdfind` | `query` | **Personal file search** — macOS Spotlight index, < 100 ms. Always use for user files (Downloads, Documents, Music, etc.) |
| `glob` | `pattern`, `root` | **Project file search** — `root:"."` only; pattern matches filename, not full path; `**/` prefix stripped automatically |
| `read_file` | `path` | Read a single file |
| `write_file` | `path`, `content` | Write a file |
| `applescript` | `script` | Control macOS apps (Mail, Calendar, Reminders, Messages, Music…); Calendar/Reminders sync to iPhone/iPad/Watch via iCloud |
| `shortcuts` | `name`, `input` | Run a named Apple Shortcut (iCloud-synced; can trigger iPhone/Watch automations) |
| `shell` | `command` | General bash; counting/aggregation (`wc -l`), not file discovery |
| `search` | `query` | DuckDuckGo instant answer (no API key) |

**File search hierarchy**: `mdfind` for anything outside the project (user personal files) → `glob` for project files → `shell` only for operations neither handles.

`normalizeFindCmd()` in `executor.go:runTool` strips `-maxdepth N` and appends `2>/dev/null` to any `shell find` command as a safety net for model non-compliance.

**`glob` pattern notes**: pattern is matched against the filename only (`filepath.Match(pattern, d.Name())`). Globstar prefixes like `**/*.go` are automatically stripped to `*.go` before matching. Do not include `/` in patterns.

## Known Model Behaviour (Volcengine/DeepSeek)

- Tends to return `status: "uncertain"` even with clear tool output → mitigated by "commit to completed" instruction appended after tool results
- Follows JSON output format reliably when given concrete examples
- May still use `find ~` via shell for personal file searches despite `mdfind` guidance → `normalizeFindCmd()` and repeated prompt reinforcement mitigate this

## Memory System

- **Episodic entries** (`type: "episodic"`) written by R4b on task acceptance; contain merged output and intent-derived tags
- **Procedural entries** (`type: "procedural"`) written by R4b on replan; contain gap summary and failure lesson
- Query uses keyword scan against serialised entry JSON — passes `MemoryQuery.Query` (natural language intent)
- **Race condition fixed**: one-shot mode calls `cancel()` + `time.Sleep(200ms)` after task completes so memory goroutine drains pending writes before exit

## REPL Session Context

Each REPL turn records `{input, result.Summary}` in a rolling 5-entry history.
`buildSessionContext()` formats it and passes it to the Perceiver so follow-up inputs
("wrong", "bullshit", "do it again", pronouns) resolve against prior context.

`printResult` detects string output (via marshal+unmarshal roundtrip) and prints it with
`fmt.Println` so `\n` renders as real newlines. Structured output (object/array) falls back
to `json.MarshalIndent`. Output is suppressed when it duplicates the summary.

## Abort Handling

Ctrl+C in REPL aborts only the current task, never the process:
1. Signal handler calls `taskCancel()` (per-task context) + sends `taskID` to `abortTaskCh`.
2. Dispatcher calls `entry.cancel()` for that task's executor/agentval goroutines.
3. Executor checks `ctx.Err()` before every `bus.Publish()` — cancelled contexts skip publish entirely, preventing stale `ExecutionResult` messages from reaching the bus.
4. `disp.Abort()` closes the pipeline box and sets `suppressed=true`; stale in-flight messages are drained silently.
5. `disp.Resume()` is called at the top of the next user task to re-enable the pipeline box.

## Design Documents

| File | Description |
|---|---|
| `ARCHITECTURE.md` | Full system architecture, philosophy, data flow, risk register |
| `docs/mvp-roles-v0.5.md` | Role definitions v0.5 — current canonical spec |
| `docs/issues.md` | Bug log: all issues found in first test session, root causes, fix sequences |
