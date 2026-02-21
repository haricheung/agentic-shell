package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/roles/agentval"
	"github.com/haricheung/agentic-shell/internal/roles/auditor"
	"github.com/haricheung/agentic-shell/internal/roles/executor"
	"github.com/haricheung/agentic-shell/internal/roles/ggs"
	"github.com/haricheung/agentic-shell/internal/roles/memory"
	"github.com/haricheung/agentic-shell/internal/roles/metaval"
	"github.com/haricheung/agentic-shell/internal/roles/perceiver"
	"github.com/haricheung/agentic-shell/internal/roles/planner"
	"github.com/haricheung/agentic-shell/internal/tasklog"
	"github.com/haricheung/agentic-shell/internal/types"
	"github.com/haricheung/agentic-shell/internal/ui"
)

func main() {
	// Load env
	_ = godotenv.Load(".env")

	// Resolve cache dir
	homeDir, _ := os.UserHomeDir()
	cacheDir := filepath.Join(homeDir, ".cache", "agsh")

	// Ensure cache directory exists before opening any files.
	_ = os.MkdirAll(cacheDir, 0755)

	// Redirect debug logs to file so they don't interfere with the terminal UI.
	// Tail ~/.cache/agsh/debug.log to observe internal role activity.
	if f, err := os.OpenFile(filepath.Join(cacheDir, "debug.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	// Build the bus — foundational, everything depends on it
	b := bus.New()

	// LLM clients — each tier reads {TIER}_{API_KEY,BASE_URL,MODEL},
	// falling back to the shared OPENAI_* vars for any unset tier variable.
	brainClient := llm.NewTier("BRAIN") // R2 Planner only — needs reasoning/thinking
	toolClient := llm.NewTier("TOOL")   // R1 Perceiver, R3 Executor, R4a AgentVal, R4b MetaVal

	// Infrastructure roles
	mem := memory.New(b, filepath.Join(cacheDir, "memory.json"))
	aud := auditor.New(b, b.NewTap(),
		filepath.Join(cacheDir, "audit.jsonl"),
		filepath.Join(cacheDir, "audit_stats.json"),
		5*time.Minute)

	// Sci-fi terminal UI — reads its own independent tap of every bus message
	disp := ui.New(b.NewTap())

	// Final result channel — delivers output to the REPL/one-shot handler
	resultCh := make(chan types.FinalResult, 4)

	outputFn := func(taskID, summary string, output any) {
		resultCh <- types.FinalResult{TaskID: taskID, Summary: summary, Output: output}
	}

	// Per-task structured log registry — one JSONL file per task under tasks/
	logReg := tasklog.NewRegistry(filepath.Join(cacheDir, "tasks"))

	// Logical roles
	// R2_BRAIN env var selects the planning engine: "cc" or "llm" (default).
	plan := planner.New(b, brainClient, logReg, os.Getenv("R2_BRAIN"))
	mv := metaval.New(b, toolClient, outputFn, logReg)
	gs := ggs.New(b, outputFn) // R7 — Goal Gradient Solver; sits between R4b and R2
	exec := executor.New(b, toolClient)
	av := agentval.New(b, toolClient)

	// Context — cancelled on SIGTERM or when the current mode finishes.
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM) // Ctrl+C (SIGINT) handled per-mode below
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Audit report channel — delivers R6 reports to the REPL printer.
	auditReportCh := make(chan types.AuditReport, 4)
	auditReportSub := b.Subscribe(types.MsgAuditReport)
	go func() {
		for msg := range auditReportSub {
			raw, _ := json.Marshal(msg.Payload)
			var rep types.AuditReport
			if json.Unmarshal(raw, &rep) == nil {
				select {
				case auditReportCh <- rep:
				default:
				}
			}
		}
	}()

	// Start persistent goroutines
	go mem.Run(ctx)
	go aud.Run(ctx)
	go plan.Run(ctx)
	go mv.Run(ctx)
	go gs.Run(ctx)
	go disp.Run(ctx)

	// Task abort channel: REPL sends a taskID here when Ctrl+C is pressed mid-task.
	// The dispatcher cancels all executor/agentval goroutines for that task.
	abortTaskCh := make(chan string, 4)

	// Subtask dispatcher: subscribes to SubTask messages and spawns paired executor/agentval goroutines
	go runSubtaskDispatcher(ctx, b, exec, av, abortTaskCh, logReg)

	// REPL or one-shot
	if len(os.Args) > 1 && os.Args[1] != "" {
		// One-shot mode: Ctrl+C cancels the whole task and exits.
		intrCh := make(chan os.Signal, 1)
		signal.Notify(intrCh, os.Interrupt)
		go func() {
			select {
			case <-intrCh:
				cancel()
			case <-ctx.Done():
			}
		}()

		input := strings.Join(os.Args[1:], " ")
		if err := runTask(ctx, b, toolClient, input, resultCh); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			cancel()
			os.Exit(1)
		}
		// Cancel context so memory/auditor goroutines drain their pending writes before exit
		cancel()
		// Give goroutines a moment to flush (memory drain, audit flush).
		// The channels are small; this is bounded to a few milliseconds in practice.
		time.Sleep(200 * time.Millisecond)
	} else {
		// REPL mode
		runREPL(ctx, b, toolClient, plan, resultCh, auditReportCh, cancel, cacheDir, disp, abortTaskCh)
	}
}

// runSubtaskDispatcher subscribes to DispatchManifest, SubTask, and ExecutionResult
// messages on the bus. Subtasks are dispatched in sequence-number order: all subtasks
// sharing the same sequence number run in parallel, and the next sequence group is only
// started once the current group fully completes. Outputs from each completed group are
// appended to the context of the next group so later subtasks can see earlier results
// (e.g. a "locate file" subtask feeds its path to an "extract audio" subtask).
func runSubtaskDispatcher(ctx context.Context, b *bus.Bus, exec *executor.Executor, av *agentval.AgentValidator, abortTaskCh <-chan string, logReg *tasklog.Registry) {
	manifestCh := b.Subscribe(types.MsgDispatchManifest)
	subTaskCh := b.Subscribe(types.MsgSubTask)
	execResultCh := b.Subscribe(types.MsgExecutionResult)

	type subtaskState struct {
		resultCh     chan types.ExecutionResult
		correctionCh chan types.CorrectionSignal
	}

	// taskDispatch tracks the sequential dispatch state for one parent task.
	type taskDispatch struct {
		ctx        context.Context
		cancel     context.CancelFunc
		expected   int                     // total subtasks from manifest (-1 = not yet received)
		bySeq      map[int][]types.SubTask // sequence number -> subtasks
		inFlight   int                     // subtasks currently executing
		currentSeq int                     // sequence group now running (0 = not started)
		prevOutputs []string               // outputs collected from completed sequence groups
	}

	// completionSignal is sent by each agentval goroutine on finish.
	type completionSignal struct {
		parentTaskID string
		output       any
	}
	completionCh := make(chan completionSignal, 32)

	dispatches := make(map[string]*taskDispatch) // parentTaskID -> dispatch state
	var mu sync.Mutex
	states := make(map[string]*subtaskState) // subtaskID -> executor/agentval channels

	// spawnSubtask launches one executor+agentval pair.
	spawnSubtask := func(td *taskDispatch, st types.SubTask) {
		resultC := make(chan types.ExecutionResult, 8)
		correctionC := make(chan types.CorrectionSignal, 8)
		mu.Lock()
		states[st.SubTaskID] = &subtaskState{resultCh: resultC, correctionCh: correctionC}
		mu.Unlock()

		log.Printf("[DISPATCHER] spawning executor+agentval for subtask=%s (seq=%d)", st.SubTaskID, st.Sequence)
		subTask := st
		tl := logReg.Get(subTask.ParentTaskID)
		go exec.RunSubTask(td.ctx, subTask, correctionC, tl)
		go func() {
			outcome := av.Run(td.ctx, subTask, resultC, correctionC, tl)
			mu.Lock()
			delete(states, subTask.SubTaskID)
			mu.Unlock()
			completionCh <- completionSignal{parentTaskID: subTask.ParentTaskID, output: outcome.Output}
		}()
		td.inFlight++
	}

	// dispatchSeq launches all subtasks for a given sequence number,
	// enriching their Context with outputs from previous sequences.
	dispatchSeq := func(td *taskDispatch, seq int) {
		subtasks := td.bySeq[seq]
		td.currentSeq = seq
		prevCtx := ""
		if len(td.prevOutputs) > 0 {
			prevCtx = "\n\nOutputs from prior steps (use these directly — do not re-run discovery):\n" +
				strings.Join(td.prevOutputs, "\n---\n")
		}
		log.Printf("[DISPATCHER] dispatching sequence=%d (%d subtasks)", seq, len(subtasks))
		for _, st := range subtasks {
			if prevCtx != "" {
				st.Context = st.Context + prevCtx
			}
			spawnSubtask(td, st)
		}
	}

	// minSeqAbove returns the smallest sequence number strictly above floor, or -1.
	minSeqAbove := func(td *taskDispatch, floor int) int {
		best := -1
		for seq := range td.bySeq {
			if seq > floor && (best < 0 || seq < best) {
				best = seq
			}
		}
		return best
	}

	// tryStart dispatches the first sequence group once all subtasks are buffered.
	tryStart := func(td *taskDispatch) {
		if td.expected <= 0 || td.inFlight > 0 || td.currentSeq > 0 {
			return
		}
		total := 0
		for _, sts := range td.bySeq {
			total += len(sts)
		}
		if total < td.expected {
			return // still waiting for subtask messages
		}
		if first := minSeqAbove(td, 0); first >= 0 {
			dispatchSeq(td, first)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case taskID, ok := <-abortTaskCh:
			if !ok {
				return
			}
			if td, found := dispatches[taskID]; found {
				log.Printf("[DISPATCHER] aborting task=%s", taskID)
				td.cancel()
				delete(dispatches, taskID)
			}

		case msg, ok := <-manifestCh:
			if !ok {
				return
			}
			raw, _ := json.Marshal(msg.Payload)
			var manifest types.DispatchManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				log.Printf("[DISPATCHER] ERROR: bad DispatchManifest payload: %v", err)
				continue
			}
			td, exists := dispatches[manifest.TaskID]
			if !exists {
				tCtx, tCancel := context.WithCancel(ctx)
				td = &taskDispatch{ctx: tCtx, cancel: tCancel, bySeq: make(map[int][]types.SubTask)}
				dispatches[manifest.TaskID] = td
			}
			td.expected = len(manifest.SubTaskIDs)
			log.Printf("[DISPATCHER] manifest task_id=%s expecting %d subtasks", manifest.TaskID, td.expected)
			tryStart(td)

		case msg, ok := <-subTaskCh:
			if !ok {
				return
			}
			st, err := toSubTask(msg.Payload)
			if err != nil {
				log.Printf("[DISPATCHER] ERROR: bad SubTask payload: %v", err)
				continue
			}
			td, exists := dispatches[st.ParentTaskID]
			if !exists {
				tCtx, tCancel := context.WithCancel(ctx)
				td = &taskDispatch{ctx: tCtx, cancel: tCancel, bySeq: make(map[int][]types.SubTask)}
				dispatches[st.ParentTaskID] = td
			}
			td.bySeq[st.Sequence] = append(td.bySeq[st.Sequence], st)
			tryStart(td)

		case sig, ok := <-completionCh:
			if !ok {
				return
			}
			td := dispatches[sig.parentTaskID]
			if td == nil {
				continue
			}
			// Collect output for context injection into next sequence.
			if sig.output != nil {
				var s string
				if raw, err := json.Marshal(sig.output); err == nil {
					if json.Unmarshal(raw, &s) == nil && s != "" {
						td.prevOutputs = append(td.prevOutputs, s)
					} else {
						td.prevOutputs = append(td.prevOutputs, string(raw))
					}
				}
			}
			td.inFlight--
			if td.inFlight == 0 {
				if next := minSeqAbove(td, td.currentSeq); next >= 0 {
					dispatchSeq(td, next)
				} else {
					delete(dispatches, sig.parentTaskID)
				}
			}

		case msg, ok := <-execResultCh:
			if !ok {
				return
			}
			result, err := toExecutionResult(msg.Payload)
			if err != nil {
				log.Printf("[DISPATCHER] ERROR: bad ExecutionResult payload: %v", err)
				continue
			}
			mu.Lock()
			state, found := states[result.SubTaskID]
			mu.Unlock()
			if !found {
				log.Printf("[DISPATCHER] WARNING: no state for subtask=%s (already completed?)", result.SubTaskID)
				continue
			}
			select {
			case state.resultCh <- result:
			default:
				log.Printf("[DISPATCHER] WARNING: resultCh full for subtask=%s", result.SubTaskID)
			}
		}
	}
}

func runTask(ctx context.Context, b *bus.Bus, llmClient *llm.Client, input string, resultCh <-chan types.FinalResult) error {
	scanner := bufio.NewScanner(os.Stdin)
	clarifyFn := func(question string) (string, error) {
		fmt.Printf("? %s\n> ", question)
		if scanner.Scan() {
			return scanner.Text(), nil
		}
		return "", fmt.Errorf("no input")
	}

	p := perceiver.New(b, llmClient, clarifyFn)
	if _, err := p.Process(ctx, input, ""); err != nil {
		return fmt.Errorf("perceiver: %w", err)
	}

	// Wait for final result
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-resultCh:
		printResult(result)
	}
	return nil
}

// sessionEntry records one REPL turn for context passing to the Perceiver.
type sessionEntry struct {
	Input   string
	Summary string
}

func runREPL(ctx context.Context, b *bus.Bus, llmClient *llm.Client, plan *planner.Planner, resultCh <-chan types.FinalResult, auditReportCh <-chan types.AuditReport, cancel context.CancelFunc, cacheDir string, disp *ui.Display, abortTaskCh chan<- string) {
	fmt.Println("\033[1m\033[36m⚡ agsh\033[0m — agentic shell  \033[2m(exit/Ctrl-D to quit | Ctrl+C aborts task | debug: ~/.cache/agsh/debug.log)\033[0m")

	rl, err := readline.NewEx(&readline.Config{
		Prompt:            "\033[36m>\033[0m ",
		HistoryFile:       filepath.Join(cacheDir, "history"),
		HistorySearchFold: true,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
	})
	if err != nil {
		// readline unavailable (e.g. not a TTY) — not expected in normal usage
		fmt.Fprintf(os.Stderr, "readline init error: %v\n", err)
		cancel()
		return
	}
	defer rl.Close()

	const maxHistory = 5
	var history []sessionEntry

	// Per-task state — protected by taskMu.
	var taskMu sync.Mutex
	var taskCancel context.CancelFunc
	var currentTaskID string

	// Ctrl+C during task execution (readline NOT active): abort the task only.
	// Ctrl+C during readline input arrives as readline.ErrInterrupt (handled below).
	// We never call cancel() from here — readline's ErrInterrupt handles "really quit".
	intrCh := make(chan os.Signal, 1)
	signal.Notify(intrCh, os.Interrupt)
	defer signal.Stop(intrCh)
	go func() {
		for {
			select {
			case <-intrCh:
				taskMu.Lock()
				tc := taskCancel
				tid := currentTaskID
				taskMu.Unlock()
				if tc != nil {
					tc() // cancel per-task context (unblocks waitResult)
					// Tell dispatcher to cancel the executor/agentval goroutines.
					select {
					case abortTaskCh <- tid:
					default:
					}
					disp.Abort() // close the pipeline box immediately
					fmt.Print("\r\033[K\n\033[33m⚠️  task aborted\033[0m  (type 'exit' or Ctrl+D to quit)\n")
				}
				// When idle (tc == nil), do nothing — readline's ErrInterrupt handles exit.
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		// readline handles: backspace, arrow keys, Ctrl+A/E, history (↑↓), Unicode/CJK.
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			// Ctrl+C while idle (no task running) — first press warns, close loop exits.
			fmt.Println("\n\033[2m(Ctrl+C again or type 'exit' to quit)\033[0m")
			line2, err2 := rl.Readline()
			if err2 == readline.ErrInterrupt || strings.TrimSpace(line2) == "exit" || strings.TrimSpace(line2) == "quit" {
				cancel()
				return
			}
			line = line2
			err = err2
		}
		if err != nil {
			// io.EOF (Ctrl+D) or other error → exit cleanly
			cancel()
			break
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			cancel()
			break
		}

		// /brain [cc|llm] — show or switch R2's planning engine.
		if input == "/brain" || strings.HasPrefix(input, "/brain ") {
			args := strings.TrimPrefix(input, "/brain")
			args = strings.TrimSpace(args)
			if args == "" {
				fmt.Printf("R2 brain: \033[1m%s\033[0m\n", plan.BrainMode())
			} else if args == "cc" || args == "llm" {
				plan.SetBrainMode(args)
				fmt.Printf("R2 brain switched to \033[1m%s\033[0m\n", args)
			} else {
				fmt.Println("usage: /brain [cc|llm]")
			}
			continue
		}

		// /audit — request an on-demand audit report directly from R6, bypassing the pipeline.
		if input == "/audit" {
			b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleUser,
				To:        types.RoleAuditor,
				Type:      types.MsgAuditQuery,
			})
			select {
			case rep := <-auditReportCh:
				printAuditReport(rep)
			case <-time.After(3 * time.Second):
				fmt.Println("(audit report timed out)")
			case <-ctx.Done():
				return
			}
			continue
		}

		// Per-task context: cancelling it aborts only this task, not the whole process.
		taskCtx, tCancel := context.WithCancel(ctx)
		taskMu.Lock()
		taskCancel = tCancel
		currentTaskID = "" // will be set after Process() returns the ID
		taskMu.Unlock()

		clarifyFn := func(question string) (string, error) {
			// Print the question as plain output — NOT embedded in the readline prompt.
			// A \n inside SetPrompt causes readline to miscalculate cursor position and
			// reprint the question line on every internal redraw, flooding the terminal.
			fmt.Printf("\033[33m?\033[0m %s\n", question)
			ans, err := rl.Readline()
			if err != nil {
				return "", fmt.Errorf("no input")
			}
			return strings.TrimSpace(ans), nil
		}

		disp.Resume() // lift post-abort suppression before the new pipeline starts
		p := perceiver.New(b, llmClient, clarifyFn)
		taskID, err := p.Process(taskCtx, input, buildSessionContext(history))
		if err != nil {
			taskMu.Lock()
			taskCancel = nil
			currentTaskID = ""
			taskMu.Unlock()
			tCancel()
			if taskCtx.Err() != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		// Register task ID so the signal handler can send it to the dispatcher on abort.
		taskMu.Lock()
		currentTaskID = taskID
		taskMu.Unlock()

		// Wait for the result matching this task ID.
		// Discard stale FinalResults from previously aborted tasks.
	waitResult:
		for {
			select {
			case <-taskCtx.Done():
				break waitResult
			case result := <-resultCh:
				if result.TaskID != taskID {
					continue // stale result from a previously aborted task
				}
				// Wait for the display to close the pipeline box before printing
				// the result and returning to readline. Without this, the REPL
				// goroutine can reach rl.Readline() (which draws ❯) before the
				// display goroutine calls endTask(), whose \r\033[K then erases it.
				disp.WaitTaskClose(300 * time.Millisecond)
				printResult(result)
				history = append(history, sessionEntry{Input: input, Summary: result.Summary})
				if len(history) > maxHistory {
					history = history[len(history)-maxHistory:]
				}
				break waitResult
			case rep := <-auditReportCh:
				// Periodic audit report arrived mid-task — print it then keep waiting.
				printAuditReport(rep)
			}
		}

		taskMu.Lock()
		taskCancel = nil
		currentTaskID = ""
		taskMu.Unlock()
		tCancel()

		if ctx.Err() != nil {
			return
		}
	}
}

// buildSessionContext formats the last N REPL turns into a concise string
// for the Perceiver to use as context when interpreting follow-up inputs.
func buildSessionContext(history []sessionEntry) string {
	if len(history) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, e := range history {
		fmt.Fprintf(&sb, "[%d] User: %s\n    Result: %s\n", i+1, e.Input, firstN(e.Summary, 120))
	}
	return sb.String()
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func printResult(result types.FinalResult) {
	const (
		bold  = "\033[1m"
		green = "\033[32m"
		reset = "\033[0m"
	)
	fmt.Printf("\n%s%s📋 Result%s\n", bold, green, reset)
	fmt.Println(result.Summary)
	if result.Output == nil {
		return
	}
	// If output is a plain string, print it directly so \n renders as newlines
	// rather than being JSON-encoded to visible "\n" escape sequences.
	// After bus/JSON round-trips, string values surface as either Go string
	// or interface{} containing string — handle both.
	b, err := json.Marshal(result.Output)
	if err != nil {
		fmt.Println(result.Output)
		return
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		// It's a string — print with real newlines, not JSON escapes.
		if s != result.Summary {
			fmt.Println(s)
		}
		return
	}
	// Structured output (object/array) — pretty-print as indented JSON.
	var pretty []byte
	if pretty, err = json.MarshalIndent(result.Output, "", "  "); err == nil {
		fmt.Println(string(pretty))
	} else {
		fmt.Println(result.Output)
	}
}

func printAuditReport(rep types.AuditReport) {
	const (
		bold   = "\033[1m"
		cyan   = "\033[36m"
		yellow = "\033[33m"
		red    = "\033[31m"
		dim    = "\033[2m"
		reset  = "\033[0m"
	)
	fmt.Printf("\n%s%s📡 Audit Report%s  %s%s → %s%s\n",
		bold, cyan, reset, dim, rep.Period.From, rep.Period.To, reset)
	fmt.Printf("  Tasks observed:      %d\n", rep.TasksObserved)
	fmt.Printf("  Avg corrections:     %.2f\n", rep.ConvergenceHealth.AvgCorrectionCount)
	gt := rep.ConvergenceHealth.GapTrendDistribution
	fmt.Printf("  Gap trends:          ↑improving=%d  →stable=%d  ↓worsening=%d\n",
		gt.Improving, gt.Stable, gt.Worsening)
	if len(rep.BoundaryViolations) > 0 {
		fmt.Printf("  %sBoundary violations:%s\n", yellow, reset)
		for _, v := range rep.BoundaryViolations {
			fmt.Printf("    • %s\n", v)
		}
	}
	if len(rep.DriftAlerts) > 0 {
		fmt.Printf("  %sDrift alerts:%s\n", red, reset)
		for _, d := range rep.DriftAlerts {
			fmt.Printf("    • %s\n", d)
		}
	}
	if len(rep.BoundaryViolations) == 0 && len(rep.DriftAlerts) == 0 {
		fmt.Printf("  %sNo anomalies detected.%s\n", dim, reset)
	}
	fmt.Println()
}

func toSubTask(payload any) (types.SubTask, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.SubTask{}, err
	}
	var st types.SubTask
	return st, json.Unmarshal(b, &st)
}

func toExecutionResult(payload any) (types.ExecutionResult, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.ExecutionResult{}, err
	}
	var r types.ExecutionResult
	return r, json.Unmarshal(b, &r)
}
