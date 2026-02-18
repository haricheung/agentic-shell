package perceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R1 — Perceiver. Translate raw user input into a structured TaskSpec JSON object.

Output rules — choose ONE:

If the task is clear enough to act on:
{"task_id":"<short_snake_case_id>","intent":"<one-sentence goal>","success_criteria":["<verifiable outcome 1>","..."],"constraints":{"scope":null,"deadline":null},"raw_input":"..."}

If genuinely ambiguous AND the answer would materially change the plan:
{"needs_clarification": true, "question": "<single focused question>"}

No markdown, no prose, no code fences.

Field rules:
- task_id: short, descriptive, snake_case (e.g. "find_video_file", "disk_space_check"). Not a UUID.
- success_criteria: each entry must be verifiable from tool output alone — a validator reading only stdout/file-contents must be able to judge pass/fail. Bad: "task is done". Good: "output contains the file path".
- intent: one sentence, action-oriented, no filler.

Session history rules:
- Use provided history to resolve ambiguous pronouns ("it", "that", "those files") and reactions ("wrong", "again", "bullshit") — always infer intent, never ask about these.
- A negative reaction ("wrong", "no", "that's not right") means restate the previous intent with stricter success criteria or a different approach.
- Only ask for clarification when the task domain is genuinely unknown and guessing would produce useless work.`

// Perceiver is R1. It translates user input into TaskSpec via the bus.
type Perceiver struct {
	llm *llm.Client
	b   *bus.Bus
	// clarify is a function called when R1 needs user input; returns user's answer
	clarify func(question string) (string, error)
}

// New creates a Perceiver.
func New(b *bus.Bus, llmClient *llm.Client, clarifyFn func(string) (string, error)) *Perceiver {
	return &Perceiver{llm: llmClient, b: b, clarify: clarifyFn}
}

// Process takes raw user input, possibly asks a clarifying question, and publishes a TaskSpec.
// It returns the task ID so the caller can correlate the eventual FinalResult.
// sessionContext is a summary of recent REPL history; pass "" for one-shot mode.
func (p *Perceiver) Process(ctx context.Context, rawInput, sessionContext string) (string, error) {
	input := rawInput
	for {
		spec, needsClarification, question, err := p.perceive(ctx, input, sessionContext)
		if err != nil {
			return "", fmt.Errorf("perceiver: %w", err)
		}

		if !needsClarification {
			p.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RolePerceiver,
				To:        types.RolePlanner,
				Type:      types.MsgTaskSpec,
				Payload:   spec,
			})
			log.Printf("[R1] published TaskSpec task_id=%s", spec.TaskID)
			return spec.TaskID, nil
		}

		// Ask user for clarification
		answer, err := p.clarify(question)
		if err != nil {
			return "", fmt.Errorf("perceiver: clarification: %w", err)
		}
		// Append the Q&A to the input for next round; keep session context unchanged
		input = fmt.Sprintf("%s\n\nClarification: Q: %s A: %s", rawInput, question, answer)
		sessionContext = "" // already embedded in the first prompt; don't duplicate
	}
}

func (p *Perceiver) perceive(ctx context.Context, input, sessionContext string) (types.TaskSpec, bool, string, error) {
	userPrompt := input
	if sessionContext != "" {
		userPrompt = "Recent session history:\n" + sessionContext + "\n\nNew input: " + input
	}
	raw, err := p.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return types.TaskSpec{}, false, "", err
	}

	raw = llm.StripFences(raw)

	// Check for clarification request
	var clarCheck struct {
		NeedsClarification bool   `json:"needs_clarification"`
		Question           string `json:"question"`
	}
	if err := json.Unmarshal([]byte(raw), &clarCheck); err == nil && clarCheck.NeedsClarification {
		return types.TaskSpec{}, true, clarCheck.Question, nil
	}

	var spec types.TaskSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return types.TaskSpec{}, false, "", fmt.Errorf("parse TaskSpec: %w (raw: %s)", err, raw)
	}

	if spec.TaskID == "" {
		spec.TaskID = uuid.New().String()
	}
	spec.RawInput = input

	return spec, false, "", nil
}
