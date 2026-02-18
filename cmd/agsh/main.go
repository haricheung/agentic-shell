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
)

func main() {
	// Load env
	_ = godotenv.Load(".env")

	// Resolve cache dir
	homeDir, _ := os.UserHomeDir()
	cacheDir := filepath.Join(homeDir, ".cache", "agsh")

	// Build the bus — foundational, everything depends on it
	b := bus.New()

	// LLM client — shared by all LLM-backed roles
	llmClient := llm.New()

	// Infrastructure roles
	mem := memory.New(b, filepath.Join(cacheDir, "memory.json"))
	aud := auditor.New(b.Tap(), filepath.Join(cacheDir, "audit.jsonl"))

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

	// Context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nagsh: shutting down")
		cancel()
	}()

	// Start persistent goroutines
	go mem.Run(ctx)
	go aud.Run(ctx)
	go plan.Run(ctx)
	go mv.Run(ctx)

	// Subtask dispatcher: subscribes to SubTask messages and spawns paired executor/agentval goroutines
	go runSubtaskDispatcher(ctx, b, exec, av)

	// REPL or one-shot
	if len(os.Args) > 1 && os.Args[1] != "" {
		// One-shot mode
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
		runREPL(ctx, b, llmClient, resultCh, cancel)
	}
}

// runSubtaskDispatcher subscribes to SubTask and ExecutionResult messages on the bus.
// For each SubTask it spawns a paired Executor+AgentValidator goroutine set.
// It bridges ExecutionResult bus messages back to the AgentValidator's direct result channel.
func runSubtaskDispatcher(ctx context.Context, b *bus.Bus, exec *executor.Executor, av *agentval.AgentValidator) {
	subTaskCh := b.Subscribe(types.MsgSubTask)
	execResultCh := b.Subscribe(types.MsgExecutionResult)

	type subtaskState struct {
		resultCh     chan types.ExecutionResult
		correctionCh chan types.CorrectionSignal
	}

	var mu sync.Mutex
	states := make(map[string]*subtaskState) // subtask_id -> state

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-subTaskCh:
			if !ok {
				return
			}
			st, err := toSubTask(msg.Payload)
			if err != nil {
				log.Printf("[DISPATCHER] ERROR: bad SubTask payload: %v", err)
				continue
			}

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
			go exec.RunSubTask(ctx, subTask, correctionC)
			go func() {
				av.Run(ctx, subTask, resultC, correctionC)
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
	if err := p.Process(ctx, input, ""); err != nil {
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

func runREPL(ctx context.Context, b *bus.Bus, llmClient *llm.Client, resultCh <-chan types.FinalResult, cancel context.CancelFunc) {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("agsh — agentic shell (type 'exit' to quit)")

	const maxHistory = 5
	var history []sessionEntry

	for {
		fmt.Print("\nagsh> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			cancel()
			break
		}

		clarifyFn := func(question string) (string, error) {
			fmt.Printf("? %s\n> ", question)
			if scanner.Scan() {
				return scanner.Text(), nil
			}
			return "", fmt.Errorf("no input")
		}

		p := perceiver.New(b, llmClient, clarifyFn)
		if err := p.Process(ctx, input, buildSessionContext(history)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}

		// Wait for result (non-blocking with context)
		select {
		case <-ctx.Done():
			return
		case result := <-resultCh:
			printResult(result)
			// Record this turn in session history
			history = append(history, sessionEntry{Input: input, Summary: result.Summary})
			if len(history) > maxHistory {
				history = history[len(history)-maxHistory:]
			}
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
	fmt.Println("\n--- Result ---")
	fmt.Println(result.Summary)
	if result.Output != nil {
		// Pretty-print if structured
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
