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

const systemPrompt = `You are R1 — Perceiver. Your mission is to translate raw user input into a structured, unambiguous TaskSpec JSON object.

Skills:
- Parse natural language into structured intent
- Use session history (if provided) to interpret contextual or reactive inputs
- Identify ambiguities; when something is genuinely ambiguous and the answer would materially change the plan, output a clarifying question instead
- Extract measurable success criteria precise enough for validators to score
- Identify scope constraints (file paths, time bounds, domains)

Session history rules (IMPORTANT):
- If recent session history is provided, use it to resolve ambiguous inputs
- Reactions like "wrong", "incorrect", "bullshit", "no", "that's not right", "again" mean the previous result was unsatisfactory — restate the previous intent as a new TaskSpec but with better success criteria or a different approach
- Pronouns and references like "it", "that", "the same", "those files" refer to the most recent task in history
- Short reactions should NEVER trigger a clarification question — infer intent from history

Output rules:
- If the task is clear enough to act on, output ONLY a valid JSON object with this schema:
  {"task_id":"...","intent":"...","success_criteria":["..."],"constraints":{"scope":null,"deadline":null},"raw_input":"..."}
- If clarification is needed, output ONLY a JSON object:
  {"needs_clarification": true, "question": "..."}
- No markdown, no prose, no code fences.`

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
// sessionContext is a summary of recent REPL history; pass "" for one-shot mode.
func (p *Perceiver) Process(ctx context.Context, rawInput, sessionContext string) error {
	input := rawInput
	for {
		spec, needsClarification, question, err := p.perceive(ctx, input, sessionContext)
		if err != nil {
			return fmt.Errorf("perceiver: %w", err)
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
			return nil
		}

		// Ask user for clarification
		answer, err := p.clarify(question)
		if err != nil {
			return fmt.Errorf("perceiver: clarification: %w", err)
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
