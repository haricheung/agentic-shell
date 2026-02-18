# Bug Log & Fix History

Bugs discovered and fixed during the first end-to-end test session (2026-02-19).

---

## Issue #1 — Executor returns wrong result: "No .go files found"

**Symptom**
Running `agsh "list all go files in the current directory"` returned:
> "No .go files found in the current working directory, which satisfies all the task's success
> criteria (all .go files included, no non-.go files present…)"

The project clearly has 17 `.go` files. The answer was factually wrong.

**Root causes**
1. The executor had no working-directory context in its prompt. The LLM didn't know where it was running.
2. The LLM model (DeepSeek/Volcengine) consistently ignores system-prompt instructions and emits
   `find . -maxdepth 1 -name '*.go'` — which finds nothing because all project files live in
   subdirectories, not the repo root.
3. The AgentValidator system prompt was too permissive: it accepted an empty/negative result as
   "matched" without requiring positive evidence.

**Fix attempts (in order)**

| # | What was tried | Outcome |
|---|---|---|
| 1 | Add `os.Getwd()` to executor user prompt so LLM knows current dir | LLM still used `-maxdepth 1`, still found nothing |
| 2 | Strengthen executor system prompt: "use `find . -name '*.go'` without `-maxdepth`" | Model ignored the instruction |
| 3 | Strengthen AgentValidator prompt: require positive evidence for "matched" | Validator now sends corrections, but executor repeated the same wrong command |
| 4 | Pass tool-call history across correction rounds so executor can't repeat itself | Executor tried different wrong commands (`ls *.go`, `ls -la`) but still no recursive find |
| 5 | Add `normalizeFindCmd()` in `runTool` to strip `-maxdepth N` from all find commands before execution | **Fixed.** Deterministic code guardrail bypasses model non-compliance |
| 6 | Add `glob` tool (Go `filepath.WalkDir`, always recursive) and make it the preferred tool for file discovery | **Better fix.** Model naturally picks `glob` for file tasks; no shell subprocess, no depth issue |

**Root fix**: Code-level normalisation + a dedicated `glob` tool. Prompt engineering alone is insufficient when the underlying model reliably ignores specific instructions.

---

## Issue #2 — Tasks are too slow (5–7 minutes for a trivial query)

**Symptom**
Counting or listing files took 5–7 minutes. Every simple task generated ~18 LLM calls.

**Root causes**
1. Planner decomposed simple single-step tasks (e.g. "list files") into 2 subtasks, doubling cost.
2. Executor returned `status: "uncertain"` after every tool call instead of committing to `"completed"`,
   triggering AgentValidator correction loops.
3. Correction rounds started fresh (`execute()` was called anew each time), so the executor had no
   memory of what it had already tried and repeated the same wrong command.
4. `maxRetries = 3` allowed up to 4 LLM calls per subtask before failing.

**Fix attempts (in order)**

| # | What was tried | Outcome |
|---|---|---|
| 1 | Update planner prompt: "prefer a single SubTask for simple operations" | Planner now emits 1 subtask for most simple queries |
| 2 | Update executor prompt: "use `completed` after seeing tool output; `uncertain` only when genuinely ambiguous" | Reduced but did not eliminate `uncertain` loops |
| 3 | After tool results, append "You have the output. Output the final result now." to the prompt | Executor committed faster |
| 4 | Pass accumulated tool-call history into correction rounds | Avoids repeating identical commands across retries |
| 5 | Reduce `maxRetries` from 3 → 2 | Saves one correction round per subtask on failure |
| 6 | Add debug logging of each LLM response and tool call | Exposed the `-maxdepth 1` root cause for Issue #1 |

**Root fix**: Issues #1 and #2 are coupled — once the executor used `glob` and got correct output on the first try, the correction loop disappeared entirely. Typical simple task now takes ~20–25 s (perceiver + planner + one executor tool call + agentval + metaval).

---

## Issue #3 — Memory not written for failed tasks

**Symptom**
After repeated failures on "list go files", the memory store had no record of what went wrong.
On the next session the system made the exact same mistakes.

**Root cause**
`MetaValidator.evaluate()` only wrote a `MemoryEntry` on `verdict == "accept"`. The `"replan"` branch
wrote nothing. Failed tasks left no institutional knowledge.

**Fix**
Added a `"procedural"` memory entry in the `"replan"` branch of `evaluate()`:
```json
{
  "type": "procedural",
  "content": {
    "lesson": "Task failed: <gap_summary>. Avoid repeating the same approach.",
    "gap_summary": "...",
    "failed_subtasks": ["..."]
  },
  "tags": ["failure", "replan", "<task_id>", "<keywords from gap_summary>"]
}
```
This gives the Planner context on what failed and why before it replans.

---

## Issue #4 — `MemoryQuery.Query` field silently ignored

**Symptom**
The Planner queried memory with `MemoryQuery{Query: spec.Intent}` (natural language), but the
memory store never used this field. All memory reads returned either everything or nothing,
regardless of the query.

**Root cause**
`Store.Query(taskID, tags string)` filtered by exact `taskID` match and substring `tags` match only.
The `Query` (natural-language) field of `MemoryQuery` was accepted in the struct but never passed
to `Store.Query()`.

**Fix**
- Added `query string` parameter to `Store.Query()`.
- Tokenises the query into words (≥3 chars), serialises each candidate entry to JSON, and checks
  whether any keyword appears in the serialised text.
- Updated `Run` handler to pass `query.Query` through.

---

## Issue #5 — Memory entry lost on one-shot exit

**Symptom**
In one-shot mode (`agsh "some task"`), the success `MemoryEntry` published by MetaValidator was
never persisted to `memory.json`. Subsequent sessions had no record of completed tasks.

**Root cause**
Race condition on shutdown: `MetaValidator` publishes `MsgMemoryWrite` to the bus, then calls
`outputFn()` which unblocks `runTask()`, which returns, causing `main()` to return and the process
to exit. The memory store's goroutine never got CPU time to dequeue and process the message.

**Fix attempts (in order)**

| # | What was tried | Outcome |
|---|---|---|
| 1 | Add `cancel()` call after `runTask` returns in one-shot mode | Memory goroutine received ctx.Done() but exited immediately without draining |
| 2 | Drain pending writes in `memory.Run` on `ctx.Done()` (select-default loop) | Entries now dequeued, but process had already exited |
| 3 | Both: `cancel()` + drain in Run + `time.Sleep(200ms)` in main | **Fixed.** 200 ms gives the scheduler time to run the memory goroutine's drain loop |

**Root fix**: Graceful shutdown — `memory.Run` drains its write channel on context cancellation, and the main goroutine waits briefly before exiting.

---

## Issue #6 — Memory tags are useless for retrieval

**Symptom**
All memory entries had tags `["task", "<taskID>"]`. The Planner could only retrieve entries for
the exact same `taskID`, making cross-task learning impossible (e.g. a lesson learned from
`list_go_files` could never be found when planning `count_go_lines`).

**Root cause**
Tags were hardcoded in `metaval.go` as `[]string{"task", taskID}` regardless of content.

**Fix**
Tags now extracted from the task `intent` field: split on whitespace, lowercase, strip punctuation,
keep words ≥4 chars. The `"accept"` entry gets `["success", taskID, <intent keywords>...]` and the
`"replan"` entry gets `["failure", "replan", taskID, <gap_summary keywords>...]`.

---

## Issue #7 — REPL has no session context; follow-up inputs fail

**Symptom** (screenshot)
After the program returned a wrong line count, the user typed "bushit". The Perceiver responded:
> "I'm not sure what you're asking for. Could you please rephrase or clarify your request?"

It had no knowledge of the previous turn and treated "bushit" as a standalone, meaningless command.

**Root cause**
Each REPL turn called `perceiver.New(...).Process(ctx, input)` with only the raw new input.
No session history was maintained or passed.

**Fix**
- `runREPL` maintains a `[]sessionEntry` (last 5 turns) recording each input and its result summary.
- `buildSessionContext()` formats history as numbered turns and passes it to `perceiver.Process()`.
- `Process()` now accepts a `sessionContext string` and prepends it to the LLM user prompt.
- Perceiver system prompt updated with explicit session history rules:
  - Reactions ("wrong", "bullshit", "no") → redo previous task with better criteria
  - Pronouns ("it", "that") → resolve to most recent task
  - Short reactive inputs → **never** trigger a clarification question

**Verified**
```
agsh> count go lines for this project
--- Result ---
Total lines of Go code: 1204   ← wrong

agsh> bullshit, use wc -l to count properly
--- Result ---
Total line count: 2581          ← correct; reused prior context
```
