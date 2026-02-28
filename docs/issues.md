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

---


## Issue #23 — Clarification question printed dozens of times before user types anything

**Symptom**
```
❯ 算算目前我用的外接显示器是什么尺寸的
? 您需要我通过什么方式来确定外接显示器的尺寸？...
? 您需要我通过什么方式来确定外接显示器的尺寸？...
... (×12)
❯ up to you
```
The same clarification question appeared ~12 times before the user had typed any answer.

**Root causes**
1. **No cap on clarification rounds**: `perceiver.Process()` looped unconditionally — if the
   model returned `needs_clarification: true` more than once, the loop continued forever.
2. **IME-buffered empty keystrokes**: Typing Chinese input via an IME can leave residual
   keystrokes in the terminal buffer. When `rl.Readline()` was called inside `clarifyFn`,
   it consumed those buffered empty strokes and returned `""` immediately — without blocking
   for real user input. Each empty answer caused the model to be called again with a blank
   clarification, which returned `needs_clarification: true` again, calling `clarifyFn`
   again, and so on until the buffer was drained.

**Fix** (`perceiver/perceiver.go`):
- **`maxClarificationRounds = 2`**: `Process()` now loops at most twice before giving up.
- **Empty answer → break**: if the user provides an empty answer (Enter with no text),
  treat it as "proceed with your best interpretation" and exit the loop immediately.
- **Final commit call**: after the loop exits (max rounds or empty answer), `perceive()` is
  called one final time with an appended instruction
  `"[Instruction: proceed with the best interpretation; do not request further clarification.]"`
  to force the model to emit a `TaskSpec` instead of another `needs_clarification`.
- **`publish()` helper**: extracted from the loop body to avoid duplicating the bus publish +
  log line.

**Behaviour after fix**
- First clarification round: model asks → user sees question → user types answer → loop
- Second clarification round (if model still unclear): model asks once more → user answers
- After two rounds, or on empty answer: model is forced to commit; task proceeds.

---


## Issue #24 — `❯` prompt not shown after task completes

**Symptom**
After a task finishes, the REPL sometimes returned with no visible prompt — the `❯` readline
cursor was missing. The pipeline footer (`└─── ✅ ...`) appeared but the prompt did not.

**Root cause**
Race condition between the display goroutine and the REPL goroutine:

1. MetaValidator calls `bus.Publish(MsgFinalResult)` — puts tap message in display goroutine's
   channel buffer, but the goroutine may not have run yet.
2. MetaValidator calls `outputFn()` — fills `resultCh`, unblocking the REPL goroutine.
3. REPL goroutine: `printResult()` → breaks out of `waitResult` → calls `rl.Readline()`,
   which draws `❯ ` on the current terminal line.
4. Display goroutine: finally gets scheduled, receives `MsgFinalResult` tap → `endTask()` →
   prints `\r\033[K└─── ✅ ...`. The `\r\033[K` erases the `❯` that readline drew.

The REPL goroutine and display goroutine race to the terminal. The `❯` is erased whenever
the display goroutine loses the race and fires after readline.

**Fix**
Added `WaitTaskClose(timeout time.Duration)` to `Display` — a synchronisation point that
blocks until the pipeline box is fully closed:

- **`taskDone chan struct{}`** field added to `Display`; created in `startTask()` under
  the mutex, closed (and nilled) by `endTask()` under the mutex.
- **`WaitTaskClose(300ms)`** called in the REPL's `waitResult` loop immediately after
  receiving the final result, **before** `printResult()` and before readline resumes.
  The 300 ms timeout prevents deadlock if the display goroutine is stuck.

Order after fix (deterministic):
1. Display: `endTask()` prints footer → closes `taskDone`
2. REPL: `WaitTaskClose` returns → `printResult()` → `rl.Readline()` draws `❯`

---


## Issue #25 — Clarification question reprinted on every readline internal redraw

**Symptom**
```
? Are you asking for the physical monitor size (like 27 inches) or...
? Are you asking for the physical monitor size (like 27 inches) or...
... (×20+)
```
The question line repeated dozens of times during a single `clarifyFn` call, even
after the Issue #23 loop cap was in place.

**Root cause**
The readline prompt was set with an embedded newline:
```go
rl.SetPrompt(fmt.Sprintf("\033[33m?\033[0m %s\n\033[36m❯\033[0m ", question))
```
The chzyer/readline library calculates prompt width assuming a single line. When the
prompt contains `\n`, readline only tracks the portion after the newline (`❯ `) as
the "active" prompt line. On every internal redraw (terminal resize, interference from
concurrent writes, cursor movement), readline erases and redraws from the `❯` position
— but the `? question` line above it is raw output that was already scrolled into
history. Each redraw cycle prints another copy of the full two-line prompt, leaving the
previous `? question` line stranded above it. With enough redraws, dozens of copies
accumulate before the user types anything.

**Fix** (`cmd/agsh/main.go`):
Print the question with `fmt.Printf` as plain output before calling `rl.Readline()`.
The readline prompt stays as the simple single-line `❯ ` which readline tracks correctly.
```go
fmt.Printf("\033[33m?\033[0m %s\n", question)
ans, err := rl.Readline()
```
The question is now printed exactly once; readline only manages its own `❯ ` line.

---


## Issue #26 — `/audit` opens a pipeline box that never closes; REPL appears stuck

**Symptom**
After running `/audit`, the pipeline box opened (`┌─── ⚡ agsh pipeline`) and never
closed. The spinner kept running and the `❯` prompt was continuously erased by the 80ms
ticker's `\r\033[K`, making the REPL appear completely frozen.

**Root cause**
The display goroutine's tap handler treated `MsgAuditQuery` as a normal task pipeline
message. When `/audit` published `MsgAuditQuery`, the display saw it, called `startTask()`
(opening the pipeline box and setting `inTask = true`), and started the spinner. The
corresponding response `MsgAuditReport` was published later, but the display's `endTask()`
is only triggered by `MsgFinalResult` — `MsgAuditReport` never matched. So `inTask` stayed
`true` indefinitely; the spinner kept firing every 80ms and erasing whatever was on the
current terminal line, including the readline prompt.

**Fix** (`internal/ui/display.go`):
Skip both `MsgAuditQuery` and `MsgAuditReport` at the top of the tap handler before any
`startTask()` logic. Audit messages are meta-system events, not task pipeline events; they
must never open a pipeline box.
```go
if msg.Type == types.MsgAuditQuery || msg.Type == types.MsgAuditReport {
    continue
}
```

---


## Issue #27 — Planner generates fake, repeated subtask UUIDs

**Symptom**
The same subtask ID `a1b2c3d4-e5f6-7890-abcd-ef1234567890` appears 270 times across
completely different tasks in the debug log. The top repeated IDs share an obvious
sequential template pattern:
```
a1b2c3d4-e5f6-7890-abcd-ef1234567890  → 270 uses
a1b2c3d4-e5f6-7890-abcd-ef1234567891  →  33 uses   ← last digit incremented
a1b2c3d4-e5f6-7890-abcd-ef1234567892  →  33 uses
e3b0c442-98fc-1c14-9afc-39c7c5d6f0b1  → 166 uses
b2c3d4e5-f6a7-8901-bcde-f01234567890  → 112 uses
```
The top 10 repeated IDs account for ~26% of all subtask dispatches.

**Root cause**
The planner LLM is fabricating UUIDs in its JSON output rather than delegating ID
generation to the Go runtime. The planner system prompt tells R2 to assign a `subtask_id`
to each subtask — the LLM invents plausible-looking but deterministic UUIDs from its
training distribution. It is never called `uuid.NewString()`; it just writes hex that
looks like a UUID.

**Impact**
The entire dispatcher routing guarantee depends on subtask ID uniqueness. Results from
task A's executor targeting subtask_id X are delivered to task B's agentval goroutine if
task B reuses ID X. This is confirmed by:
```
[DISPATCHER] WARNING: no state for subtask=a1b2c3d4... (already completed?)
```
The dispatcher receives an `ExecutionResult` for an ID whose state was already cleaned
up by a different task. The result is silently dropped; the intended recipient never
receives it.

**Fix needed**
Subtask IDs must be assigned by the Go runtime after the LLM responds, not by the LLM
itself. The planner prompt should not ask for `subtask_id` in the JSON output; Go code
should call `uuid.NewString()` and inject IDs before publishing.

---


## Issue #28 — R4b accepts tasks with failed subtasks (1 matched + 1 failed = accept)

**Symptom** (debug log)
```
[R4b] outcome for subtask=d9e0f1... status=matched (1/2)
[R4b] outcome for subtask=c8f9a0... status=failed  (2/2)
[R4b] task=linus_torvalds_recent_activity ACCEPTED
```
R4b accepted a task where one of two subtasks explicitly failed.

**Root cause**
R4b validation is LLM-based and holistic. The LLM receives all outcomes together and
reasons about whether "the overall goal" is met — it does not mechanically check whether
every subtask passed. When a failed subtask's counterpart produced plausible-sounding
output, the LLM concluded the combined result was "good enough" and emitted `accept`.
The prompt instruction "accept only when ALL success_criteria met" is advisory to the
LLM, not an enforced code constraint.

**Impact**
The user receives a partial or wrong result and is told the task succeeded. Incorrect
answers are delivered silently with no signal that part of the execution failed.

**Fix**
Added a code-enforced hard gate in `evaluate()` before any LLM call:
```go
if len(failedIDs) > 0 {
    m.triggerReplan(...)  // LLM never reached
    return
}
// LLM called only when ALL subtasks matched — merge outputs only
```
Extracted `triggerReplan()` shared by the hard gate and the LLM-verdict replan path.
The LLM can no longer override a `status=failed` outcome by reasoning about "overall goal".
Tests added in `metaval_test.go` covering all `computeGapTrend` expectations.

---


## Issue #29 — Memory system is structurally bypassed; effectively write-only

**Symptom**
The same user question about monitor size was answered incorrectly three times in a row.
R5 stored failure lessons after each attempt. The agents made identical mistakes on every
retry with no evidence that prior lessons influenced any plan.

**Root cause — two structural failures:**

1. **R1 (Perceiver) is completely memory-blind.** Misunderstandings are locked in at
   R1 before R2 ever queries memory. R1 produces a `TaskSpec` with the wrong `intent`
   and `success_criteria`, and R2 plans against that wrong spec. Any memory R2 retrieves
   is irrelevant because the task is already mis-characterised. R1 has no integration
   with R5 at all.

2. **R2 memory results are advisory, not enforced.** R2 sends `MsgMemoryRead`, receives
   entries, but its prompt gives memory the same weight as any other context — the LLM
   may or may not act on it. The log confirms R2 dispatches identical subtask structures
   with identical fake UUIDs regardless of stored lessons. There is no code-level check
   that memory results must materially change the plan when relevant entries exist.

**Impact**
The memory system correctly stores episodic and procedural entries. Nothing reads them
in a way that changes agent behaviour. It is real infrastructure connected to nothing
that matters. The system cannot learn across sessions.

**Fix needed**
R1 must query memory before characterising ambiguous tasks. R2 must have a code-enforced
contract: if relevant procedural entries exist, the plan must demonstrate it has avoided
the previously recorded failure approach.

---


## Issue #30 — Validators evaluate holistically; per-criterion enforcement is absent

**Symptom**
R4a accepts `status=completed, output="27 inches"` even when the tool stdout is empty —
the claim is plausible so the LLM scores it as `matched`. R4b accepts 1-matched + 1-failed
outcomes (see Issue #28). Validators behave as lenient reviewers rather than strict
checkers.

**Root cause**
Both R4a and R4b are LLM-based validators scoring LLM-produced output. LLMs reason by
plausibility and analogy — they are constitutionally unsuited to strict boolean
per-criterion checking. The design requires each `success_criteria` entry to be
independently verified (pass/fail), but the LLM receives all criteria together with all
output and forms a holistic impression. One plausible-sounding criterion can carry the
whole result even when others are clearly unmet.

This is the same failure as writing expectations without tests: the expectations exist in
the prompt, but without mechanical per-criterion checking they are decoration.

**Impact**
- R4a: spurious `matched` verdicts on empty or hallucinated tool output
- R4b: partial task acceptance (failed subtasks ignored if others look good)
- Correction loops that should fire do not; the system over-reports success

**Fix needed**
Validation must be restructured so that each `success_criterion` is evaluated
independently and produces an explicit boolean verdict before any holistic merge. A
single `false` must hard-fail the validation in code, regardless of what the LLM
concludes about the overall result.

---


## Issue #31 — LLM output parser fails on trailing prose after JSON

**Symptom**
```
[R3] ERROR re-executing subtask ...: parse LLM output: invalid character 'I' after
top-level value (raw: {"action":"result",...}
I have the required output from the previous tool call...)
```
Correct executions are thrown away and republished as `status=failed`.

**Root cause**
The LLM occasionally appends explanatory prose after the closing `}` of its JSON
response. `StripFences()` strips markdown code fences but does not strip trailing
non-JSON text. `json.Unmarshal` fails on the first non-whitespace character after the
top-level value. R3 catches the parse error, and because it cannot interpret the result,
publishes a `failed` ExecutionResult. R4a then immediately fails the subtask as an
infrastructure error.

**Fix needed**
After `StripFences()`, truncate the string at the first `}` that closes the top-level
JSON object before attempting `json.Unmarshal`.

---


## Issue #32 — `redirectPersonalFind` discards `-path` filter; mdfind rejects glob patterns

**Symptom**
```
find /Users/haricheung -type f -name "*.json" -path "*memory*"
→ redirecting to mdfind: query="*.json"
→ (no files found with name matching "*.json")
```
Both the path filter and the result are lost.

**Root cause — two compounding failures:**

1. `redirectPersonalFind` extracts only the `-name` value and silently discards all
   other `find` flags including `-path`, `-type`, and `-maxdepth`. The memory file path
   constraint is lost.

2. `mdfind -name '*.json'` performs exact name matching — Spotlight does not expand
   glob wildcards in `-name` queries. A pattern containing `*` will never match any
   file. The redirect produces guaranteed empty results for any pattern with a wildcard.

**Fix needed**
For redirected queries, extract the stem from the `-name` pattern (strip `*` and
leading path separators) and pass only that to `mdfind`. Post-filter results by
extension in Go code if needed. Alternatively, when `-path` is present, consider
using `shell find` within the specific subdirectory rather than redirecting to mdfind.

---


## Issue #33 — Memory entries treated as advisory; calibration not code-enforced

**Symptom**
R2 queried R5 before planning and received relevant memory entries, but the LLM
planner consistently ignored them. The same failing approaches (e.g. `shell find`)
were repeated even when procedural entries explicitly documented them as failures.
Memory was effectively write-only: read path worked, but entries had no behavioral
effect on planner output.

**Root cause**
`plan()` serialised the raw `MemoryResponse` as a JSON block and appended it to the
user prompt as advisory text:
```
Prior memory entries (learn from these):
[{"type":"procedural","timestamp":"...","content":"..."}]
```
LLMs treat advisory context as optional. The model had no strong structural cue to
treat the entries as hard constraints vs. background information, so it ignored them.
The problem is Issue #29 / T5 — memory was bypassed at the calibration step.

**Fix**
Implemented `calibrate()` in Go code (Steps 1–3 of the Memory Calibration Protocol):
- **Step 1**: Retrieve entries from R5 (already done via bus before this fix).
- **Step 2**: Sort by recency (newest first), cap at `maxMemoryEntries=10`, keyword-
  filter against intent (discard zero-overlap entries).
- **Step 3**: Derive structured constraint text: `MUST NOT` for procedural entries
  (failed approaches) and `SHOULD PREFER` for episodic entries (successful approaches).

The constraint block is injected with explicit headings:
```
--- MEMORY CONSTRAINTS (code-derived) ---
MUST NOT (prior failures — do not repeat these approaches):
  - [tags: file, search] "used shell find -name *.go"
--- END CONSTRAINTS ---
```
`MUST NOT` framing gives the LLM a hard structural signal that the model respects.
No extra LLM call is needed for calibration; all logic is deterministic Go code.

**Added**
- `calibrate(entries []types.MemoryEntry, intent string) string` — Steps 1–3
- `entrySummary(e types.MemoryEntry) string` — human-readable entry line
- `memTokenize(s string) []string` — keyword tokeniser (len≥3, lowercase)
- `maxMemoryEntries = 10` constant
- Tests: `internal/roles/planner/planner_test.go` — one test per documented expectation
- Expectation comments on all three functions

---


## Issue #34 — Validator criteria invisible in pipeline display

**Symptom**
The pipeline visualiser showed `MsgSubTask: #1 locate the audio file` with no hint
of what the subtask was being validated against. `MsgSubTaskOutcome` showed only
`matched` or `failed` with no detail about which criterion caused a failure.
Debugging validator behaviour required reading raw debug logs.

**Root cause**
`msgDetail()` in `display.go` rendered `MsgSubTask` as `#N intent` only (no
criteria). `MsgSubTaskOutcome` returned the bare `Status` string, discarding the
`GapTrajectory` and `UnmetCriteria` fields that are populated on failure.

**Fix**
Updated `msgDetail()`:

- **MsgSubTask**: appends `| <first criterion>` when `SuccessCriteria` is non-empty;
  appends `(+N)` suffix when there are multiple criteria. Example:
  `#1 locate audio | output contains a valid file path (+1)`

- **MsgSubTaskOutcome**: when `status="failed"` and `GapTrajectory` is non-empty,
  extracts the last trajectory point and shows the first unmet criterion:
  `failed | unmet: output contains a valid file path`
  When status is `matched` or no trajectory exists, returns the status string only.

**Added**
- Tests: `internal/ui/display_test.go` — one test per documented expectation
- Expectation comments on `msgDetail()`

---


## Issue #35 — Model switch fails: doubled `/chat/completions` path in URL

**Symptom**
Switching the active model (e.g. from DeepSeek to kimi-k2.5 / Aliyun DashScope)
caused all LLM calls to fail with HTTP 404 or connection errors.

**Root cause**
Some provider base URLs include the full endpoint path:
```
OPENAI_BASE_URL="https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
```
`client.go` always appended `/chat/completions` to the base URL, producing:
```
https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions/chat/completions
```
The correct convention is `OPENAI_BASE_URL` = base root only (e.g.
`https://dashscope.aliyuncs.com/compatible-mode/v1`), but this was easy to
copy-paste wrong from provider documentation.

**Fix**
In `llm.New()`, strip any trailing `/chat/completions` suffix from the base URL
before storing it, so both forms of the env var work:
```go
base = strings.TrimSuffix(base, "/chat/completions")
```

---


## Issue #36 — Left-arrow / Home cursor movement broken with CJK input

**Symptom**
- Pressing left-arrow while typing CJK text left the cursor in the wrong
  column (did not move back by a full character width).
- Pressing Home (go-to-start) placed the cursor mid-line, not at column 0.
- Backspace correctly deleted one CJK character per press.
- Entering ASCII text after CJK caused it to appear at the leftmost column
  instead of at the cursor position.

**Root cause (series of investigations)**

| Attempt | Hypothesis | Result |
|---|---|---|
| 1 | Prompt ANSI bytes counted as columns → wrong offset | Wrong: `ColorFilter` already strips ANSI before measuring |
| 2 | `❯` (U+276F) ambiguous width: Ghostty renders 2 cols but `runewidth` says 1 → promptLen mismatch | Partially right: replaced `❯` with ASCII `>` but cursor still broken |
| Final | `getBackspaceSequence()` in `runebuf.go` emits exactly **1 `\b` per rune** regardless of character width; CJK chars are 2 columns wide, so backing up by 1 column leaves cursor one column short per character; also, the `sep` (line-wrap boundary) map indexed by visual column was used as if it were a rune index — harmless for ASCII but wrong for CJK | ✅ Root cause confirmed |

**Fix**
Created a patched local copy of `chzyer/readline` at
`internal/readline_compat/` and wired it via a `go.mod` `replace` directive.

Changed `getBackspaceSequence()` in `runebuf.go`:
1. **Backspace count**: emit `runes.Width(r.buf[i-1])` backspaces per rune
   instead of always 1 — so CJK chars back up 2 columns.
2. **`sep` map**: rebuilt indexed by rune position (not visual column), using
   `col/r.width < (col+w)/r.width` to detect line-wrap boundary crossings.

---


## Issue #37 — R4b check logic had no criteria to evaluate against

**Symptom**
R4b (Meta-Validator) frequently accepted tasks where subtasks had actually
failed important criteria, or replanned unnecessarily. Its `verdict=accept`
was unreliable.

**Root cause — structural data gap**
`SubTaskOutcome` carried only `status`/`output`/`gap_trajectory` — no
`success_criteria`. R4b's system prompt said "check every success criterion
in the TaskSpec", but `TaskSpec` has no criteria (R1 is a pure transducer;
R2 owns criteria derivation). R4b was making verdicts with no criteria at
all — guessing from intent text alone.

**Fix**
1. Added `Intent string` and `SuccessCriteria []string` to `SubTaskOutcome`
   (types.go) so criteria travel with the outcome to R4b.
2. R4a populates both fields in every outcome it produces (extracted
   `outcome()`/`publish()` helpers to eliminate 6 duplicated struct
   literals).
3. R4b system prompt updated: check `success_criteria` in each
   `SubTaskOutcome`, not "criteria in TaskSpec".
4. R4b user prompt labels make the criteria-per-outcome structure explicit.
5. R4b `evaluate()` logs each outcome's criteria before calling the LLM
   for full auditability.

**Still unresolved / related**
- Issue #28 (R4b accepts 1-matched + 1-failed): the new criteria-per-outcome
  structure gives R4b the data to catch this, but the prompt still says
  "failed subtasks are acceptable if the overall goal is met". This wording
  should be tightened to require all criteria to be positively demonstrated.

---


## Issue #38 — success_criteria were intent echoes, not assertions

**Symptom**
R2 generated `success_criteria` that restated the intent as a question:
`"今天是星期几"` ("what day is today") instead of a verifiable assertion:
`"output explicitly states which day of the week today is"`. R4a could not
meaningfully score these — any output trivially satisfied an intent echo.

**Root cause**
The schema placeholder `"<verifiable from tool output>"` was too vague.
The LLM interpreted "verifiable" as "something related to the goal" rather
than "a falsifiable assertion checkable from raw tool output alone".

**Fix**
Added explicit "Success criteria rules" block to the R2 system prompt with
bad/good examples covering the three failure modes:
- Intent echo (bad: question form; good: assertion form with expected value)
- Missing concrete value (bad: "check PM2.5"; good: "output contains a
  numeric PM2.5 value")
- Not falsifiable without context
Updated the JSON schema placeholder to `"<assertion checkable against tool
output>"`.

---


## Issue #39 — Holding forward-delete (DEL) key exits the shell

**Symptom**
Holding the forward-delete (DEL) key while at the REPL prompt deleted
characters normally until the buffer became empty, then printed `exit` to
the terminal and exited the shell.

**Root cause**
`escapeExKey()` in `utils.go` decoded the forward-delete escape sequence
`\033[3~` to `CharDelete = 4`, which is the same constant as Ctrl+D.
In `operation.go`, `CharDelete` on an empty buffer is intentionally treated
as EOF (matching standard shell Ctrl+D behaviour): it writes `EOFPrompt`
(`"exit"`) to the terminal and sends `io.EOF` to the error channel, causing
the REPL to exit.

**Fix**
Added a new constant `CharFwdDelete` (distinct rune value, separate from
`CharDelete = 4`) and remapped `\033[3~` → `CharFwdDelete` in
`escapeExKey()`. Added a corresponding `case CharFwdDelete:` in
`operation.go` that deletes forward when buffer is non-empty and only bells
when empty — never sends `io.EOF`.

Files changed in `internal/readline_compat/`:
- `utils.go` — new `CharFwdDelete` constant; `escapeExKey` remap
- `operation.go` — new `case CharFwdDelete:` handler with Expectations comment
- `fwddel_test.go` — 3 tests (constants distinct, mapping correct, no CharDelete)

---


## Issue #40 — Spinner repeats on same line when correction signal contains CJK text

**Symptom**
When R4a issued a correction signal with a CJK `WhatToDo` string (e.g. `重新执行read_file命令读取…`),
the spinner line in the terminal repeated itself: each 80ms tick printed a new spinner frame
concatenated onto the previous line instead of overwriting it:
```
⠼ ⚙️  retry 1 — 重新执行…⠴ ⚙️  retry 1 — 重新执行…⠦ ⚙️  retry 1 — ...
```

**Root cause**
`clip(s, n)` in `display.go` counts **runes**, not **visual columns**. CJK characters occupy
2 terminal columns each, so `clip(c.WhatToDo, 38)` allowed 38 CJK runes = 76 visual columns.
With the `⚙️  retry N — ` prefix (~14 cols), the total spinner status reached ~90 columns,
wrapping the line on an 80-column terminal.

When a line wraps, `\r` (carriage return) only moves the cursor to the beginning of the
**last** wrapped terminal line, not the first. `\033[K` (erase to end of line) then cleared
only that partial line. The previous wrapped lines stayed on screen, and each tick appended
another frame, creating the repeated concatenation effect.

**Fix**
Added `runeWidth(r rune) int` (returns 2 for CJK/wide Unicode blocks, 1 for others) and
`clipCols(s string, cols int) string` (column-aware truncation) to `display.go`.
Changed `dynamicStatus()` CorrectionSignal case to use `clipCols(c.WhatToDo, 38)` instead
of `clip(c.WhatToDo, 38)`. With column-aware clipping, 38 CJK runes → capped at 19 runes
(38 cols), keeping the total spinner line within 54 visual columns regardless of script.

Files changed:
- `internal/ui/display.go` — added `runeWidth`, `clipCols`; updated `dynamicStatus`
- `internal/ui/display_test.go` — 8 new tests covering `runeWidth`, `clipCols`, and `dynamicStatus` CJK case

---


## Issue #41 — LLM token usage discarded; no per-task structured log for gradient computation

**Symptom**
No machine-readable record of LLM calls, tool calls, criterion verdicts, corrections, or replan
events per task. Token counts were discarded (API returned `usage` field but client ignored it).
Debugging required reading human-readable debug.log; GGS (v0.7) had no raw data to compute its
loss function components D, P, Ω.

**Root cause**
`llm.Chat()` returned `(string, error)` and discarded the `usage` field from the API response.
No structured logging existed in any role — only `log.Printf` to the shared debug.log.

**Fix**
1. `internal/llm/client.go` — added `Usage` struct; changed `Chat()` to `(string, Usage, error)`.
2. `internal/tasklog/tasklog.go` (new) — `Registry` + `TaskLog` with 9 event kinds
   (`task_begin`, `task_end`, `subtask_begin`, `subtask_end`, `llm_call`, `tool_call`,
   `criterion_verdict`, `correction`, `replan`). All `TaskLog` methods nil-safe.
   One JSONL file per task written to `~/.cache/agsh/tasks/<task_id>.jsonl`.
3. `internal/roles/planner/planner.go` — `logReg *tasklog.Registry` in constructor;
   `Open()` on plan start (idempotent across replan rounds); LLM call logged in `dispatch()`.
4. `internal/roles/executor/executor.go` — `tlog *tasklog.TaskLog` param on `RunSubTask`;
   `SubtaskBegin` at goroutine start; `LLMCall` per iteration; `ToolCall` per tool invocation.
5. `internal/roles/agentval/agentval.go` — `tlog` param on `Run`/`score`; `LLMCall` in
   `score()`; `CriterionVerdict` per criterion; `Correction` before retry; `SubtaskEnd`
   at every return path.
6. `internal/roles/metaval/metaval.go` — `logReg` in constructor; `LLMCall` in `evaluate()`;
   `Replan` in `triggerReplan()`; `Close("accepted")`/`Close("abandoned")` at task end.
7. `cmd/agsh/main.go` — creates `logReg`; passes to planner/metaval constructors and
   `runSubtaskDispatcher`; `logReg.Get(parentTaskID)` wires tlog into each subtask pair.

Files changed:
- `internal/tasklog/tasklog.go` (new)
- `internal/tasklog/tasklog_test.go` (new, 9 tests)
- `internal/llm/client.go`
- `internal/roles/planner/planner.go`
- `internal/roles/executor/executor.go`
- `internal/roles/agentval/agentval.go`
- `internal/roles/metaval/metaval.go`
- `cmd/agsh/main.go`

---


## Issue #42 — `search` tool always fails: DuckDuckGo API unreachable from mainland China

**Symptom**
Every `search` tool call times out after 15 seconds:
```
[R3] tool[5] → ERROR: websearch: http request: context deadline exceeded
```
The GFW blocks `api.duckduckgo.com`. The tool was never usable from the deployment environment.

**Root cause**
`internal/tools/websearch.go` used the DuckDuckGo Instant Answer API
(`https://api.duckduckgo.com/`), which is blocked in mainland China.

**Fix**
Replaced with the Bocha web search API (`https://api.bochaai.com/v1/web-search`):
- POST request with JSON body; auth via `BOCHA_API_KEY` env var (Bearer token)
- Returns `webPages.value[]` with title, snippet/summary, URL, date
- Fails fast with a clear error when `BOCHA_API_KEY` is not set
- `formatBochaResult` prefers `summary` over `snippet` when both are present
- Caps at 5 results; separates each with a blank line

Files changed:
- `internal/tools/websearch.go` — full rewrite (Bocha API)
- `internal/tools/websearch_test.go` (new, 7 tests for `formatBochaResult`)
- `CLAUDE.md` — updated tool table and env config section

---


## Issue #43 — v0.7 GGS spec not implemented

**Symptom**
Medium loop lacked a controller: R4b sent plain "retry" signals to R2 with no directional content. R2 had no way to know whether to change tools, change paths, or give up. The gradient was never computed.

**Root cause**
R7 (Goal Gradient Solver) was deferred in v0.6. The loss function (D, P, Ω, L, ∇L) was designed but not implemented. R4b computed a naive `gap_trend` from failed-subtask count deltas; R2 self-directed replanning without gradient guidance.

**Fix**
Implemented the full v0.7 GGS spec:
- New `internal/roles/ggs/` package (R7): subscribes to `MsgReplanRequest` (from R4b), computes D/P/Ω/L/∇L, selects directive from decision table, emits `MsgPlanDirective` to R2. Handles `abandon` (Ω ≥ 0.8) directly with FinalResult.
- New `types.PlanDirective` + `types.LossBreakdown`; `types.ReplanRequest` updated (removed `GapTrend`, added `Outcomes []SubTaskOutcome`, `ElapsedMs int64`).
- `SubTaskOutcome` gains `ToolCalls []string` so GGS can derive `blocked_tools` for `break_symmetry`/`change_approach` directives.
- R4b (`metaval`): tracks task start time, sends `ReplanRequest` to R7 (not R2), includes full outcomes + elapsed_ms. Removed `computeGapTrend()` and `prevFailedCounts`. `maxReplans` kept as safety net.
- R2 (`planner`): subscribes to `MsgPlanDirective` instead of `MsgReplanRequest`. Merges GGS `blocked_tools` with memory-sourced MUST NOT constraints. New `replanWithDirective()` replaces old `replan()`.
- Auditor: `allowedPaths` updated (R4b→R7 for ReplanRequest, R7→R2 for PlanDirective). Reads gradient from PlanDirective instead of `ReplanRequest.GapTrend`.
- UI: R7 emoji (📈), `MsgPlanDirective` color/label/detail added.
- `tasklog.Replan()` signature simplified (removed `gapTrend` param).
- 36 new tests in `ggs_test.go` covering all loss/gradient computation functions.

---


## Issue #44 — GGS idle on happy path: medium loop open on task acceptance

**Symptom**
On successful task completion (all subtasks matched), GGS (R7) was completely bypassed. R4b published `MsgFinalResult` directly to the user and called `outputFn` itself. The pipeline showed no R7 activity on the happy path, violating the "medium loop is complete" invariant from the v0.7 spec.

**Root cause**
`metaval.go` accept case (verdict="accept") published `MsgFinalResult` to `RoleUser` and called `outputFn`. GGS only ran when R4b's hard gate triggered a `ReplanRequest` (i.e., only on failure). A proper closed-loop controller computes the error signal on every cycle including when the error is zero (D=0).

**Fix**
- New `types.MsgOutcomeSummary` + `types.OutcomeSummary` struct. R4b sends this to R7 instead of publishing FinalResult directly.
- R4b accept case: writes episodic memory (unchanged), then publishes `MsgOutcomeSummary` to GGS. No longer calls `outputFn` or publishes `MsgFinalResult`. Also fixed missing cleanup of `taskStart` and `replanCounts` on accept.
- GGS `Run()`: subscribes to both `MsgReplanRequest` (failure path) and `MsgOutcomeSummary` (accept path).
- GGS `processAccept()`: computes D=0, P=0.5, Ω from elapsed time + prior replans, logs final L/∇L, emits `MsgFinalResult` + calls `outputFn`. GGS is the sole emitter of FinalResult for both accept and abandon — consistent exit path.
- Auditor: `MsgOutcomeSummary → {R4b, R7}` added to allowedPaths.
- UI: `MsgOutcomeSummary` shown in pipeline flow (R4b ──[OutcomeSummary]──► R7) with green colour.
- 4 new tests for `processAccept` in `ggs_test.go`.

---


## Issue #45 — All roles shared one LLM model; reasoning roles outperformed by cheaper execution models

**Symptom**
Reasoning roles (R1/R2/R4b) and execution roles (R3/R4a) all used a single `OPENAI_MODEL`. On tasks requiring
current knowledge (e.g. "today's top tech news"), R2 (planner) produced outdated results because the model
lacked the capability for time-sensitive reasoning — yet the same model was used for tool invocation where
capability matters less than speed and cost.

**Root cause**
`llm.New()` read a single `OPENAI_MODEL` env variable. All five LLM-backed roles received the same client
instance. There was no mechanism to assign a smarter model to reasoning roles and a faster/cheaper model to
execution roles.

**Fix**
- `llm.NewTier(prefix string)`: reads `{prefix}_API_KEY`, `{prefix}_BASE_URL`, `{prefix}_MODEL`;
  falls back to `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` for any unset var.
  `New()` is kept as a backward-compatible wrapper (`NewTier("")`).
- `main.go`: replaced single `llmClient` with `brainClient = NewTier("BRAIN")` (R1/R2/R4b)
  and `toolClient = NewTier("TOOL")` (R3/R4a).
- `.env` two-section layout: `[brain-model]` uses `BRAIN_API_KEY` / `BRAIN_BASE_URL` / `BRAIN_MODEL`;
  `[tool-model]` uses `TOOL_MODEL`; shared `OPENAI_*` vars as fallback.
- `CLAUDE.md`: documented the model tier split.
- 3 new tests in `client_test.go` covering `NewTier` expectations.

---


## Issue #46 — GGS timeBudgetMs too tight; tasks abandoned at 91% Ω due to LLM latency

**Symptom**: Tasks that ultimately succeed are abandoned mid-run with "budget pressure=91%". The abandon was triggered by the time sub-component of Ω alone: at elapsed=211 s with one replan, `Ω = 0.6*(1/3) + 0.4*(211845/120000) = 0.906 ≥ 0.8`.

**Root cause**: `timeBudgetMs = 120_000` (2 min) doesn't account for real-world LLM call latency. With kimi-k2.5 / deepseek taking 5–15 s per call, a single subtask (5–6 executor tool calls + agentval + metaval + one replan cycle) routinely takes 3–5 minutes. The gradient calculation is correct; the budget constant was calibrated for a local model.

**Fix**: Raised `timeBudgetMs` from `120_000` to `300_000` (5 min) in `internal/roles/ggs/ggs.go`.

---


## Issue #47 — Abandon message shows only subtask ID, not failure reason

**Symptom**: `❌ Task abandoned after 3 failed attempts. 1 subtask(s) failed: [a7b8c9d0-...]` — the user sees a UUID, not why it failed.

**Root cause**: The hard-gate replan path in `metaval.go` built `gapSummary` as `"N subtask(s) failed: [<ids>]"` — the `SubTaskOutcome.FailureReason` field (populated by R4a) was never extracted. The abandon message therefore showed only the opaque subtask ID.

**Fix**: At the hard-gate call site, iterate failed outcomes and join their `FailureReason` strings into `gapSummary`. Inside `triggerReplan`, do the same when composing the user-facing abandon message. Both GGS (via `ReplanRequest.GapSummary`) and the final displayed message now show the actual R4a failure reason.

---


## Issue #48 — R2 has no way to consult a stronger model for complex planning

**Symptom**: Brain model (deepseek-v3.2) must plan blindly — it cannot inspect the codebase, verify tool availability, or ask Claude for architectural insight before decomposing a task.

**Root cause**: R2's `dispatch()` made a single LLM call and parsed the result directly as a SubTask JSON array. No mechanism existed for the brain to call an external tool before finalising the plan.

**Fix**: Added an optional `cc` (Claude Code CLI) tool loop to R2's `dispatch()`:
- Brain LLM may output `{"action":"call_cc","prompt":"..."}` before the final SubTask array.
- `runCC(ctx, prompt)` shells out to `cc --print <prompt>` (60 s timeout, 4000 char truncation).
- R2 appends the response to the context and calls the brain LLM again for the final plan.
- Hard-capped at `maxCCCalls=2` per planning session to prevent loops.
- Direct `[...]` array output bypasses the tool loop entirely (backward compatible).
- System prompt updated with `OPTIONAL TOOL — cc` section describing when and how to call it.
- 5 new tests in `planner_test.go` covering cc detection, array bypass, and constant guards.

---


## Issue #49 — No UI visibility into whether R2 used cc or the brain model for planning

**Symptom**: When R2 calls `cc`, the pipeline UI shows nothing — only the debug log reveals whether cc was consulted.

**Root cause**: `MsgCCCall` / `MsgCCResponse` message types didn't exist. `runCC()` called the CLI and fed the result back silently with only `log.Printf`.

**Fix**:
- Added `MsgCCCall` and `MsgCCResponse` message types and `RoleCC = "cc"` role to `types.go`.
- Added `CCCall` and `CCResponse` payload structs (task_id, call_n, max_n, prompt, chars, response preview).
- `dispatch()` in `planner.go` now publishes `MsgCCCall` (R2 → cc) before `runCC()` and `MsgCCResponse` (cc → R2) after, carrying the 300-char response preview.
- `display.go`: added `🤖` emoji for `RoleCC`; cyan color for both cc message types; spinner labels "🤖 consulting cc..." / "📐 planning with cc insight..."; detail lines show `call N/M: <prompt>` and `<chars> chars: <response preview>`.
- `auditor.go`: added `MsgCCCall → {R2, cc}` and `MsgCCResponse → {cc, R2}` to `allowedPaths`.

---


## Issue #50 — R4a has no current date in context; cannot resolve relative dates

**Symptom**: R4a fails criterion "at least one source is within the last 6 months" when search results show relative dates ("13 days ago", "5 months ago"), even though the dates clearly resolve to within 6 months.

**Root cause**: No current date in R4a's context — it cannot resolve relative dates to absolute dates. The LLM either refuses to evaluate or defaults to a stale training date.

**Fix**: Injected `Today's date: YYYY-MM-DD` into every R4a user prompt via `time.Now().UTC().Format("2006-01-02")`. This is a general mechanism — R4a already has the intelligence to resolve relative dates once it knows the current date; no domain-specific rules were added to the system prompt.

---


## Issue #51 — No UI visibility into whether R2 used brain model or cc for planning

**Symptom**: `DispatchManifest` line in pipeline always reads "N subtasks" — no indication of whether R2 called cc or planned with the brain model directly.

**Root cause**: `DispatchManifest` carried no `cc_calls` field; `display.go` had no logic to distinguish the two paths.

**Fix**:
- Added `CCCalls int` field to `types.DispatchManifest` (0 = brain only; >0 = cc consulted N times).
- `emitSubTasks(spec, raw, ccCalls int)` now accepts and sets `CCCalls` in the manifest.
- `display.go` DispatchManifest detail renders `"N subtasks | via brain"` or `"N subtasks | via cc (N call)"`.

---


## Issue #52 — No way to enforce cc as R2's brain model or switch at runtime

**Symptom**: cc can only be an optional consultation tool called by the LLM brain; there is no way to make cc the primary planning engine, nor to switch between engines without restarting.

**Root cause**: `Planner` struct had no `brainMode` concept — `dispatch()` always called `p.llm.Chat()`.

**Fix**:
- Added `brainMode string` + `sync.RWMutex` to `Planner`.
- `New(... brainMode string)` — pass `"cc"` or `"llm"` (default); reads `R2_BRAIN` env var in `main.go`.
- `SetBrainMode(mode string)` / `BrainMode() string` — thread-safe runtime switch.
- `dispatch()` branches: `brainMode=="cc"` → `dispatchViaCCBrain()` (cc is primary, 120 s timeout); otherwise → `dispatchViaLLM()` (existing loop with optional cc consultation).
- `DispatchManifest.PlannerBrain` field propagated to UI: "via brain", "via brain + cc (N)", or "via cc (brain)".
- `/brain [cc|llm]` REPL command shows or switches the engine at runtime.

---


## Issue #53 — cc subprocess invocation fails inside a Claude Code session

**Symptom**: `R2_BRAIN=cc` always errors with `[cc error: exit status 1]`; R2 cannot plan.

**Root cause (two bugs)**:
1. `CLAUDECODE` env var is set when agsh runs inside a Claude Code session; cc detects this and refuses to launch nested sessions.
2. `cc` is a zsh alias (`https_proxy=… claude`), not a binary. `exec.Command("cc")` resolves to the system C compiler (`clang`), which rejects `--print` as an unknown flag.

**Fix**:
- `ccEnviron()` helper strips `CLAUDECODE` from the subprocess environment.
- Both `runCC()` and `dispatchViaCCBrain()` now invoke `zsh -i -c 'cc --print "$AGSH_PROMPT"'` so shell aliases are loaded. Prompt is passed via `AGSH_PROMPT` env var to avoid shell injection.

---


## Issue #54 — R4a spuriously retries correct search results because ToolCalls snippet misses leading content

**Symptom**: R4a returns `retry` for a correct, detailed search result (e.g. Trump China visit dates from Reuters/Al Jazeera). All criteria marked `met:false` with evidence "no extractable content about confirmed dates".

**Root cause**: The ToolCalls snippet was built with `lastN(result, 120)` — the last 120 chars of search output. For web search results, the useful content (article titles, dates, snippets) appears at the **beginning** of the output; the tail is typically URL metadata or closing JSON. R4a only sees the tail and correctly concludes there is no evidence.

**Fix**: Changed `lastN(result, 120)` to `firstN(result, 200)`. Nearly all tool outputs (search titles/snippets, file paths, shell results) put the relevant content at the **start**, not the end. `lastN` was only correct for ffmpeg-style commands with banners before results — and those are already handled by `headTail` in the executor's 4000-char context window. The ToolCalls evidence snippet only needs the leading content.

---


## Issue #55 — R1 owned success_criteria; mixed perception with planning

**Symptom**
R1 (Perceiver) wrote `success_criteria` in the `TaskSpec`. R2 (Planner) then wrote subtask-level criteria. This means the same semantic boundary ("what counts as success") was split across two roles with different vantage points — R1 doesn't know how R2 will decompose the task, so its criteria were often intent echoes rather than testable assertions against concrete tool output.

**Root cause**
Architectural: R1's mission is perception (translate raw input into structured intent). Criteria specification requires knowledge of the execution plan, which belongs to R2.

**Fix**
- R1 no longer outputs `success_criteria`. Its output format is reduced to `{task_id, intent, constraints, raw_input}`.
- R2 now outputs a JSON wrapper `{"task_criteria":[...],"subtasks":[...]}` instead of a raw subtask array. `task_criteria` are assertions about the COMBINED output of ALL subtasks — same quality bar as subtask-level criteria (concrete, falsifiable).
- `DispatchManifest` gains `TaskCriteria []string`; R2 sets it in `emitSubTasks` by parsing the wrapper.
- `emitSubTasks` tries wrapper parse first; falls back to raw array for backward compatibility.
- R4b's `evaluate()` now passes `manifest.TaskCriteria` to the LLM instead of `TaskSpec.SuccessCriteria`.
- R4b system prompt updated: it evaluates `task_criteria` from R2, not subtask criteria from R4a.

Files changed:
- `internal/roles/perceiver/perceiver.go` — remove `success_criteria` from prompt + output format
- `internal/roles/planner/planner.go` — wrapper output format; `emitSubTasks` parses wrapper; `TaskCriteria` set in manifest
- `internal/roles/metaval/metaval.go` — system prompt + `evaluate()` user prompt use `task_criteria`
- `internal/types/types.go` — `DispatchManifest.TaskCriteria []string` field added

---


## Issue #56 — Gradient direction invisible in pipeline display

**Symptom**
The `PlanDirective` pipeline line showed `directive | ∇L=gradient Ω=N%` — the gradient label was present but the direction was not visually distinct from surrounding text. Users familiar with the GGS design could not see at a glance whether the solver was converging.

**Root cause**
`msgDetail` for `MsgPlanDirective` used a plain string with no directional symbol or color differentiation.

**Fix**
Updated `msgDetail` in `display.go` to prepend a colored directional arrow before the gradient label:
- `↑` green — improving (loss decreasing)
- `↓` red — worsening (loss increasing)
- `⊥` yellow — plateau (stuck in local minimum)
- `→` no color — stable

After the colored arrow, `ansiReset + ansiYellow` restores the message-type color so the rest of the label renders correctly within the bracket.

Files changed:
- `internal/ui/display.go` — `MsgPlanDirective` case in `msgDetail`

---


## Issue #57 — R1 and R2 resolve relative dates incorrectly without knowing current date

**Symptom**
Input: `今年春节期间重要的科技新闻` ("important tech news during this year's Spring Festival").
Today is 2026-02-22. R1 resolved "今年春节" as "2025年春节期间（1月28日-2月4日）" — wrong year (2025 vs 2026), same root cause as issue #50 but in R1 instead of R4a.

**Root cause**
R1 and R2 had no `Today's date` injection. Without knowing the current date, temporal references like "今年" (this year), "最近" (recently), "上周" (last week) are resolved from training data, which may lag by months or years.

**Fix**
Two-part fix that respects the R1/R2 role boundary:

- **R1**: add a "temporal reference rule" to the system prompt — R1 must NOT resolve relative time words (今年/this year, 最近/recently, 上周/last week, etc.) into specific dates. Preserve them verbatim in `intent`. This is architecturally correct: R1 is a receiver, not a resolver; R2 owns temporal interpretation.

- **R2**: inject `Today's date: YYYY-MM-DD` into the user prompt in `plan()` and `replanWithDirective()`. R2 needs the concrete date to write falsifiable `task_criteria` (e.g. "output contains news from Spring Festival 2026, Jan 28–Feb 4"). This is the same mechanism as R4a (issue #50) and is general, not badcase-specific.

Files changed:
- `internal/roles/perceiver/perceiver.go` — temporal reference rule added to system prompt; date injection removed
- `internal/roles/planner/planner.go` — `Today's date` prepended in `plan()` and `replanWithDirective()`

---


## Issue #58 — R3 LLM brain loops on identical tool call when DuckDuckGo returns no results

**Symptom**
R3 called `search("Google Spring Festival 2026 announcement")` 10 times in a row with identical parameters, producing identical empty/error results each iteration. Budget was exhausted without progress; task abandoned with D=1.000, Ω=0.864.

**Root cause**
When a tool call returns no usable results (DDG connection reset, empty response), the LLM re-plans with the same call because nothing in its context indicates the call was already tried. There is no guard against consecutive identical calls.

**Fix**
Added loop detection in `executor.go` `Run()` method. Before executing each tool call, compute `currentSig = tool + ":" + firstN(params, 60)`. If `currentSig` matches the immediately preceding call signature, block execution and inject a `⚠️ DUPLICATE CALL BLOCKED` warning into `toolResultsCtx`. The warning explicitly instructs the LLM to either emit a final result from existing data or try a completely different approach. The blocked call is not appended to `ToolCalls` (no evidence fabricated).

Files changed:
- `internal/roles/executor/executor.go` — consecutive duplicate call detection and blocking

---


## Issue #59 — DDG search routed through CC internal proxy (port 26560), connection fails

**Symptom**
`search` tool always errors: `websearch: http request: Post "https://html.duckduckgo.com/html/": read tcp ...:55573->...:26560: connection reset by peer`. Port 26560 is Claude Code's internal proxy.

**Root cause**
`websearch.go` used `http.DefaultClient`, which inherits `HTTPS_PROXY` from the process environment. When `agsh` runs inside a Claude Code session, CC injects its own proxy into the environment. DDG must be reached directly; CC's proxy does not forward external traffic.

**Fix**
Replace `http.DefaultClient` with a package-level `ddgClient` that sets `Transport.Proxy` to a no-op function (`func(*http.Request) (*url.URL, error) { return nil, nil }`). This bypasses all proxy env vars for DDG requests only; the LLM client (which legitimately needs the proxy to reach the API) is unaffected.

Files changed:
- `internal/tools/websearch.go` — dedicated `ddgClient` with proxy disabled

---


## Issue #60 — cc-as-brain removed; LLM restored as sole R2 brain

**Symptom**: cc-brain mode (`R2_BRAIN=cc`) failed in two ways: (1) `cc --print` rejected inside Claude Code sessions due to the `CLAUDECODE` env var; (2) even after stripping that var, cc prepended reasoning prose before the JSON, defeating the `StripFences`-then-parse pipeline.

**Root cause**: cc is a conversational assistant that cannot reliably be constrained to pure JSON output when called as a subprocess. The `cc` alias also cannot be invoked via `exec.Command` (it is a shell alias, not a binary).

**Fix**: Removed all cc-related code entirely — `ccEnviron`, `runCC`, `dispatchViaCCBrain`, `SetBrainMode`, `BrainMode`, `brainMode` field, `mu` mutex, `maxCCCalls` constant, `MsgCCCall`/`MsgCCResponse` message types, `CCCall`/`CCResponse` payload structs, `RoleCC`, `PlannerBrain`/`CCCalls` manifest fields, `/brain` REPL command, cc sections in `display.go` and `auditor.go`. `dispatch()` is a single plain LLM call again.

---


## Issue #61 — Reasoning model `<think>` blocks cause JSON parse failure in Executor

**Symptom**: Task abandoned with "Infrastructure/executor error: LLM output contained malformed JSON with stray `</think>` token between tool calls." R4a classifies this as an infrastructure error and marks the subtask failed immediately — no retry is attempted.

**Root cause**: Reasoning models (e.g. `deepseek-reasoner`) emit `<think>...</think>` blocks in raw response content before (or occasionally between) JSON objects. `StripFences` only removes ` ``` ` code fences; `<think>` tags passed through to `json.Unmarshal`, which fails with `invalid character '<'`. The error propagated from `execute()` → `RunSubTask()` → R4a as a synthetic failed `ExecutionResult` whose output is the raw error string.

**Fix**: Added `StripThinkBlocks(s string) string` to `internal/llm/client.go`. Iteratively removes all `<think>...</think>` blocks; truncates at an unclosed `<think>` tag. `StripFences` now calls `StripThinkBlocks` as its first step, so all callers (executor, planner, perceiver, agentval) are protected automatically. 4 new tests in `client_test.go`.

---


## Issue #62 — Trajectory checkpoints missing from pipeline display

**Symptom**: The pipeline flow lines for `SubTaskOutcome`, `ReplanRequest`, and `FinalResult` showed only minimal detail. `SubTaskOutcome` failed only showed the unmet criterion (no R4a score). `ReplanRequest` showed only the gap summary (no "N/M failed" count). `FinalResult` had no flow line at all — only the `endTask` footer appeared. GGS integration path (R4b → R7 → R2) had no bus-level tests.

**Root cause**: (1) `SubTaskOutcome` detail didn't include `GapTrajectoryPoint.Score`. (2) `ReplanRequest` detail didn't compute failed/total from `Outcomes`. (3) `printFlow` skipped `FinalResult` entirely (comment said "surfaced via endTask"). (4) `FinalResult` type had no loss fields — GGS computed D/P/Ω/∇L but discarded them after logging. (5) No integration tests verified the R4b→R7→R2 bus flow.

**Fix**:
- `types.go`: Added `Loss LossBreakdown`, `GradL float64`, `Replans int` to `FinalResult`.
- `ggs.go`: Set `Loss`, `GradL`, `Replans` on `FinalResult` in both `processAccept` and the abandon path of `process()`.
- `display.go msgDetail`: `SubTaskOutcome` failed → `"failed | score=X.XX | unmet: criterion"`; `ReplanRequest` → `"N/M failed | gap_summary"`; new `FinalResult` case → `"D=X.XX ∇L=±X.XX Ω=X%"` (+ `| N replan(s)` when replans > 0).
- `display.go printFlow`: Removed early return for `MsgFinalResult` so the trajectory checkpoint is always shown.
- `display.go Run`: Detects abandon (Loss.D > 0) to pass `success=false` to `endTask`.
- `display.go dynamicStatus`: Added `MsgReplanRequest` case → `"📊 N/M subtasks failed — computing gradient..."`.
- `display_test.go`: Updated `TestMsgDetail_SubTaskOutcome_FailedWithUnmetCriteria`; added 5 new tests.
- `ggs_integration_test.go`: 5 bus-level integration tests (change_path, break_symmetry, refine/improving, abandon→FinalResult, accept→FinalResult with D=0).

---


## Issue #63 — Spec-vs-code gaps: criterion-level D/P, structured failure_class, GGS thrashing detection

**Symptom**: GGS computes D at subtask granularity (failed subtasks / total subtasks) and P via keyword heuristics on `FailureReason` strings. Spec defines criterion-level D (`failed_criteria / total_criteria`) and structured `failure_class`-based P. R4a had no `failure_class` field in its criterion output, so GGS had no structured signal. Auditor lacked GGS thrashing detection (consecutive `break_symmetry` without D decreasing). Abandon-path summary was a single inline format string with no enumeration of completed/failed intents.

**Root cause**: `CriteriaVerdict` type was absent from `types.go`; `SubTaskOutcome` had no `CriteriaVerdicts` field; `GapTrajectoryPoint` had no `FailureClass`; `CorrectionSignal` had no `FailedCriterion`/`FailureClass`. R4a prompt did not instruct the LLM to classify `failure_class`. `computeD` counted subtasks, not criteria. `computeP` had no structured path, only keywords. Auditor had no `breakSymCount`/`lastBreakSymD` state.

**Fix**:
- `types.go`: Added `CriteriaVerdict` struct; added `CriteriaVerdicts []CriteriaVerdict` to `SubTaskOutcome`; added `FailureClass` to `GapTrajectoryPoint`; added `FailedCriterion` and `FailureClass` to `CorrectionSignal`.
- `agentval.go`: Added `FailureClass` to `criterionResult`; updated system prompt to request `failure_class` on failed criteria; added `aggregateFailureClass` and `toCriteriaVerdicts` helpers; trajectory building now sets `GapTrajectoryPoint.FailureClass`; `outcome()` now accepts and forwards `criteriaVerdicts`; `CorrectionSignal` building now populates `FailedCriterion`/`FailureClass`.
- `ggs.go`: `computeD` rewritten to use `CriteriaVerdicts` when present (criterion-level), with subtask-level fallback; old `computeP` renamed to `computePKeyword`; new `computeP` uses structured `FailureClass` from `CriteriaVerdicts`, falls back to `computePKeyword`; added `buildAbandonSummary` enumerating completed/failed intents; abandon path uses `buildAbandonSummary` instead of inline string.
- `auditor.go`: Added `breakSymCount`/`lastBreakSymD` maps; GGS thrashing detection block in `MsgPlanDirective` handler — fires `ggs_thrashing` anomaly after 2+ consecutive `break_symmetry` without D decreasing; resets counter on non-`break_symmetry` directive.
- `agentval_test.go` (new): Tests for `aggregateFailureClass` (5 cases) and `toCriteriaVerdicts` (6 cases).
- `ggs_test.go`: Added `TestComputeD_CriterionLevelHigherThanSubtaskLevel`, `TestComputeD_FallsBackToSubtaskLevelWhenNoCriteriaVerdicts`, `TestComputeP_AllLogicalCriteriaReturnsOne`, `TestComputeP_AllEnvironmentalCriteriaReturnsZero`, `TestComputeP_FallsBackToKeywordWhenNoStructuredClass`.
- `auditor_test.go` (new): `TestDetectGGSThrashing_FiredAfterTwoConsecutiveWithNoDDecrease`, `TestDetectGGSThrashing_NotFiredWhenDDecreases`, `TestDetectGGSThrashing_ResetOnNonBreakSymmetryDirective`.

---


## Issue #64 — Laws 1, 2, 3 from ARCHITECTURE.md not implemented

**Symptom**: Laws 1, 2, 3 from ARCHITECTURE.md marked "not yet implemented". Executor would execute destructive shell commands (rm -rf, mkfs, etc.) and overwrite existing files without any gate. GGS had no kill-switch for consecutive worsening replans. Procedural `MemoryEntry` had no `failure_class` field — future tasks could not filter memory by failure type.

**Root cause**: No irreversible-action gate in executor; no consecutive-worsening kill-switch in GGS; procedural `MemoryEntry` derived `failure_class` from free-text `gap_summary` keywords instead of structured `CriteriaVerdicts`.

**Fix**:
- Law 1 — `isIrreversibleShell` + `isIrreversibleWriteFile` in `executor.go:runTool`; both return a `[LAW1]` prefixed string on block (no error, treated as a tool result); R4a prompt adds "Law 1 safety rule" — `[LAW1]` output → immediate `failed` verdict with `failure_class=environmental`.
- Law 2 — `worseningCount map[string]int` added to GGS struct; `process()` increments count on `worsening` gradient and resets on non-worsening; after 2 consecutive worsening gradients the directive is overridden to `abandon` with a `[R7] LAW2 KILL-SWITCH` log line; `worseningCount` cleaned up on both abandon and accept exit paths.
- Law 3 — `aggregateFailureClassFromOutcomes` helper in `metaval.go` counts `fail`-verdict `CriteriaVerdicts` across failed outcomes; `failureLesson` struct gains `FailureClass` field; procedural `MemoryEntry` carries structured `failure_class` and a `failure_class:<value>` tag for R2 memory queries.

---


## Issue #65 — Law 1 bypass: model uses `find -delete` after `rm` is blocked

**Symptom**: Manual test of "delete all .log files in /tmp" showed that after `rm -v /tmp/*.log` was blocked by Law 1 (correct), the planner replanned and the model issued `find /tmp -maxdepth 1 ... -delete`, which bypassed the Law 1 gate and deleted files.

**Root cause**: `isIrreversibleShell` only checked prefix patterns (`rm `, `rmdir`, etc.). `find ... -delete` and `find ... -exec rm` are equally destructive but start with `find `, which is otherwise read-only.

**Fix**: Added two additional checks in `isIrreversibleShell` for `find` commands: (1) contains ` -delete` → blocked; (2) contains `-exec rm` or `-exec /bin/rm` → blocked. Added three tests: `ReturnsTrueForFindDelete`, `ReturnsTrueForFindExecRm`, `ReturnsFalseForFindWithoutDelete`.

---


## Issue #66 — Law 1 bypass: `rm` embedded in for-loop body not detected

**Symptom**: Second manual test of "delete all .log files in /tmp" showed that after both `rm -v` and `find -delete` were blocked (correct), the planner replanned and the model issued `for file in ...; do if [ -f "$file" ]; then rm "$file"; fi; done` — a for-loop with `rm` in the `then` branch. The loop body starts with `if`, not `rm`, so `isIrreversibleShell` did not detect it.

**Root cause**: `isIrreversibleShell` checked only the full command string against prefix patterns. Compound commands produced by loops (`for`/`while`), conditionals (`if/then/else`), and pipelines (`| xargs rm`) embed the destructive sub-command after separators (`;`, `&&`, `|`) and shell keywords (`then`, `do`), making it invisible to prefix matching.

**Fix**: Refactored `isIrreversibleShell` into two helpers: `splitShellFragments` splits the command on `&&`, `||`, `;`, `|`, and `\n`, then strips leading shell keywords (`then`, `do`, `else`, `{`, `(`); `isIrreversibleFragment` applies the prefix/pattern checks to a single normalized fragment. `isIrreversibleShell` now iterates all fragments and returns true on the first destructive one. Also added `xargs rm` and `xargs /bin/rm` as prefix patterns to catch `find ... | xargs rm` pipes. Tests added: `ReturnsTrueForForLoopWithRm`, `ReturnsTrueForAndAndRm`, `ReturnsTrueForXargsRm`, `ReturnsFalseForReadOnlyPipeline`, and six `splitShellFragments` unit tests.

---


## Issue #67 — Safety-net abandon emits FinalResult with D=0.0; UI shows green (false success)

**Symptom**: After the Law 1 gate forced the agent to exhaust its replan budget, the
pipeline display rendered the final FinalResult line in green (`D=0.00`) instead of red,
because the `endTask(success=false)` rule triggers on `FinalResult.Loss.D > 0`.

**Root cause**: The metaval safety-net abandon path (triggered after `maxReplans=3` rounds
in `triggerReplan`) published `FinalResult` with a zero-value `Loss` struct, leaving
`Loss.D = 0.0`. GGS is bypassed on this path so it never computed D from the outcomes.

**Fix**: Added `safetyNetLoss(outcomes []types.SubTaskOutcome) types.LossBreakdown` helper
that computes D as `failed / total`; falls back to D=1.0 when outcomes is empty or all
matched (the invariant is that if the safety net fired, the task failed). The safety-net
`FinalResult` now carries `Loss: safetyNetLoss(outcomes)` and `Replans: replanCount` so the
UI abort-detection rule (`D > 0`) fires correctly and the replan count appears in the flow
line. Five tests added for `safetyNetLoss`.

---


## Issue #68 — Batch B: Law 0 misattribution, Law 2 prompt gap, Law 3 context cap

**Symptom**:
- **B1 (Law 0)**: R4a LLM misattributes failure_class (e.g., calls "permission denied" logical); deterministic environmental patterns were left to LLM discretion.
- **B2 (Law 2)**: `planDirectivePrompt` described directive labels but never told R2 what `failure_class` means for replanning strategy; R2 often repeated the same approach even when signalled "environmental".
- **B3 (Law 3)**: `toolResultsCtx` accumulated unbounded across up to 10 tool calls (up to 40 KB); only per-result truncation existed; the total context injected into each LLM turn was uncapped.

**Root cause**: Three spec items that were marked "pending implementation" in ARCHITECTURE.md.

**Fix**:
- **B1** (`internal/roles/agentval/agentval.go`): added `classifyEnvironmental(evidence, toolCalls)` using compiled case-insensitive regex against: `permission denied`, `no such file`, `not found`, `not exist`, `connection refused`, `timeout`, `network error`, `command not found`, `executable file not found`, `[LAW1]`. After LLM verdict is parsed, post-processes each failed `criterionResult`: promotes to `"environmental"` if pattern matches; never demotes existing `"environmental"`. (+5 tests)
- **B2** (`internal/roles/planner/planner.go`): appended failure_class guidance block to `planDirectivePrompt` explaining that `"environmental"` → change path/parameters, `"logical"` → change tool class/method, `"mixed"` → fix environmental blockers first.
- **B3** (`internal/roles/executor/executor.go`): applied `headTail(toolResultsCtx.String(), 8000)` before appending accumulated context to prompt; keeps first ~2667 chars and last ~5333 chars. (+2 headTail tests)

Files changed:
- `internal/roles/agentval/agentval.go` (+5 tests in `agentval_test.go`)
- `internal/roles/planner/planner.go`
- `internal/roles/executor/executor.go` (+2 tests in `executor_test.go`)

---


## Issue #69 — No per-role LLM cost/time reporting after task completion

**Symptom**: After each task the user had no visibility into how many tokens each role consumed or how long each LLM call took. The task log captured per-call token counts but the data was never surfaced to the terminal.

**Root cause**:
1. `llm.Usage` had no `ElapsedMs` field; HTTP round-trip time was measured but discarded.
2. `tasklog.LLMCall` accumulated tokens globally but had no per-role breakdown.
3. `tasklog.Registry.Close` discarded role stats on close.
4. `perceiver.Process` ignored the `llm.Usage` returned by `Chat`; no way for `main.go` to track perceiver cost.

**Fix**:
- `internal/llm/client.go`: added `ElapsedMs int64` to `Usage`; timer wraps `httpClient.Do` + `io.ReadAll`.
- `internal/tasklog/tasklog.go`: added `RoleStat` (exported) + `roleStat` accumulator inside `TaskLog`; `LLMCall` signature gains `elapsedMs int64` (before `iterIndex`); new `RoleStats() []RoleStat` sorted by canonical order; `Registry` gains `cache map[string][]RoleStat`; `Close` saves stats to cache; new `GetStats(taskID)` returns and deletes cache entry.
- `internal/roles/perceiver/perceiver.go`: `Process` now returns `(string, llm.Usage, error)`; `perceive` returns `llm.Usage`; usage accumulated across clarification rounds.
- `internal/roles/planner/planner.go`, `executor/executor.go`, `agentval/agentval.go`, `metaval/metaval.go`: pass `usage.ElapsedMs` to `LLMCall`.
- `cmd/agsh/main.go`: capture `perceiverUsage` from `p.Process()`; new `printCostStats(perceiverUsage, roleStats)` prints a `📊 Cost` table with per-role call count, token count, and LLM time; called after `printResult` in both one-shot and REPL modes.

Files changed:
- `internal/llm/client.go`
- `internal/tasklog/tasklog.go` (+6 new tests in `tasklog_test.go`)
- `internal/roles/perceiver/perceiver.go`
- `internal/roles/planner/planner.go`
- `internal/roles/executor/executor.go`
- `internal/roles/agentval/agentval.go`
- `internal/roles/metaval/metaval.go`
- `cmd/agsh/main.go`

---


## Issue #70 — Agent-generated files land in project root, polluting VCS

**Symptom**: Files created by the agent as task output (Python scripts, Markdown reports,
generated data) were written relative to CWD (the project root), appearing as untracked
VCS noise and requiring manual cleanup.

**Root cause**: `write_file` resolved relative paths against the process working directory.
No designated output directory existed; the executor prompt gave no guidance on where
to place generated files.

**Fix**:
- `internal/tools/workspace.go` (new): `WorkspaceDir()` returns `$AGSH_WORKSPACE` or
  `~/agsh_workspace`; `ExpandHome()` expands `~/`; `ResolveOutputPath()` redirects bare
  filenames (no directory component) and `./`-prefixed paths to the workspace; `EnsureWorkspace()`
  creates the directory.
- `internal/roles/executor/executor.go`: in `write_file` handler, expand `~` then call
  `ResolveOutputPath`; logs redirect. Executor system prompt updated: output files MUST
  use `~/agsh_workspace/` as base.
- `cmd/agsh/main.go`: calls `tools.EnsureWorkspace()` at startup.
- `CLAUDE.md`: updated `write_file` tool table row.

Files changed:
- `internal/tools/workspace.go` (new, +8 tests in `workspace_test.go`)
- `internal/roles/executor/executor.go`
- `cmd/agsh/main.go`
- `CLAUDE.md`

---


## Issue #71 — REPL: CJK fullwidth punctuation causes line repetition; no multi-line input

**Symptom**:
1. Pasting Chinese text containing fullwidth punctuation (，。：！？【】 etc.) caused the
   same line to be repeated many times in the readline display — stale previous renders
   were not properly erased.
2. Pasting a multi-paragraph block was impossible: each embedded newline submitted the
   current buffer as a separate (incomplete) task, fragmenting the input.

**Root cause**:
1. `readline_compat/runes.go` `Width()` only counted characters in `unicode.Han`,
   `unicode.Hangul`, `unicode.Hiragana`, `unicode.Katakana` as double-wide. Fullwidth
   forms (U+FF01-U+FF60, e.g. ，。：) and other East Asian wide characters outside
   those tables were counted as width 1. The resulting undercount of visual columns made
   `idxLine` return too-small a value, so `cleanOutput` went up too few terminal lines on
   each `Refresh`, leaving stale rendered text visible.
2. No multi-line input path existed; readline submitted on every `\n`.

**Fix**:
- `internal/readline_compat/runes.go`: Added `wideExtra` `*unicode.RangeTable` covering
  U+1100-U+115F (Hangul Jamo), U+2E80-U+303E (CJK Radicals + Symbols & Punct — includes
  、。〈〉《》【】), U+FE10-U+FE19 (Vertical Forms), U+FE30-U+FE6B (CJK Compat Forms),
  U+FF01-U+FF60 (Fullwidth Forms ，。：！？), U+FFE0-U+FFE6 (Fullwidth Signs). Added
  `wideExtra` to `doubleWidth` slice so `Width()` returns 2 for all East Asian wide chars.
- `cmd/agsh/main.go`: Multi-line input via `"""` sentinel — typing `"""` alone enters
  accumulation mode (prompt changes to `... `); subsequent lines are collected; closing
  `"""` joins them with `\n` and submits as a single task; Ctrl+C cancels accumulation.

Files changed:
- `internal/readline_compat/runes.go` (5 new expectations + `wideExtra` table)
- `internal/readline_compat/runebuf_cjk_test.go` (+5 tests for fullwidth chars)
- `cmd/agsh/main.go` (multi-line `"""` sentinel in `runREPL`)

---


## Issue #72 — Three cost-tracking gaps: tool timing, task_end stats, episodic cost

**Symptom**
1. Tool execution time was not tracked — `ToolCall` had no elapsed-ms parameter, so the `📊 Cost` block showed no "tools" row.
2. `task_end` JSONL events were missing `role_stats` and `tool_elapsed_ms`, making offline analysis of per-role cost impossible.
3. Episodic `MemoryEntry` had no cost field, so R2 could not calibrate planning heuristics from past token/tool costs.

**Root cause**
- `tasklog.ToolCall()` signature lacked an `elapsedMs int64` parameter; executor called it without timing.
- `Close()` wrote a `task_end` event but only included `total_tokens`, `tool_call_count`, and `tool_elapsed_ms` were missing; `role_stats` array was absent.
- `types.MemoryEntry` had no `Cost` field; metaval never captured stats before closing the log.

**Fix**
- `internal/types/types.go`: Added `RoleCost`, `TaskCost` structs; added `Cost *TaskCost` to `MemoryEntry`.
- `internal/tasklog/tasklog.go`: Added `TaskStats` struct (`Roles []RoleStat`, `ToolCallCount int`, `ToolElapsedMs int64`); added `toolCallCount`/`toolElapsedMs` accumulators to `TaskLog`; added `Stats()` pre-close snapshot method; updated `ToolCall()` to accept `elapsedMs int64`; enriched `task_end` event with `role_stats`, `tool_call_count`, `tool_elapsed_ms`; changed `GetStats()` return type to `*TaskStats`.
- `internal/roles/executor/executor.go`: Wrapped each `runTool()` call with `time.Now()` timer; passed `toolElapsedMs` to `ToolCall`.
- `internal/roles/metaval/metaval.go`: Added `toTaskCost(*tasklog.TaskStats) *types.TaskCost` helper; snapshot stats via `tl.Stats()` before `Close()` on accept path; included `Cost` in episodic `MemoryEntry`.
- `cmd/agsh/main.go`: Updated `printCostStats` to accept `*tasklog.TaskStats`; added "tools" row showing call count and wall-clock execution time.
- `internal/tasklog/tasklog_test.go`: Fixed `ToolCall` call signatures; added 3 new tests (`TestToolStats_AccumulatesCallCount`, `TestToolStats_AccumulatesElapsedMs`, `TestTaskEnd_IncludesToolStats`).

Files changed:
- `internal/types/types.go`
- `internal/tasklog/tasklog.go`
- `internal/roles/executor/executor.go`
- `internal/roles/metaval/metaval.go`
- `cmd/agsh/main.go`
- `internal/tasklog/tasklog_test.go`

---


## Issue #73 — GGS has no mechanism to pass failed targets to R2 for environmental failures

**Symptom**
When a task fails due to environmental blockage (e.g. reuters.com returning HTTP 451), GGS correctly
identifies `failure_class = "environmental"` and emits `change_path`/`refine` directive. However,
R2 receives no structured data about *which specific targets were already tried and blocked*. Each
replan round's `PlanDirective` carried `blocked_tools: null` for environmental directives — the only
signal was free text in `gap_summary`. R2 could re-select already-tried domains (e.g. reuters.com →
bbc.com → back to reuters.com) with no code-enforced constraint against it. Additionally, failed
targets were not accumulated across rounds: if round 1 blocked reuters.com and round 2 blocked
bbc.com, round 3 had no memory of either.

**Root cause**
`deriveBlockedTools` in `ggs.go` returned nil for all directives except `break_symmetry` and
`change_approach`. Environmental failure directives (`change_path`, `refine`) never populated any
structured constraint. `PlanDirective` had no `blocked_targets` field. GGS had no per-task state
for accumulating tried targets across rounds.

**Fix**
- `internal/types/types.go`: Added `BlockedTargets []string` to `PlanDirective` (alongside
  `BlockedTools`). `BlockedTools` = tool names for logical failures; `BlockedTargets` = specific
  failed inputs for environmental failures.
- `internal/roles/ggs/ggs.go`: Added `deriveBlockedTargets()` — extracts `query`/`command`/`path`
  fields from failed subtask `ToolCalls` JSON inputs for `change_path`/`refine` directives. Added
  `appendDeduped()` helper. Added `triedTargets map[string][]string` to `GGS` struct; accumulates
  across all replan rounds per task; cleaned up on accept and abandon paths. `BlockedTargets` in
  each `PlanDirective` carries the full accumulated history.
- `internal/roles/planner/planner.go`: `replanWithDirective` now injects `blocked_targets` into
  the MUST NOT constraints block under a distinct heading
  `"GGS blocked_targets — specific inputs already tried and blocked by environment"`.
- `internal/roles/ggs/ggs_test.go`: Added 6 new tests: `NilForBreakSymmetryDirective`,
  `NilForNonFailedOutcomes`, `ExtractsQueryFromSearchToolCall`, `ExtractsCommandFromShellToolCall`,
  `DeduplicatesAcrossMultipleCalls`, `AppendDeduped_AddsNewItemsOnly`.
- `docs/mvp-roles-v0.7.md`: Updated directive semantics, Dynamic MUST NOT Injection section,
  `PlanDirective` schema, R2 Memory Calibration Protocol, Q3 resolution, Key Invariants.

Files changed:
- `internal/types/types.go`
- `internal/roles/ggs/ggs.go`
- `internal/roles/planner/planner.go`
- `internal/roles/ggs/ggs_test.go`
- `docs/mvp-roles-v0.7.md`
- `docs/issues.md`

---


## Issue #74 — Memory constraint content truncated at 180 chars, hiding key lessons from R2

**Symptom**
The `MUST NOT` / `SHOULD PREFER` constraint lines injected into R2's prompt were cut mid-sentence.
For example, a procedural entry's gap_summary `"The executor made claims about cleaning locations
without actually searching/removing..."` appeared as `"cleaning lo…"` — the actual lesson was lost.

**Root cause**
`entrySummary()` in `internal/roles/planner/planner.go` hard-capped content JSON at 180 characters.
180 chars (10 entries × 180 = 1.8 KB max) was set too conservatively; it cut long gap_summaries
before the substance of the failure was communicated to R2.

**Fix**
Raised the cap from 180 to 400 chars. At 10 entries × 400 chars = 4 KB max, the constraint block
stays well within LLM context budget while allowing a full gap_summary to reach R2 intact.
Updated the corresponding expectation comment and test assertion.

Files changed:
- `internal/roles/planner/planner.go`
- `internal/roles/planner/planner_test.go`

---


## Issue #75 — Result section shows no context about what was asked

**Symptom**
The `📋 Result` section displayed the answer with no reminder of the original question.
In REPL sessions with multiple tasks, it was easy to lose track of which answer
corresponded to which input, especially after a long pipeline run.

**Root cause**
`printResult()` in `cmd/agsh/main.go` printed only the result header and output.
The original user input was available at both call sites but never passed or displayed.

**Fix**
Added a dim `  › <question>` line immediately below `📋 Result`:
- `ClipQuestion(s string) string` added to `internal/ui/display.go`: takes only the
  first line of multi-line input, truncates at the first sentence-ending punctuation
  (`.?!。？！`) found after rune 15 (to skip abbreviations like "e.g."), falls back
  to a hard clip at 80 runes with "…".
- `printResult()` signature changed to `printResult(result types.FinalResult, rawInput string)`;
  both call sites in `runTask()` and `runREPL()` updated to pass `input`.
- 4 new tests in `internal/ui/display_test.go`:
  `ShortInputUnchanged`, `MultilineUsesFirstLine`, `TruncatesAtSentenceEnd`,
  `FallsBackToHardClipAt80Runes`.

Files changed:
- `internal/ui/display.go`
- `internal/ui/display_test.go`
- `cmd/agsh/main.go`


## Issue #76 — No warning when .env is missing or API credentials are unset

**Symptom**
Running `artoo` without a `.env` file (or with empty `OPENAI_*` vars) silently started
the REPL. The first task then failed with a cryptic HTTP connection error
(`dial tcp [::1]:...: connect: connection refused`) that gave no hint about the
missing configuration.

**Root cause**
`llm.NewTier()` created a `Client` with empty `baseURL` / `apiKey` / `model` without
any validation. `main()` did not check whether the clients were usable before starting
the bus, roles, and REPL.

**Fix**
- Added `Validate()` to `llm.Client` with 6 Expectations and 6 matching tests.
  Returns a descriptive error listing which fields (base URL, API key, model) are missing,
  including the tier label.
- `main()` calls `toolClient.Validate()` immediately after creating the LLM clients.
  If validation fails, prints a red error with guidance to copy `.env.example` and exits.
  `brainClient.Validate()` failure prints a yellow warning (BRAIN falls back to OPENAI_*).

Files changed:
- `internal/llm/client.go`
- `internal/llm/client_test.go`
- `cmd/artoo/main.go`


## Issue #77 — Search tool requires LANGSEARCH_API_KEY; unusable without paid API

**Symptom**
The `search` tool was backed by the LangSearch web search API, gated behind
`LANGSEARCH_API_KEY`. Without the key the tool was completely absent from R3's
prompt, making web search impossible for users who didn't have a LangSearch account.

**Root cause**
`SearchAvailable()` returned `false` when `LANGSEARCH_API_KEY` was unset.
`buildSystemPrompt()` conditionally excluded item #8 (search) from the executor system prompt.

**Fix**
Replaced the LangSearch backend with DuckDuckGo HTML scraping (no API key required):
- `websearch.go`: rewrote `Search()` to POST to `https://html.duckduckgo.com/html/`,
  parse organic results via regex, filter ads (href containing `duckduckgo.com/y.js`),
  strip inline HTML tags, and unescape HTML entities.
- `SearchAvailable()` now always returns `true`.
- `buildSystemPrompt()` unconditionally includes the search tool (item #8).
- Added `parseDDGResults()`, `stripHTMLTags()` with full Expectations blocks and 1:1 tests.
- Removed all `LANGSEARCH_API_KEY` / `LANGSEARCH_BASE_URL` references from `.env.example`,
  `CLAUDE.md`, and `README.md`.

Files changed:
- `internal/tools/websearch.go`
- `internal/tools/websearch_test.go`
- `internal/roles/executor/executor.go`
- `.env.example`
- `CLAUDE.md`
- `README.md`

---

## Issue #78 — GGS v0.8: decision table refactored, `success` macro-state, `PrevDirective` tracking

**Symptom**
GGS v0.7 decision table used ∇L sign as the primary split, leading to two problems: (1) improving
loss with a logically wrong approach returned "refine" instead of "change_approach" (case 3.3 —
the system may be hallucinating success or criteria-gaming); (2) D = 0 was required for acceptance,
causing wasteful replanning when the task was already "close enough".

**Root cause**
v0.7 `selectDirective` used the gradient label ("improving"/"plateau"/"worsening") as its primary
split, conflating trajectory noise with approach quality. The `Gradient` string in `PlanDirective`
exposed this internal label to consumers (Auditor, UI, Planner), coupling them to a noisy signal.
No convergence threshold (δ) was checked before action directives, so D = 0.05 triggered the same
replanning as D = 1.0.

**Fix**
Implemented spec v0.8 across 9 files:

- `internal/types/types.go`: Remove `Gradient string` from `PlanDirective`; add `PrevDirective string`.
  Add `Directive string` and `PrevDirective string` to `FinalResult`.
- `internal/roles/ggs/ggs.go`: Rewrite `selectDirective` with v0.8 diagnostic cascade
  (Ω → D → |∇L|+P). Add `rho` constant. Add `success` macro-state path in `process()`.
  Add `prevDirective map[string]string` to GGS struct; populated on every action directive,
  cleared on terminal paths. Add `buildSuccessSummary()` and `mergeMatchedOutputs()` helpers.
  Remove `gradient` parameter from `buildRationale()`. `processAccept()` now emits
  `Directive="accept"` and `PrevDirective` in FinalResult.
- `internal/roles/ggs/ggs_test.go`: Updated `selectDirective` tests for new signature; added
  tests for `success`, case 3.3 fix, `buildSuccessSummary`, `mergeMatchedOutputs`, `prevDirective`.
- `internal/roles/ggs/ggs_integration_test.go`: Removed `pd.Gradient` assertions; added
  `PrevDirective` checks; added `TestGGSIntegration_SuccessPath_EmitsFinalResult`.
- `internal/ui/display.go`: Abandon detection changed from `fr.Loss.D > 0` to
  `fr.Directive == "abandon"` (correct for the `success` path where D ≤ δ but D > 0).
  `msgDetail` for PlanDirective: arrow now from `pd.GradL` sign; shows `prev→directive` transition.
- `internal/ui/display_test.go`: Removed `Gradient:` from PlanDirective struct literals.
- `internal/roles/planner/planner.go`: Log line uses `pd.PrevDirective`; updated `refine`
  directive description to remove "Loss is decreasing" (no longer always true in v0.8).
- `internal/roles/auditor/auditor.go`: Gap trend and convergence failure detection now uses
  `pd.GradL` threshold (±0.1) instead of `pd.Gradient` string.
- `internal/roles/auditor/auditor_test.go`: Removed `Gradient:` from test fixture.

---

## Issue #79 — MetaVal safety-net FinalResult missing `Directive: "abandon"` → UI shows ✅ on abandoned task

**Symptom**
When a task is abandoned via MetaVal's maxReplans safety net (not GGS's own abandon path),
the pipeline footer shows `✅` even though the result summary clearly shows `❌ Task abandoned
after N failed attempts`.

**Root cause**
`display.go` changed abandon detection from `fr.Loss.D > 0` to `fr.Directive == "abandon"` in
issue #78 to correctly handle the v0.8 `success` macro-state (where D ≤ δ but D > 0). However,
MetaVal's safety-net path in `triggerReplan()` (metaval.go:414) emitted `FinalResult` without
setting `Directive`, leaving it as `""`. The GGS abort path correctly sets `Directive: "abandon"`,
but the MetaVal safety-net did not.

**Fix**
`internal/roles/metaval/metaval.go`: Add `Directive: "abandon"` to the safety-net FinalResult
payload. Updated `safetyNetLoss` comment to remove the stale `D > 0 → failure` UI reference.

---

## Issue #80 — MetaVal `evaluate()` silent return on JSON parse error causes infinite hang

**Symptom**
Pipeline stalls at "subtask matched — merging..." indefinitely. The auditor's 5-minute
periodic report fires mid-task (visible in the UI), but no FinalResult is ever emitted.
The task disappears with no result.

**Root cause**
Two related failures:

1. **Parse error**: R4b's LLM response contained Chinese news text with ASCII double-quote
   characters (`"`) inside the `merged_output` string value (e.g., `"隔夜酒不算酒驾"`).
   JSON terminates the string at the first unescaped `"`, leaving the parser inside the
   object where the next UTF-8 byte (`0xE9`, first byte of `隔` = U+9694) is reported as
   the invalid rune U+00E9 = `é` — hence "invalid character 'é' after object key:value pair".

2. **Silent failure**: `evaluate()` handled the parse error with a bare `return`:
   ```go
   if err := json.Unmarshal(...); err != nil {
       log.Printf(...)
       return   // no FinalResult, no replan — task hangs forever
   }
   ```

**Fix**
`internal/roles/metaval/metaval.go`:

- `evaluate()`: replace silent `return` with `m.triggerReplan(ctx, tracker, nil,
  totalCorrections, "metaval verdict parse error: "+err.Error())`. The task now either
  recovers or hits the maxReplans safety net cleanly instead of hanging.
- `extractJSON(s string) string`: new helper using brace-matching to strip leading/trailing
  prose that `StripFences` misses. Called as `extractJSON(llm.StripFences(raw))` before
  `json.Unmarshal`.
- System prompt: added "JSON encoding rules (MANDATORY)" block — explicitly forbids bare
  ASCII `"` inside string values, requiring Unicode curly quotes or rephrasing instead.

**Tests added** (`internal/roles/metaval/metaval_test.go`):
- `TestExtractJSON_PassesThroughPureJSON`
- `TestExtractJSON_ExtractsFromLeadingProse`
- `TestExtractJSON_ExtractsFromTrailingProse`
- `TestExtractJSON_HandlesNestedBraces`
- `TestExtractJSON_ReturnsUnchangedWhenNoBrace`
- `TestEvaluate_TriggerReplanOnParseError`

---

## Issue #81 — R5 memory: file-backed JSON store replaced with LevelDB MKCT pyramid engine

**Symptom**
The old file-backed `memory.json` store supported only keyword-scan queries over a flat list of
episodic/procedural `MemoryEntry` objects. Cross-task SOP promotion, decay-weighted avoidance,
structured distinction between approach-level and path-level failures, and per-tag convolution
were not possible. Metaval was the sole writer, bypassing GGS observability, and the memory
layer was not independently testable (it formatted prompt text internally, violating the Data
Service principle).

**Root cause**
v0.7 R5 design lacked a memory algebra. `MemoryEntry` had no decay constant, no valence (σ),
no frequency weight (f), and no tag-pair index — making temporal relevance and dual-channel
convolution impossible. Metaval wrote entries on accept/replan without GGS's routing context,
so the system could not distinguish "tool X was blocked" from "task intent Y is best-practised
via approach Z". There was no Dreamer-style background consolidation; stale entries were never
garbage-collected.

**Fix**
Full rewrite of R5 and supporting roles across 11 files:

- `go.mod`: Added `github.com/syndtr/goleveldb v1.0.0` (pure-Go LevelDB; no CGO).

- `internal/types/types.go`: Added `MsgMegram` message type (Auditor observability). Added
  `Intent string` to `ReplanRequest` and `OutcomeSummary` (GGS terminal Megram space tag).
  Added `Megram` struct (11 fields: ID, Level, CreatedAt, LastRecalledAt, Space, Entity,
  Content, State, F, Sigma, K). Added `SOPRecord`, `Potentials` structs. Added `MemoryService`
  interface (`Write`, `QueryC`, `QueryMK`, `RecordNegativeFeedback`, `Close`). Added
  deprecation comments on `MsgMemoryWrite/Read/Response`.

- `internal/roles/memory/memory.go`: Full rewrite implementing the MKCT pyramid engine:
  - `Store` backed by LevelDB with async write queue (buffered channel; fire-and-forget).
  - Key schema (using `|` separator): `m|<id>` (primary), `x|<space>|<entity>|<id>` (inverted
    index), `l|<level>|<id>` (level scan), `r|<id>` (last_recalled_at).
  - `quantizationMatrix` mapping 7 GGS states to (f, σ, k): abandon→(-1.0, 0.05),
    accept→(+1.0, 0.05), change_approach→(-1.0, 0.05), success→(+1.0, 0.05),
    break_symmetry→(+1.0, 0.05), change_path→(0.0, 0.2), refine→(+0.5, 0.5).
  - `QueryC`: prefix scan on inverted index, C-level filter, updates `r|<id>` recall timestamp.
  - `QueryMK`: dual-channel convolution `M_att=Σ|f_i|·exp(−k_i·Δt)`,
    `M_dec=Σσ_i·f_i·exp(−k_i·Δt)` → Potentials{Attention, Decision, Action}.
  - `deriveAction`: M_att<0.5→Ignore; M_dec>0.2→Exploit; M_dec<-0.2→Avoid; else→Caution.
  - `Run(ctx)`: starts Dreamer goroutine + processes write queue; drains+closes DB on Done.
  - `dreamer`: 5-min ticker calling `gcPass` (M_att<0.1 → DELETE) and
    `trustBankruptcyPass` (C-level M_dec<0.0 → demote to K, k=0.05). LLM consolidation
    deferred to Phase 2.
  - Exported helpers: `IntentSlug(intent)`, `ParseToolCall(tc)`, `QuantizationMatrix()`.

- `internal/roles/memory/memory_test.go` (new): 26 tests covering `deriveAction` (4),
  `IntentSlug` (6), `ParseToolCall` (8), and LevelDB integration tests for Write+QueryMK,
  QueryC recall update, GC pass, Trust Bankruptcy demotion, and RecordNegativeFeedback.

- `internal/roles/ggs/ggs.go`: Added `mem types.MemoryService` field. Updated `New` to
  accept mem as third argument. Added `writeTerminalMegram(intent, content, state)` — called
  before accept/success/abandon FinalResult. Added `writeMegramsFromToolCalls(outcomes,
  directive)` — called before action-state PlanDirective; emits one Megram per blocked_target.
  Both helpers publish `MsgMegram` for Auditor observability.

- `internal/roles/ggs/ggs_test.go` and `ggs_integration_test.go`: Updated all `New(b, nil)`
  calls to `New(b, nil, nil)` (12 occurrences total).

- `internal/roles/planner/planner.go`: Added `mem types.MemoryService` field. Updated `New`
  to accept mem as fourth argument. Removed bus-based memory query dance (`memoryCh`,
  `awaitingMemory`, `memoryEntries` state). Added `queryMKCTConstraints(ctx, intent) string`
  — calls `mem.QueryC` + `mem.QueryMK` synchronously, delegates formatting to `calibrateMKCT`.
  Added `calibrateMKCT(sops, pots) string` — maps Action to SHOULD PREFER / MUST NOT / CAUTION
  blocks; merges positive-σ SOPs under SHOULD PREFER and negative-σ SOPs under MUST NOT.

- `internal/roles/planner/planner_test.go`: Added 6 tests for `calibrateMKCT` covering empty
  input, Exploit/Avoid/Caution action blocks, and positive/negative σ SOP routing.

- `internal/roles/metaval/metaval.go`: Removed all `MsgMemoryWrite` bus publishes (episodic
  on accept, procedural on replan). Added `Intent: tracker.spec.Intent` to `OutcomeSummary`
  and `ReplanRequest`. Removed unused `toTaskCost()` and `failureLesson` type. GGS is now
  the sole writer to R5 on all paths.

- `cmd/artoo/main.go`: Memory path changed from `memory.json` to `memory.leveldb`. Updated
  `planner.New` and `ggs.New` call sites with new signatures.

**Tests added** (`internal/roles/memory/memory_test.go`):
- `TestDeriveAction_BelowThresholdReturnsIgnore`
- `TestDeriveAction_PositiveDecisionReturnsExploit`
- `TestDeriveAction_NegativeDecisionReturnsAvoid`
- `TestDeriveAction_NeutralDecisionReturnsCaution`
- `TestIntentSlug_AddsPrefix`
- `TestIntentSlug_CapAt3Words`
- `TestIntentSlug_Lowercase`
- `TestIntentSlug_StripNonAlnum`
- `TestIntentSlug_EmptyInput`
- `TestIntentSlug_FewerThan3Words`
- `TestParseToolCall_NoColon`
- `TestParseToolCall_ExtractsToolName`
- `TestParseToolCall_QueryField`
- `TestParseToolCall_CommandField`
- `TestParseToolCall_PathField`
- `TestParseToolCall_NoRecognizedField`
- `TestParseToolCall_MalformedJSON`
- `TestParseToolCall_StripsOutputSnippet`
- `TestWriteQueryMK_NewStoreReturnsIgnore`
- `TestWriteQueryMK_Exploit`
- `TestWriteQueryMK_Avoid`
- `TestQueryC_OnlyReturnsCLevel`
- `TestQueryC_UpdatesLastRecalledAt`
- `TestGCPass_DeletesExpiredMegrams`
- `TestGCPass_PreservesActiveMegrams`
- `TestWrite_FireAndForget`
- `TestRecordNegativeFeedback_CancelsPositivePotential`
- `TestTrustBankruptcyPass_DemotesCLevel`

**Tests added** (`internal/roles/planner/planner_test.go`):
- `TestCalibrateMKCT_EmptyReturnsEmpty`
- `TestCalibrateMKCT_ExploitIncludesShouldPrefer`
- `TestCalibrateMKCT_AvoidIncludesMustNot`
- `TestCalibrateMKCT_CautionIncludesCaution`
- `TestCalibrateMKCT_PositiveSigmaUnderShouldPrefer`
- `TestCalibrateMKCT_NegativeSigmaUnderMustNot`

---

## Issue #82 — LevelDB startup failure is silent: `log.Fatalf` goes to debug.log instead of stderr

**Symptom**
Running artoo one-shot when another artoo process already holds the LevelDB lock produces a
completely silent failure: blank stdout, blank stderr, exit code 1. The user has no indication
of what went wrong or how to fix it.

**Root cause**
`main.go` redirects the `log` package's output to `~/.artoo/debug.log` at line 59-63, before
`memory.New()` is called at line 84. When `memory.New()` fails to open LevelDB (e.g. "resource
temporarily unavailable" from a stale REPL session holding the file lock), it calls
`log.Fatalf(...)`, which writes to the already-redirected log file. The terminal sees nothing —
no error, no hint, just exit code 1.

**Fix**
`internal/roles/memory/memory.go`: Replace `log.Fatalf` with `fmt.Fprintf(os.Stderr, ...)` +
`os.Exit(1)`. Direct stderr writes bypass the `log` package's output redirection, guaranteeing
the error is always visible on the terminal regardless of when main.go redirects the log.
Added a second hint line: "Another artoo process may be running (LevelDB is single-writer).
Kill it and retry."

---

## Issue #83 — R4a accepts hallucinated output: `status:"failed"` ExecutionResult scored as "matched"

**Symptom**
Running "count code lines of this project" produced `754 lines` (wrong — Python files from
`cloc`'s hallucinated output) and was accepted as a successful result. Debug log showed R3
failing with `parse LLM output: invalid character 'T' looking for beginning of value` yet
the task completed as "accepted" with matched criteria.

**Root cause**
Two compounding bugs:

1. **R3 embeds full raw LLM response in error output.** The executor `execute()` used:
   `fmt.Errorf("parse LLM output: %w (raw: %s)", err, raw)`. The `(raw: %s)` suffix
   embedded the complete hallucinated LLM response — including fake cloc output with line
   counts — into `ExecutionResult.Output` (via `reason := err.Error()` in `RunSubTask`).

2. **R4a ignores `status:"failed"` and evaluates criteria from error message content.**
   The agentval `score()` function passes the full `ExecutionResult` JSON to the LLM
   without any pre-check for `status:"failed"`. The system prompt listed infrastructure
   errors by output content patterns but never mentioned `status:"failed"` itself. The
   LLM received the error string containing `754` (embedded cloc output), matched both
   criteria, and returned `verdict:"matched"`.

**Fix**
1. `internal/roles/executor/executor.go`: strip `(raw: %s)` from returned error; log raw
   separately to debug.log only. `ExecutionResult.Output` now contains only a clean error
   message with no embedded LLM content.

2. `internal/roles/agentval/agentval.go`: add "Executor failure rule" (highest priority)
   to `score()` system prompt: if `status` is `"failed"` → verdict `"failed"` immediately,
   do not evaluate criteria, do not retry.

---

## Issue #84 — R3 LLM outputs "Tool call: {json}" label, causing persistent parse failures and task abandonment

**Symptom**
After issue #83 was fixed, "count code lines of this project" abandoned after 3 replans.
All three planner iterations produced subtasks where R3 consistently output:
`Tool call: {"action":"tool","tool":"shell","command":"..."}` — parse fails, R4a correctly
fails (issue #83 fix working), but the task always abandons because R3 never executes.

**Root cause**
The R3 system prompt `systemPromptTail` at line 55 showed the output format as:
```
Tool call:    {"action":"tool","tool":"<name>","<param>":"<value>",...}
Final result: {"action":"result",...}
```
The `Tool call:` label in the format description taught the model to include that literal
prefix in its actual output. The model then extrapolated further: it generated the full
`Tool call: {json}\n\nTool result: {fake output}\n{final JSON}` block all in one LLM
response — hallucinating both the tool invocation, a fake result, and the final answer
without actually running anything.

The same label appeared in `correctionPrompt`.

**Fix**
`internal/roles/executor/executor.go`:
- Replaced `"Tool call:    {...}"` with `"To call a tool:\n{...}"` in both
  `systemPromptTail` and `correctionPrompt` to remove the inline `Tool call:` label.
- Added explicit instructions: "Output ONLY raw JSON — no label before it" and
  "NEVER generate fake tool output or pretend a tool ran — only output a tool call
  JSON OR a final result JSON, never both in the same response."

---

## Issue #85 — R3 parse failure: model outputs two concatenated JSON objects

**Symptom**
After issue #84 was fixed, R3 still occasionally fails with:
`parse LLM output: invalid character '{' after top-level value`

**Root cause**
The model complied with the "no label" instruction from #84 (outputs raw JSON now) but
still generates two concatenated JSON objects in one response: the tool-call JSON
immediately followed by the fake final-result JSON. E.g.:
```
{"action":"tool","tool":"shell","command":"find . -name '*.go' | wc -l"}
{"action":"result","subtask_id":"...","status":"completed","output":"23604"}
```
`json.Unmarshal` rejects trailing content after the first valid JSON value and returns
`invalid character '{' after top-level value`, causing the same parse failure loop.

**Fix**
`internal/roles/executor/executor.go`: replace `json.Unmarshal([]byte(raw), &v)` with
`json.NewDecoder(strings.NewReader(raw)).Decode(&v)` for both the `finalResult` and
`toolCall` parse attempts. `json.Decoder.Decode` reads exactly one complete JSON value
and ignores all subsequent content — the concatenated second object is silently dropped,
and the first (the actual tool call) is parsed correctly.

---

## Issue #86 — R2 suggests third-party CLIs (`cloc`, `tokei`) in subtask context field

**Symptom**
Running "count code lines of this project" caused R3 to execute `cloc` via the shell
tool. `cloc` is not installed; the command failed silently (or produced wrong output
on systems where it is installed), leading to wrong results or task failures.

**Root cause**
R2's LLM spontaneously wrote `cloc` and `tokei` into the subtask `context` field from
training knowledge, suggesting them as the preferred way to count lines. R3 followed
the suggestion and ran them via the `shell` tool. There was no instruction in R2's
system prompt prohibiting third-party CLI tools.

**Fix**
`internal/roles/planner/planner.go`: added one rule to the `Context field rules` block:
> Do NOT suggest third-party CLI tools (cloc, tokei, jq, ripgrep, fd, bat, etc.) —
> use only standard Unix commands (find, wc, grep, awk, sed, sort, du) or the
> executor's built-in tools (mdfind, glob, shell, search).

## Issue #87 — R6 Auditor blind to tool-level failure rates and LLM correction classes

**Symptom**
Log analysis revealed: `search` tool 25% error rate (connection resets), `shell` 12.9% error rate, 70/269 executor LLM calls producing invalid JSON. None of these were surfaced in audit reports — the operator had to grep debug logs to find them.

**Root cause**
The Auditor tracked only message-routing violations, gradient convergence (∇L), and GGS thrashing. It had no visibility into `ExecutionResult.Status == "failed"` (subtask-level failures) or `CorrectionSignal.FailureClass` (which distinguishes environmental tool errors from logical LLM errors).

**Fix**
- `types.go`: added `ToolHealth{ExecutionFailures, EnvironmentalRetries, LogicalRetries}` struct; added `ToolHealth` field to `AuditReport`
- `auditor.go`: added tracking fields + persistence; `process()` now watches `MsgExecutionResult` (status="failed" → `executionFailures++`) and `MsgCorrectionSignal` (by `FailureClass` → `environmentalRetries` / `logicalRetries`); idle guard includes all three; `publishReport()` snapshots and resets them
- `cmd/artoo/main.go`: `printAuditReport` shows ToolHealth section when any counter is non-zero; "No anomalies" guard updated to include ToolHealth
- `auditor_test.go`: 4 new tests covering all new expectations

## Issue #88 — No way to inspect MKCT pyramid state at runtime

**Symptom**
Operator had no user-facing way to see how many Megrams are stored per level (M/K/C/T) or read the promoted C-level SOPs/constraints without grepping LevelDB files.

**Root cause**
`MemoryService` interface only exposed `QueryC`/`QueryMK` (specific tag lookups) and `Write`. No aggregate scan existed.

**Fix**
- `types.go`: added `MemorySummary{LevelCounts map[string]int, CLevel []SOPRecord}`
- `memory.go`: added `Store.Summary()` — scans the `l|<level>|` prefix for each level to get counts; reads full Megrams for C-level entries to populate `CLevel`
- `cmd/artoo/main.go`: added `/memory` REPL command (calls `mem.Summary()` directly, no bus round-trip); added `printMemorySummary()` with colour-coded level table and C-level entry list
- `memory_test.go`: 4 new tests covering empty store, count accuracy, C-level field mapping, and non-C exclusion

---

## Issue #89 — R4a accepts fabricated success narrative when primary action failed

**Symptom**: Artoo claimed a Twitter video download succeeded and returned a file path. The actual yt-dlp invocation was killed mid-stream (no completion signal). R3 then ran `ls ~/Downloads/*.mp4`, found a pre-existing file from 9 days earlier, and wrote a fabricated prose narrative ("download completed successfully… interrupted by signal kill but file was saved"). R4a accepted it because the criteria — "output contains .mp4 path" and "output confirms download completed" — were evaluated against R3's self-written `output` prose rather than the `tool_calls` ground truth.

**Root cause**: R4a's prompt said "trust tool output as primary evidence" but did not forbid granting `met=true` based solely on R3's `output` prose when `tool_calls` contradicted it. The `tool_calls` clearly showed yt-dlp truncated with no "100%" line, and the only mp4 found was a post-hoc `ls` of a stale file. R4a had all the evidence to reject the claim but lacked an explicit rule to cross-check it.

**Fix**: Added "Evidence grounding rule" to R4a system prompt: (1) `output` is R3's own claim — treat as a CLAIM, not fact; (2) `tool_calls` is the ground truth; (3) if `output` claims success but the primary action's `tool_call` entry shows interruption/error/truncation with no completion signal → contradiction → retry; (4) post-hoc verification (ls/find/stat) after a failed primary action does not prove the primary action succeeded.

---

## Issue #91 — Pasting multi-line text fires each line as a separate command

**Symptom**
Pasting multiple lines into the REPL caused each physical line to be submitted as an independent command. For example, pasting a three-sentence task description would trigger three separate pipeline runs, each seeing only one sentence.

**Root cause**
`rl.Readline()` returns one line at a time. When the terminal receives a paste, all bytes arrive in the TTY buffer at once; readline buffers them internally and returns them one-by-one on successive calls. Each iteration of the REPL `for` loop consumed one line and dispatched it immediately, with no way to know more lines were already buffered.

**Fix**
Moved `rl.Readline()` to a dedicated goroutine that feeds a buffered channel (`rlCh`). The main loop reads from the channel and, after receiving the first line, opens a 50 ms accumulation window: additional lines arriving within 50 ms are joined with `\n` into a single input. Paste content arrives in <1 ms; manual typing between lines is always >100 ms. The `clarifyFn` closure was also updated to read from `readLine()` instead of calling `rl.Readline()` directly, preventing any concurrent access to the readline instance.

## Issue #90 — Executor burns all 10 LLM iters on a stuck search loop

**Symptom**
R3 called `search "Elon Musk X Twitter February 27 2026"` on iter 1, got results that didn't satisfy criteria, then repeated the identical call on iters 2–10. The existing loop detector blocked each duplicate but only injected a warning — the model ignored it and kept retrying, wasting 9 LLM API calls before exhausting `maxToolCalls`.

**Root cause**
The consecutive-duplicate check fires correctly on every repeated call, but after the block it issues a `continue` — meaning the loop re-runs, makes another LLM call, gets the same tool call, and blocks it again indefinitely. The `⚠️ DUPLICATE CALL BLOCKED` warning in the prompt context was insufficient to break the model out of the stuck state.

**Fix**
Added `consecutiveDuplicates` counter to the executor tool-call loop. On the first duplicate the existing warning is injected as before (one chance to recover). If the model makes the same call again (second consecutive duplicate) the subtask is immediately terminated with `status: "failed"` — avoiding the remaining budget burn. The counter resets to 0 on any non-duplicate call.
