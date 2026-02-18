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
	"github.com/joho/godotenv"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/roles/agentval"
	"github.com/haricheung/agentic-shell/internal/roles/auditor"
	"github.com/haricheung/agentic-shell/internal/roles/executor"
	"github.com/haricheung/agentic-shell/internal/roles/memory"
	"github.com/haricheung/agentic-shell/internal/roles/metaval"
	"github.com/haricheung/agentic-shell/internal/roles/perceiver"
	"github.com/haricheung/agentic-shell/internal/roles/planner"
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

	// LLM client — shared by all LLM-backed roles
	llmClient := llm.New()

	// Infrastructure roles
	mem := memory.New(b, filepath.Join(cacheDir, "memory.json"))
	aud := auditor.New(b.Tap(), filepath.Join(cacheDir, "audit.jsonl"))

	// Sci-fi terminal UI — reads its own independent tap of every bus message
	disp := ui.New(b.NewTap())

	// Final result channel — delivers output to the REPL/one-shot handler
	resultCh := make(chan types.FinalResult, 4)

	outputFn := func(taskID, summary string, output any) {
		resultCh <- types.FinalResult{TaskID: taskID, Summary: summary, Output: output}
	}

	// Logical roles
	plan := planner.New(b, llmClient)
	mv := metaval.New(b, llmClient, outputFn)
	exec := executor.New(b, llmClient)
	av := agentval.New(b, llmClient)

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

	// Start persistent goroutines
	go mem.Run(ctx)
	go aud.Run(ctx)
	go plan.Run(ctx)
	go mv.Run(ctx)
	go disp.Run(ctx)

	// Task abort channel: REPL sends a taskID here when Ctrl+C is pressed mid-task.
	// The dispatcher cancels all executor/agentval goroutines for that task.
	abortTaskCh := make(chan string, 4)

	// Subtask dispatcher: subscribes to SubTask messages and spawns paired executor/agentval goroutines
	go runSubtaskDispatcher(ctx, b, exec, av, abortTaskCh)

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
		if err := runTask(ctx, b, llmClient, input, resultCh); err != nil {
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
		runREPL(ctx, b, llmClient, resultCh, cancel, cacheDir, disp, abortTaskCh)
	}
}

// runSubtaskDispatcher subscribes to SubTask and ExecutionResult messages on the bus.
// For each SubTask it spawns a paired Executor+AgentValidator goroutine set.
// abortTaskCh receives task IDs to cancel; the matching executor/agentval goroutines are
// stopped immediately when a task abort is requested.
func runSubtaskDispatcher(ctx context.Context, b *bus.Bus, exec *executor.Executor, av *agentval.AgentValidator, abortTaskCh <-chan string) {
	subTaskCh := b.Subscribe(types.MsgSubTask)
	execResultCh := b.Subscribe(types.MsgExecutionResult)

	type subtaskState struct {
		resultCh     chan types.ExecutionResult
		correctionCh chan types.CorrectionSignal
	}

	// Per-task cancellable contexts so we can abort all goroutines for a given task.
	type taskCtxEntry struct {
		ctx    context.Context
		cancel context.CancelFunc
	}
	taskCtxs := make(map[string]*taskCtxEntry) // parentTaskID -> entry

	var mu sync.Mutex
	states := make(map[string]*subtaskState) // subtask_id -> state

	for {
		select {
		case <-ctx.Done():
			return

		case taskID, ok := <-abortTaskCh:
			if !ok {
				return
			}
			if entry, found := taskCtxs[taskID]; found {
				log.Printf("[DISPATCHER] aborting task=%s", taskID)
				entry.cancel()
				delete(taskCtxs, taskID)
			}

		case msg, ok := <-subTaskCh:
			if !ok {
				return
			}
			st, err := toSubTask(msg.Payload)
			if err != nil {
				log.Printf("[DISPATCHER] ERROR: bad SubTask payload: %v", err)
				continue
			}

			// Get or create a cancellable context for this parent task.
			entry, exists := taskCtxs[st.ParentTaskID]
			if !exists {
				tCtx, tCancel := context.WithCancel(ctx)
				entry = &taskCtxEntry{ctx: tCtx, cancel: tCancel}
				taskCtxs[st.ParentTaskID] = entry
			}
			taskCtx := entry.ctx

			resultC := make(chan types.ExecutionResult, 8)
			correctionC := make(chan types.CorrectionSignal, 8)

			mu.Lock()
			states[st.SubTaskID] = &subtaskState{
				resultCh:     resultC,
				correctionCh: correctionC,
			}
			mu.Unlock()

			log.Printf("[DISPATCHER] spawning executor+agentval for subtask=%s", st.SubTaskID)

			// Capture for goroutine
			subTask := st
			go exec.RunSubTask(taskCtx, subTask, correctionC)
			go func() {
				av.Run(taskCtx, subTask, resultC, correctionC)
				// Clean up state after AgentValidator completes
				mu.Lock()
				delete(states, subTask.SubTaskID)
				mu.Unlock()
			}()

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

func runREPL(ctx context.Context, b *bus.Bus, llmClient *llm.Client, resultCh <-chan types.FinalResult, cancel context.CancelFunc, cacheDir string, disp *ui.Display, abortTaskCh chan<- string) {
	fmt.Println("\033[1m\033[36m⚡ agsh\033[0m — agentic shell  \033[2m(exit/Ctrl-D to quit | Ctrl+C aborts task | debug: ~/.cache/agsh/debug.log)\033[0m")

	rl, err := readline.NewEx(&readline.Config{
		Prompt:            "\033[36m❯\033[0m ",
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

		// Per-task context: cancelling it aborts only this task, not the whole process.
		taskCtx, tCancel := context.WithCancel(ctx)
		taskMu.Lock()
		taskCancel = tCancel
		currentTaskID = "" // will be set after Process() returns the ID
		taskMu.Unlock()

		clarifyFn := func(question string) (string, error) {
			rl.SetPrompt(fmt.Sprintf("\033[33m?\033[0m %s\n\033[36m❯\033[0m ", question))
			ans, err := rl.Readline()
			rl.SetPrompt("\033[36m❯\033[0m ")
			if err != nil {
				return "", fmt.Errorf("no input")
			}
			return strings.TrimSpace(ans), nil
		}

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
				printResult(result)
				history = append(history, sessionEntry{Input: input, Summary: result.Summary})
				if len(history) > maxHistory {
					history = history[len(history)-maxHistory:]
				}
				break waitResult
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
	if result.Output != nil {
		if b, err := json.MarshalIndent(result.Output, "", "  "); err == nil {
			fmt.Println(string(b))
		} else {
			fmt.Println(result.Output)
		}
	}
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
