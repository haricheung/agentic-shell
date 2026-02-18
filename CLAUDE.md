# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
go build ./...                                      # build all packages
go run ./cmd/agsh                                   # REPL mode
go run ./cmd/agsh "list all go files"               # one-shot mode
```

Cache files (memory, audit log) are written to `~/.cache/agsh/`.

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

**Message bus** (`internal/bus/`): every inter-role message passes through it. Auditor gets a
read-only tap. Publish is non-blocking — slow subscribers drop messages with a log warning.

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

## Tools Available to Executor

| Tool | Input fields | When to use |
|---|---|---|
| `glob` | `pattern`, `root` | **Preferred** for file discovery — always recursive, no subprocess |
| `read_file` | `path` | Read a single file |
| `write_file` | `path`, `content` | Write a file |
| `applescript` | `script` | Control macOS apps (Mail, Calendar, Reminders, Messages, Music…); Calendar/Reminders sync to iPhone/iPad/Watch via iCloud |
| `shortcuts` | `name`, `input` | Run a named Apple Shortcut (iCloud-synced; can trigger iPhone/Watch automations) |
| `shell` | `command` | General bash; use for counting/aggregation (`wc -l`), not file discovery |
| `search` | `query` | DuckDuckGo instant answer (no API key) |

**Do not use `shell` for file discovery** — the LLM model reliably adds `-maxdepth 1` to `find`
commands, missing subdirectory files. `normalizeFindCmd()` strips it as a safety net, but `glob`
is the right tool and avoids the problem entirely.

## Known Model Behaviour (Volcengine/DeepSeek)

- Ignores system-prompt instructions to avoid `-maxdepth 1` in `find` → mitigated by `normalizeFindCmd()` in `executor.go:runTool`
- Tends to return `status: "uncertain"` even with clear tool output → mitigated by prompt and "commit to completed" instruction after tool results
- Follows JSON output format reliably when given concrete examples

## Memory System

- **Episodic entries** (`type: "episodic"`) written by R4b on task acceptance; contain merged output and intent-derived tags
- **Procedural entries** (`type: "procedural"`) written by R4b on replan; contain gap summary and failure lesson
- Query uses keyword scan against serialised entry JSON — passes `MemoryQuery.Query` (natural language intent)
- **Race condition fixed**: one-shot mode calls `cancel()` + `time.Sleep(200ms)` after task completes so memory goroutine drains pending writes before exit

## REPL Session Context

Each REPL turn records `{input, result.Summary}` in a rolling 5-entry history.
`buildSessionContext()` formats it and passes it to the Perceiver so follow-up inputs
("wrong", "bullshit", "do it again", pronouns) resolve against prior context.

## Design Documents

| File | Description |
|---|---|
| `ARCHITECTURE.md` | Full system architecture, philosophy, data flow, risk register |
| `docs/mvp-roles-v0.5.md` | Role definitions v0.5 — current canonical spec |
| `docs/issues.md` | Bug log: all issues found in first test session, root causes, fix sequences |
