# Bug Log & Fix History
[toc]

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

---

## Issue #8 — REPL UX: raw log spam, no visual pipeline feedback

**Symptom**
All `[R1]`, `[R3]` debug log lines printed to the same terminal as user output, making results hard to read. No visual indication of what the system was doing while processing a query.

**Root cause**
`log.Printf` defaulted to `os.Stderr` (visible in terminal). No progress indicator. No inter-role flow visualization.

**Fix**
1. Redirect `log` output to `~/.cache/agsh/debug.log` at startup — terminal stays clean.
2. Added `internal/ui/display.go` — a sci-fi terminal overlay driven by a bus tap:
   - Braille spinner (`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`) with live status label, updated every 80 ms
   - Flow line per bus message: `  🧠 R1 ──[TaskSpec]──► 📐 R2`
   - Pipeline box borders: `┌─── ⚡ agsh pipeline ────` / `└─── ✅  18ms ────`
   - Infrastructure messages (memory read/write) rendered dim; correction signals in red
   - No external dependencies — pure ANSI escape codes
3. Modified `bus.go`: single `tapCh` replaced by `taps []chan Message`; `NewTap()` lets auditor and UI each register an independent fan-out channel.
4. REPL prompt updated to `⚡ agsh` header + cyan `❯` prompt char.

---

## Issue #9 — Infinite replan loop on macOS file-search tasks

**Symptom** (screenshot)
Searching for movie/music files looped: R4b kept sending ReplanRequest despite the executor finding the correct answer on the first try.

**Root causes** (from debug log analysis)
1. **macOS TCC/SIP protection**: `~/Music/Music` is system-protected. `find ~/Music` exits with status 1 (`Operation not permitted`), even though files in other directories are returned in stdout. The executor's result was correct and complete, but stderr showed a permission error.
2. **R4a "empty = incomplete" rule was too broad**: The rule "never accept empty result for a listing task" caused R4a to reject the music subtask even when the shell actually ran and found nothing — because there genuinely are no music files in accessible directories.
3. **R4a penalised inaccessible OS directories**: It required the search to cover `~/Music` fully, which is impossible. This sent a CorrectionSignal to an executor that had already done everything it could.

**Fix**
- **`agentval.go` system prompt**: Added two explicit rules:
  - *Empty-result rule*: if `tool_calls` shows a real search was run and stdout is empty, output `matched` — empty is a valid answer.
  - *OS permission rule*: `"Operation not permitted"` / `"Permission denied"` in stderr is an OS constraint, not an executor gap; accept the result if all accessible directories were searched.
- **`executor.go` system prompt**: Added macOS guidance — always append `2>/dev/null` to find; never include `~/Music/Music` or `~/Library`.
- **`executor.go` `normalizeFindCmd()`**: Code-level guardrail — automatically appends `2>/dev/null` to any `find` command that doesn't already have it, so permission errors never cause exit status 1 or hide stdout results.

---

## Issue #10 — REPL input: backspace broken, arrow keys show codes, Chinese unsupported

**Symptom**
In the REPL:
- Backspace printed `^?` instead of deleting the previous character
- Arrow keys printed raw escape sequences (`^[[A`, `^[[B`, `^[[C`, `^[[D`)
- Up/down did not navigate command history
- Chinese (and other multi-byte Unicode) input was garbled or split across reads

**Root cause**
`bufio.Scanner` reads raw bytes from stdin with no terminal awareness.
It has no concept of terminal line editing, control sequences, or multi-byte character boundaries.

**Fix**
Replaced `bufio.Scanner` in `runREPL` with `github.com/chzyer/readline`:
- Terminal set to raw mode; readline handles backspace, `←→`, `↑↓` natively
- `↑↓` arrows navigate persistent session history (stored in `~/.cache/agsh/history`)
- Unicode-aware: correctly handles multi-byte UTF-8 including CJK wide characters
- `Ctrl+A/E` (line start/end), `Ctrl+W` (delete word) all work
- Clarify prompt uses `rl.SetPrompt()` so clarification answers also get proper editing
- `Ctrl+D` exits cleanly (EOF)
- `bufio.Scanner` retained only in `runTask` (one-shot mode, non-interactive)

---

## Issue #11 — Ctrl+C abort: second press exits program; executor keeps running after abort

**Symptom** (screenshot)
1. First Ctrl+C: "⚠️ task aborted" shown correctly — but executor/agentval goroutines kept running, display kept printing flow lines and spinning
2. Second Ctrl+C: "agsh: shutting down" — program exited instead of returning to REPL prompt

**Root causes**
1. **Executors used main `ctx`**: `runSubtaskDispatcher` called `exec.RunSubTask(ctx, ...)` using the process-wide context. Ctrl+C cancelled only `taskCtx` (the REPL wait loop), not the goroutines doing LLM calls and tool execution.
2. **Display never saw abort**: `d.inTask` stayed `true` (no FinalResult was received), so the spinner and flow lines kept appearing after abort, flooding the terminal.
3. **Signal handler called `cancel()` when idle**: when `taskCancel == nil` (after the first abort set it to nil), the SIGINT goroutine called `cancel()` → "agsh: shutting down". This happened in the brief window before readline re-entered raw mode (which would have intercepted Ctrl+C as `ErrInterrupt` instead of SIGINT).

**Fix**
- **Dispatcher now uses per-task contexts**: `runSubtaskDispatcher` maintains `taskCtxs map[parentTaskID → {ctx, cancel}]`. Each executor/agentval goroutine receives the task-specific context. When Ctrl+C fires, the signal handler sends the `taskID` to `abortTaskCh`; the dispatcher calls `entry.cancel()` to stop all goroutines for that task (cancels in-flight LLM calls and shell commands immediately).
- **`Display.Abort()`**: new method sends to `abortCh`; the `Run()` goroutine calls `endTask(false)` — prints the `❌` footer, sets `inTask = false`, stops spinner and flow lines.
- **Signal handler no longer exits on second Ctrl+C**: when `taskCancel == nil` (idle), the handler does nothing. Exiting is exclusively via readline's `ErrInterrupt` → two-press confirmation, or typing `exit`/`Ctrl+D`. This eliminates the accidental-exit race.

---

## Issue #12 — `glob` with `root:"."` finds no user personal files

**Symptom**
```
[R3] tool glob result: (no files matched pattern *三个代表* under .)
```
Searching for a file in the user's Downloads or home directory returned nothing.

**Root causes**
1. **`root: "."` is the project working directory**, not the user's home. `GlobFiles(".", pattern)` walks `/Users/haricheung/code/agentic-shell` — a code repository — which contains no personal documents.
2. **`GlobFiles` did not expand `~`**: passing `root: "~"` or `root: "~/Downloads"` would walk a literal directory named `~` on disk (which doesn't exist), silently returning no results.

**Fix**
- **`internal/tools/glob.go`**: Added `~` prefix expansion at the top of `GlobFiles`:
  ```go
  if root == "~" || strings.HasPrefix(root, "~/") || strings.HasPrefix(root, "~\\") {
      home, err := os.UserHomeDir()
      if err == nil { root = home + root[1:] }
  }
  ```
- **`internal/roles/executor/executor.go` system prompt**: Added explicit `root` guidance under the `glob` tool description:
  - `root: "."` → current project directory (code/configs in repo); use for project-scoped searches.
  - `root: "~"` → user's home directory; use when searching for personal files (documents, downloads, music, photos, etc.).
  - `root: "~/Downloads"`, `root: "~/Documents"`, etc. → specific user directories.
  - **Never** use `root: "."` to search for user personal files — it will find nothing outside the project.

---

## Issue #13 — Stale ExecutionResult published after Ctrl+C reopens pipeline box

**Symptom** (screenshot)
After aborting a task (⚠️ task aborted / ❌ box closed), a new pipeline box immediately opened at the REPL prompt showing `R3 ──[ExecutionResult: failed]──► R4a`. The REPL was idle with no task running.

**Root causes** (confirmed via debug log)
1. When the task context is cancelled by Ctrl+C, the in-flight LLM call returns a `context canceled` error. `RunSubTask` caught the error and built a `failed` result — then **still published it to the bus** unconditionally.
2. The display tap received the `ExecutionResult: failed` message, saw `inTask == false` (Abort had just fired), and called `startTask()`, opening a new pipeline box.
3. There was no suppression mechanism to block stale post-abort messages from triggering a new box.

**Fix**
- **`executor.go` `RunSubTask`**: Check `ctx.Err() != nil` before every `Publish()` call — both the initial result and correction-round results. If the context is cancelled, return immediately without publishing. This stops the cascade at source.
- **`display.go`**: Added `suppressed bool` field + `Resume()` method. `Abort()` now also sets `suppressed = true`. Incoming bus messages while suppressed and `!inTask` are drained silently (no `startTask()`). Acts as a safety net for any message that was already in-flight when the executor check fires.
- **`main.go`**: `disp.Resume()` called at the top of each new REPL task (before `perceiver.Process()`), lifting the suppression exactly when the user submits a new query.

---

## Issue #14 — Personal file search takes 6 minutes (find ~ / glob root:"~" both scan entire home)

**Symptom**
```
time find ~ -name '*三个代表*' -type f 2>/dev/null | head -20
# → 0.46s user 7.75s system 2% cpu 5:51.35 total
```
Finding a single file by name in the home directory took nearly 6 minutes.

**Root cause**
Both `find ~` (shell) and `GlobFiles(root:"~")` enumerate every inode under `~` — including `~/Library`, cloud sync folders, and millions of cached files — because they have no OS index. The result is the same slow scan regardless of which tool is used.

**Fix**
Added `mdfind` as a first-class executor tool backed by macOS Spotlight:
- **`internal/tools/mdfind.go`**: `RunMdfind(ctx, query)` calls `mdfind -name <query> 2>/dev/null`. Spotlight's persistent index returns results in < 100 ms regardless of file location.
- **`executor.go` `runTool`**: new `"mdfind"` case.
- **Executor system prompt**: `mdfind` listed first with explicit ALWAYS-use guidance for personal file searches. `glob` demoted to project-only (source code, configs). Decision step updated accordingly.

**Benchmark**: `mdfind -name '三个代表'` → **77 ms** vs `find ~` → **351 s** (4500× faster).

---

## Issue #15 — `glob` silently returns 0 results for globstar patterns (`**/*.go`)

**Symptom**
LLMs routinely emit patterns like `**/*.go` or `*/*.json` (shell globstar style). These returned 0 results with no error.

**Root cause**
`GlobFiles` matched the pattern against `d.Name()` (filename only, no path separators). Any `/` in the pattern causes `filepath.Match` to return `false` for every file. Also, the example `"pattern":"*.go"` in the executor system prompt biased the LLM toward Go-specific patterns.

**Fix**
- **`glob.go` `GlobFiles`**: strip everything up to and including the last `/` from the pattern before matching. `"**/*.go"` → `"*.go"`, `"*/*.json"` → `"*.json"`. Verified: `GlobFiles(".", "**/*.go")` now returns the same 8 files as `GlobFiles(".", "*.go")`.
- **Executor system prompt**: example changed from `"pattern":"*.go"` to `"pattern":"*.json"`; added note *"Pattern matches the FILENAME ONLY — do NOT include '/'"*.

---

## Issue #16 — Result output shows literal `\n` instead of rendered newlines

**Symptom**
```
"Available free disk space:\n- / (root): 191 GiB\n- /System/Volumes/VM: 191 GiB\n..."
```
Newlines in the output string were displayed as the two-character sequence `\n` instead of actual line breaks.

**Root cause**
`printResult` passed all output through `json.MarshalIndent`, which wraps strings in double-quotes and escapes real newlines to `\n`. The JSON-encoded string was then printed verbatim.

**Fix**
`printResult` now:
1. Marshals output to JSON, then attempts `json.Unmarshal` into a `string`.
2. If successful (plain string output): prints directly with `fmt.Println` — real newlines render correctly.
3. If not (structured object/array): falls back to `json.MarshalIndent` for pretty JSON.
4. Suppresses the output block when it duplicates the summary.

---

## Issue #17 — Subtask B fails because it ran in parallel with subtask A that locates its input

**Symptom** (debug log)
```
[R4a] subtask=b2c3d4e5... FAILED: Prerequisite not met: source file '三个代表.mp4' was not
      found via mdfind. The subtask cannot proceed as it depends on the successful location
      of the source file, which failed.
```
Meanwhile subtask A (locate file) succeeded and was matched. The file was present all along.

**Root cause**
The dispatcher launched ALL subtasks simultaneously, ignoring the `sequence` field. Subtask A
(sequence=1, locate file) and subtask B (sequence=2, extract audio) ran in parallel. Subtask B
had to re-discover the file itself; mdfind returned nothing for that query, so it failed.

Two compounding factors:
1. `sequence` was correctly set by the planner, but the dispatcher never read it.
2. The planner prompt only said "same sequence = parallel" — it never said "different sequence = dependency ordering", so future plans might still assign same-sequence to dependent tasks.

**Fix**
- **`cmd/agsh/main.go` `runSubtaskDispatcher`**: Rewrote the dispatcher to implement sequential dispatch:
  - Subscribes to `MsgDispatchManifest` to learn expected subtask count.
  - Buffers incoming subtasks in `bySeq map[int][]SubTask`.
  - Once all expected subtasks arrive, dispatches the lowest sequence group.
  - Each agentval goroutine signals completion via `completionCh`; when inFlight reaches 0, the next sequence group is dispatched.
  - Outputs from completed sequences are collected and injected into every next-sequence subtask's `Context` field as "Outputs from prior steps (use these directly — do not re-run discovery)".
- **Planner system prompt**: Added explicit sequence rules — different sequence numbers for dependent subtasks, same sequence number for truly independent parallel subtasks. Explained that the dispatcher injects prior-step outputs automatically.

**Behaviour after fix**
```
sequence=1: locate file → mdfind → /Users/haricheung/Downloads/三个代表.mp4
            [wait for completion]
sequence=2: extract audio (context includes "prior step output: /Users/.../三个代表.mp4")
            → uses the injected path directly, no re-discovery needed
```

---

## Issue #18 — LLM hallucinates ffmpeg failure; task abandoned despite success

**Symptom**
Task "extract audio from 三个代表.mp4" was abandoned after 3 replan rounds with:
> "Task abandoned after 3 failed attempts. No new audio file was created because ffmpeg failed
> to overwrite existing file, and verification subtask could not confirm file existence or
> playability due to missing file."

The file `/Users/haricheung/Downloads/三个代表.mp3` actually existed (526 KB, valid MP3, 28s).
The ffmpeg command with `-y` flag had succeeded in replan round 2.

**Root cause**
`firstN(result, 2000)` in `executor.go` truncated shell tool output to 2000 characters before
passing it to the LLM. ffmpeg's version banner + build configuration alone is ~2500 characters,
so the LLM context window for that tool call ended mid-config-dump — **the actual encoding
result or error line was never visible to the LLM**.

The cascade:
1. Replan 1: `ffmpeg ... 三个代表.mp3` (no `-y`) → file already exists → real error, correctly
   reported as failed.
2. Replan 2: `ffmpeg ... -y ... 三个代表.mp3` → **actually succeeded** (overwrites), but LLM
   saw only the truncated banner → hallucinated "file already exists" → reported `status: failed`.
3. Verification subtask: `ls -la` showed the file (526249 bytes), `afplay` played it, `ffprobe`
   confirmed 28s duration — all proving success. But R4a kept retrying because its success
   criteria included "confirm ffmpeg extraction succeeded in this run" and the extraction subtask
   was (incorrectly) marked failed. Both subtasks exhausted maxRetries=2 → reported failed.
4. R4b saw 2/3 subtasks failed → replanned again → repeat ×3 → abandoned.

**Compounding factor**
The LLM was anchored to the previous failure ("already exists") and hallucinated the same error
even when the command succeeded, since it couldn't observe the actual result.

**Fix**
- **`executor.go`**: Replaced `firstN(result, 2000)` with `headTail(result, 4000)` for tool
  results passed to the LLM context. `headTail` preserves the first ~1333 chars AND the last
  ~2667 chars, with `...[middle truncated]...` in between. Long-banner tools like ffmpeg now
  show their actual result at the end even when the total output is large.

```go
func headTail(s string, maxLen int) string {
    if len(s) <= maxLen { return s }
    head := maxLen / 3
    tail := maxLen - head
    return s[:head] + "\n...[middle truncated]...\n" + s[len(s)-tail:]
}
```

**Verification**
ffmpeg banner is ~2500 chars; with `headTail(4000)`: head=1333 shows version+partial config,
tail=2667 shows the remaining config + actual encode output or error. The LLM now sees the result.

**Note**: This fix was necessary but not sufficient — see Issue #19 for the R4a evidence gap
that caused the full cascade despite the executor correctly reporting `completed`.

---

## Issue #19 — R4a rejects completed subtasks: ToolCalls carries no output evidence

**Symptom**
After Issue #18 fix, the executor LLM correctly reported `status: completed` with
`output: "MP3 file successfully created at ..."`. But R4a still scored it as `retry` both
attempts → `max retries reached → failed`. Task still abandoned.

**Root cause**
R4a's scoring rule: *"Trust tool output (stdout, file paths, command results) as primary evidence.
The executor's prose claim alone is not evidence."*

`ExecutionResult.ToolCalls` was populated only with tool names + command inputs:
```
["shell:ffmpeg -i '/Users/.../三个代表.mp4' -q:a 0 -map a '...mp3'"]
```
No output. R4a saw a prose claim ("MP3 file successfully created") with a tool call that had
no observable result — exactly the pattern it's trained to distrust. Verdict: `retry`.

The actual ffmpeg output (`size= 514kB time=00:00:28.23 bitrate= 149.5kbits/s speed=19.6x`) was
only in the executor's internal `toolResultsCtx`, used to inform the LLM. It never flowed into
`ExecutionResult`.

**Fix**
- **`executor.go`**: After each `runTool` call, append the last 120 chars of actual output (or
  error string) to the corresponding `toolCallHistory` entry before it becomes `ExecutionResult.ToolCalls`:

```go
// success
toolCallHistory[len(toolCallHistory)-1] += " → " + lastN(strings.TrimSpace(result), 120)
// error
toolCallHistory[len(toolCallHistory)-1] += " → ERROR: " + firstN(err.Error(), 80)
```

R4a now receives:
```
"shell:ffmpeg -i '/Downloads/三个代表.mp4' -q:a 0 -map a '...mp3' → ...
  size= 514kB time=00:00:28.23 bitrate= 149.5kbits/s speed=19.6x"
```
That is concrete, verifiable evidence → verdict: `matched`.

**Verification**
End-to-end test: "find 三个代表.mp4 and extract its audio to mp3"
- seq=1 locate: MATCHED attempt=1 ✓
- seq=2 extract: MATCHED attempt=1 ✓ (no retries)
- R4b verdict: accept ✓
- Output file: `/Users/haricheung/Downloads/三个代表.mp3` — 514KB, valid MP3, 28s

---

## Issue #20 — Spinner line-wrap floods the terminal with identical retry lines

**Symptom**
During a correction/retry, the terminal filled with dozens of identical lines:
```
⠸ ⚙️  retry 1 — Use a macOS-compatible command like 'ps aux | sort -
⠼ ⚙️  retry 1 — Use a macOS-compatible command like 'ps aux | sort -
⠴ ⚙️  retry 1 — Use a macOS-compatible command like 'ps aux | sort -
...
```
Each line was a new spinner animation frame, not an in-place overwrite.

**Root cause**
The spinner used `\r` (carriage return) to overwrite the current line. When the status
string was long (~70 visible chars), the terminal wrapped it to a second line. `\r` then
returned the cursor to the start of the *second* (wrapped) line, not line 1. Each 80ms tick
wrote a new visible line instead of overwriting — producing a continuous scroll of identical
frames.

The status text for corrections was built as:
`"⚙️  retry N — " + clip(WhatToDo, 55)` ≈ 70 visible chars, which wraps in an 80-col terminal.

**Fix**
- **`display.go` ticker**: changed `\r` to `\r\033[K` — erase-to-EOL after carriage return
  clears leftover chars from longer previous statuses on the same line.
- **`display.go` `dynamicStatus`**: reduced `WhatToDo` clip from 55 → 38 runes. Full spinner
  line is now ≤ 54 visible cols, safely within any terminal ≥ 60 cols — no wrapping possible.

---

## Issue #21 — Model repeatedly uses `find /Users/...` instead of `mdfind`

**Symptom**
Despite the executor system prompt listing `mdfind` as tool #1 for personal file searches,
the model (Volcengine/DeepSeek) repeatedly emitted slow `find /Users/haricheung` shell
commands — taking 6+ minutes instead of <1 second.

Example from debug log:
```
[R3] running tool=shell cmd=find /Users/haricheung -name "三个代表.mp3" -o -name "三个代表*.mp3" ...
```

**Root cause**
Model non-compliance with prompt priority. The `shell` tool description also contained
"Always append 2>/dev/null to find commands" which implicitly validated using `find` at all.
Prompt reinforcement alone proved insufficient across multiple sessions.

**Fix**
Two-layer enforcement:
1. **Prompt**: `shell` description changed to "NEVER use 'find' to locate personal files —
   use mdfind instead", removing the implicit `find` validation.
2. **Code**: `redirectPersonalFind()` in `executor.go:runTool` — detects `shell find` commands
   targeting personal paths (`/Users/`, ` ~`, `~/`, `/home/`, `/Volumes/`) and transparently
   redirects them to `RunMdfind()` with the extracted `-name` pattern. The model receives fast
   Spotlight results without knowing the redirect happened.

```go
if query, ok := redirectPersonalFind(tc.Command); ok {
    log.Printf("[R3] redirecting personal find to mdfind: query=%q", query)
    return tools.RunMdfind(ctx, query)
}
```

Routing rules:
- `find /Users/haricheung -name "三个代表.mp3"` → `mdfind -name '三个代表'` (redirected)
- `find ~ -name "*.pdf"` → `mdfind -name '*.pdf'` (redirected)
- `find . -name "*.go"` → unchanged (project search)
- `find /tmp -name "*.log"` → unchanged (system path)

---

## Issue #22 — `/audit` always shows zeros on process restart

**Symptom**
After exiting and restarting agsh, `/audit` immediately showed:
```
Tasks observed:  0
Avg corrections: 0.00
Gap trends:      ↑improving=0  →stable=0  ↓worsening=0
No anomalies detected.
```
even though several tasks had been run in the previous session.

**Root cause**
The auditor's window stats (`tasksObserved`, `totalCorrections`, `gapTrends`,
`boundaryViolations`, `driftAlerts`, `anomalies`, `windowStart`) were held only in
memory. On process restart they were initialised to zero values in `New()`, discarding
all accumulated data from prior runs.

**Fix**
Persist window stats to `~/.cache/agsh/audit_stats.json` and reload on startup:

- **`persistedStats` struct**: mirrors the seven window fields with JSON tags.
- **`loadStats()`**: called synchronously inside `New()` before returning. Reads
  `audit_stats.json`; silently no-ops if absent (first run). Restores all seven fields
  so the window is correct from message #1 of the new session.
- **`saveStats()`**: acquires the mutex to snapshot current window fields, then writes
  JSON to `audit_stats.json` with `os.WriteFile`. Called from the auditor goroutine only
  (no extra lock needed for the write itself).
- **`process()` call site**: `saveStats()` is called only after `MsgDispatchManifest`,
  `MsgReplanRequest`, or any anomaly — the three event types that actually mutate stats.
  Not called on every tap message (20+ per task).
- **`publishReport()` call site**: `saveStats()` is called immediately after the window
  reset so a crash right after `/audit` doesn't replay old stats on next start.
- **`auditor.New()` signature updated**: `statsPath string` added as fourth parameter
  (before `interval`).
- **`main.go`**: passes `filepath.Join(cacheDir, "audit_stats.json")` as `statsPath`.

**Behaviour after fix**
```
Session 1:
  agsh> list all go files      ← tasks=1 recorded
  agsh> exit

Session 2:
  agsh> /audit
  Tasks observed:  1            ← restored from audit_stats.json
  window_start: 2026-02-19...
```
After `/audit` triggers a report the window resets and the zeroed stats are immediately
persisted, so restarting again shows tasks=0 with the new `window_start`.
