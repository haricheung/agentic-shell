# artoo — Agentic Shell

A multi-agent AI system that takes natural language goals and coordinates a hierarchy of LLM-based agents to achieve them — with structured feedback loops, gradient-directed replanning, and cross-task memory.

```
> find my Python cache directories and clean them up
> search for the latest Go 1.24 release notes and summarise
> convert ~/Downloads/lecture.mp4 to audio only
```

---

## How it works

Seven roles communicate through an observable message bus. No role calls another directly.

```
User input
  └─ R1 Perceiver       — translates input into a structured task spec
       └─ R2 Planner    — decomposes goal into subtasks; calibrates from memory
            └─ R3 Executor × N   — executes one subtask via tool calls
                 ↕ CorrectionSignal
            └─ R4a AgentValidator — per-criterion fast correction loop
                 └─ R4b MetaValidator  — merges outcomes; gates on all passing
                      └─ R7 GGS        — gradient-directed replanning controller
R5 Memory   — file-backed episodic + procedural store
R6 Auditor  — read-only bus tap; reports anomalies to operator
```

**The medium loop** is a complete closed-loop control system: R4b (sensor) → R7 GGS (controller) → R2 (actuator). When subtasks fail, GGS computes a loss gradient from the failure signal, selects a directive (`refine` / `change_path` / `change_approach` / `break_symmetry` / `abandon`), and sends a structured `PlanDirective` to R2 — telling it not just *that* replanning is needed but *what kind* of change to make and *which specific targets already failed*.

**Memory** accumulates across tasks: procedural entries record what went wrong; episodic entries record what worked. R2 calibrates its next plan against both, with code-enforced MUST NOT constraints derived from past failures.

---

## Features

- **Natural language interface** — REPL and one-shot modes; multi-line input with `"""`
- **Parallel subtask dispatch** — subtasks with the same sequence number run concurrently
- **Gradient-directed replanning** — loss function L = α·D + β·P + λ·Ω; finite-difference gradient ∇L drives directive selection
- **Environmental vs logical failure classification** — different replanning strategies for blocked paths vs wrong algorithms
- **Blocked targets accumulation** — specific failed search queries, commands, and paths carried forward across replan rounds so R2 never re-tries already-blocked inputs
- **Cross-task memory** — episodic (successes) + procedural (failures) written to `~/.artoo/memory.json`; calibrated at every plan
- **Structured task logs** — per-task JSONL with full LLM prompts, tool calls, criterion verdicts, corrections, replans (`~/.artoo/tasks/<id>.jsonl`)
- **Per-role cost reporting** — tokens, LLM time, and tool execution time printed after every task
- **Live pipeline visualiser** — sci-fi terminal UI showing inter-role message flow with loss metrics (D / ∇L / Ω)
- **Abort without exit** — Ctrl+C cancels the current task, never the process
- **Safety gate** — file-destructive commands (`rm`, `find -delete`, `rm` in loops/xargs) require confirmation; generated files land in `~/artoo_workspace/` not the project root

---

## Tools available to the executor

| Tool | When to use |
|---|---|
| `mdfind` | Personal file search — macOS Spotlight, < 100 ms |
| `glob` | Project file search — pattern matched against filename |
| `read_file` | Read a single file |
| `write_file` | Write a file |
| `shell` | General bash — counting, aggregation, ffmpeg, etc. |
| `applescript` | Control macOS apps (Mail, Calendar, Reminders, Music…) |
| `shortcuts` | Run a named Apple Shortcut |
| `search` | Web search via LangSearch API (opt-in; requires `LANGSEARCH_API_KEY`) |

---

## Requirements

- Go 1.22+
- macOS (uses Spotlight `mdfind`; other tools are cross-platform)
- An OpenAI-compatible LLM API endpoint

---

## Setup

```bash
git clone https://github.com/<you>/agentic-shell
cd agentic-shell
go build ./...
```

Copy one of the provided env files and fill in your credentials:

```bash
cp .env.example .env   # or .env.ds for DeepSeek
```

The minimum required variables:

```bash
OPENAI_API_KEY="..."
OPENAI_BASE_URL="..."   # any OpenAI-compatible endpoint
OPENAI_MODEL="..."
```

**Optional: two-tier model split** — use a reasoning model for planning/validation and a faster model for execution:

```bash
# Reasoning tier: R1 Perceiver, R2 Planner, R4b MetaValidator
BRAIN_API_KEY="..."
BRAIN_BASE_URL="..."
BRAIN_MODEL="deepseek-reasoner"

# Execution tier: R3 Executor, R4a AgentValidator
TOOL_MODEL="..."
```

**Optional: web search**

```bash
LANGSEARCH_API_KEY="..."   # https://langsearch.com — when unset, search tool is absent
```

**Optional: custom data directory**

```bash
ARTOO_DATA_DIR="/path/to/data"   # defaults to ~/.artoo/
ARTOO_WORKSPACE="/path/to/ws"    # defaults to ~/artoo_workspace/
```

---

## Usage

```bash
# REPL mode
go run ./cmd/artoo

# One-shot mode
go run ./cmd/artoo "find my largest video files in Downloads"

# Multi-line input in REPL
> """
... find all Python residual directories
... (~/.local/lib/python*, ~/.pyenv/versions/*)
... and remove them after confirmation
... """

# On-demand audit report
> /audit
```

### Data files

| Path | Contents |
|---|---|
| `~/.artoo/memory.json` | Episodic + procedural memory across sessions |
| `~/.artoo/audit.jsonl` | Structured audit events |
| `~/.artoo/tasks/<id>.jsonl` | Per-task log: LLM prompts, tool calls, verdicts, replans |
| `~/.artoo/debug.log` | Internal role debug logs |
| `~/artoo_workspace/` | Files generated by the executor land here |

Watch debug output live:
```bash
tail -f ~/.artoo/debug.log
```

---

## Architecture

The system is built on four convictions:

1. **Hierarchy over mesh** — coordination through structure, not peer-to-peer negotiation
2. **Validation as the primary control mechanism** — not retry policies, not timeouts
3. **Memory that learns** — results are consolidated across tasks, not discarded
4. **Independent observability** — the Auditor sits outside the operational hierarchy and cannot be instructed or suppressed by any agent

Full design rationale: [`ARCHITECTURE.md`](ARCHITECTURE.md)
Role specifications (v0.7): [`docs/mvp-roles-v0.7.md`](docs/mvp-roles-v0.7.md)
Bug log: [`docs/issues.md`](docs/issues.md)

---

## Project structure

```
cmd/artoo/         — entry point: REPL, one-shot, wiring
internal/
  bus/             — observable message bus
  llm/             — OpenAI-compatible LLM client
  roles/
    perceiver/     — R1: input → TaskSpec
    planner/       — R2: TaskSpec → SubTasks; memory calibration
    executor/      — R3: SubTask → ExecutionResult via tool calls
    agentval/      — R4a: per-criterion fast correction loop
    metaval/       — R4b: fan-in gate; replan or accept
    ggs/           — R7: loss function, gradient, PlanDirective
    memory/        — R5: file-backed JSON store
    auditor/       — R6: bus tap; periodic + on-demand reports
  tasklog/         — per-task JSONL structured logging
  tools/           — mdfind, glob, shell, applescript, search, …
  types/           — shared message and data types
  ui/              — terminal pipeline visualiser
docs/
  mvp-roles-v0.7.md   — current role spec
  issues.md            — bug log
```

---

## License

MIT
