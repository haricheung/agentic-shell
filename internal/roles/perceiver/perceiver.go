package perceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R1 — Perceiver. Translate raw user input into a structured TaskSpec JSON object.

Output rules — choose ONE:

If the task is clear enough to act on:
{"task_id":"<short_snake_case_id>","intent":"<one-sentence goal>","constraints":{"scope":null,"deadline":null},"raw_input":"..."}

If genuinely ambiguous AND the answer would materially change the plan:
{"needs_clarification": true, "question": "<single focused question>"}

No markdown, no prose, no code fences.

Field rules:
- task_id: short, descriptive, snake_case (e.g. "find_video_file", "disk_space_check"). Not a UUID.
- intent: one sentence, action-oriented, no filler.

Temporal reference rules:
- Do NOT resolve relative time words (今年/this year, 最近/recently, 上周/last week, 昨天/yesterday, etc.) into specific dates or years.
- Preserve them verbatim in intent. R2 owns temporal interpretation with knowledge of the current date.
- Bad: intent = "find news from Spring Festival 2025 (Jan 28–Feb 4)"
- Good: intent = "find tech news from this year's Spring Festival"

Session history rules:
- Use provided history to resolve ambiguous pronouns ("it", "that", "those files") and reactions ("wrong", "again", "bullshit") — always infer intent, never ask about these.
- A negative reaction ("wrong", "no", "that's not right") means restate the previous intent with stricter success criteria or a different approach.
- A task amendment or modifier ("用中文回答"/"answer in Chinese", "详细一点"/"more detail", "用表格展示"/"show as table", "shorter", "translate that to X") means REDO the previous task with the modifier applied. Combine the previous intent with the new constraint into a single intent.
- Only ask for clarification when the task domain is genuinely unknown and guessing would produce useless work.`

// Perceiver is R1. It translates user input into TaskSpec via the bus.
type Perceiver struct {
	llm *llm.Client
	b   *bus.Bus
	mem types.MemoryService // may be nil; used by fast path to consult global:user memories
	// clarify is a function called when R1 needs user input; returns user's answer
	clarify func(question string) (string, error)
}

// New creates a Perceiver.
func New(b *bus.Bus, llmClient *llm.Client, clarifyFn func(string) (string, error), mem types.MemoryService) *Perceiver {
	return &Perceiver{llm: llmClient, b: b, clarify: clarifyFn, mem: mem}
}

// maxClarificationRounds caps how many times R1 may ask the user a clarifying question
// before giving up and proceeding with its best interpretation.
const maxClarificationRounds = 2

// chatPrompt is a lightweight system prompt used for the fast-path direct response.
// It answers simple conversational queries without the TaskSpec machinery.
const chatPrompt = `You are Artoo — a helpful AI assistant. Answer the user's question directly and concisely. Use the user's language. No JSON, no structured output — just a natural conversational reply.`

// isConversational returns true when the input is a simple conversational query
// that needs no tools, file access, or multi-step execution. These inputs bypass
// the full pipeline and get a direct LLM response.
//
// Expectations:
//   - Returns true for common greetings (hi, hello, hey, 你好, etc.)
//   - Returns true for identity questions (who are you, what are you, 你是谁)
//   - Returns true for simple factual Q&A (what is X, explain Y, translate Z)
//   - Returns false for inputs containing action verbs that need tools (find, search, open, play, etc.)
//   - Returns false for inputs longer than 100 runes (likely complex tasks)
//   - Returns false for empty input
func isConversational(input string) bool {
	s := strings.TrimSpace(input)
	if s == "" {
		return false
	}

	// Long inputs are likely complex tasks.
	if len([]rune(s)) > 100 {
		return false
	}

	lower := strings.ToLower(s)

	// Action verbs that signal tool-needing tasks — reject fast path.
	actionVerbs := []string{
		"find", "search", "open", "play", "send", "create", "delete", "remove",
		"list", "show me", "download", "install", "run", "execute", "check",
		"查找", "搜索", "打开", "播放", "发送", "创建", "删除", "下载", "安装",
		"运行", "执行", "查看", "列出", "显示",
		"remind", "提醒", "schedule", "安排", "set alarm", "设置闹钟",
	}
	for _, v := range actionVerbs {
		if strings.Contains(lower, v) {
			return false
		}
	}

	// Greetings — always fast path.
	greetings := []string{
		"hi", "hello", "hey", "yo", "sup", "howdy", "good morning", "good afternoon",
		"good evening", "good night", "thanks", "thank you", "bye", "goodbye",
		"你好", "嗨", "早上好", "晚上好", "下午好", "谢谢", "再见",
	}
	for _, g := range greetings {
		if lower == g || strings.HasPrefix(lower, g+" ") || strings.HasPrefix(lower, g+"!") ||
			strings.HasPrefix(lower, g+",") || strings.HasPrefix(lower, g+"，") {
			return true
		}
	}

	// Identity / about-me questions.
	identityPatterns := []string{
		"who are you", "what are you", "what's your name", "what is your name",
		"introduce yourself", "tell me about yourself",
		"你是谁", "你叫什么", "介绍一下你自己", "自我介绍",
	}
	for _, p := range identityPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	// Short inputs (≤ 8 words / ≤ 15 runes for CJK) without action verbs are likely conversational.
	words := strings.Fields(s)
	runes := []rune(s)
	if len(words) <= 8 && len(runes) <= 30 {
		// Additional check: starts with a question word → conversational.
		questionStarts := []string{
			"what", "who", "why", "how", "when", "where", "is ", "are ", "do ", "does ",
			"can ", "could ", "would ", "should ", "will ",
			"什么", "谁", "为什么", "怎么", "什么时候", "哪里", "是不是", "能不能",
		}
		for _, qs := range questionStarts {
			if strings.HasPrefix(lower, qs) {
				return true
			}
		}
	}

	return false
}

// ProcessResult holds the output of Perceiver.Process().
type ProcessResult struct {
	TaskID         string    // non-empty when a TaskSpec was published to the pipeline
	DirectResponse string    // non-empty when R1 answered directly (no pipeline needed)
	Usage          llm.Usage // accumulated LLM usage across all rounds
}

// Process takes raw user input, possibly asks a clarifying question, and publishes a TaskSpec.
// When the input is a simple conversational query (greeting, identity question, factual Q&A),
// R1 answers directly via ProcessResult.DirectResponse and the pipeline is skipped entirely.
// sessionContext is a summary of recent REPL history; pass "" for one-shot mode.
//
// Expectations:
//   - Returns DirectResponse for simple conversational queries that need no tools
//   - Returns TaskID for actionable tasks that need the pipeline
//   - Asks at most maxClarificationRounds clarifying questions before committing
//   - Accumulates LLM usage across all rounds
func (p *Perceiver) Process(ctx context.Context, rawInput, sessionContext string) (ProcessResult, error) {
	// Code-level fast path: detect simple conversational inputs before the LLM call
	// and answer with a lightweight chat prompt (no TaskSpec parsing, no pipeline).
	if isConversational(rawInput) {
		slog.Info("[R1] fast path detected", "input", rawInput)

		// Build the user prompt with memory + session context.
		var parts []string

		// Query global:user memories so the fast path knows identity, preferences, etc.
		if p.mem != nil {
			memories := p.queryGlobalMemories(ctx)
			if memories != "" {
				parts = append(parts, "Your stored memories (obey these):\n"+memories)
			}
		}
		if sessionContext != "" {
			parts = append(parts, "Recent session history:\n"+sessionContext)
		}
		parts = append(parts, rawInput)

		raw, usage, err := p.llm.Chat(ctx, chatPrompt, strings.Join(parts, "\n\n"))
		if err != nil {
			return ProcessResult{Usage: usage}, fmt.Errorf("perceiver: fast path: %w", err)
		}
		return ProcessResult{DirectResponse: raw, Usage: usage}, nil
	}

	input := rawInput
	var totalUsage llm.Usage
	for round := 0; round < maxClarificationRounds; round++ {
		result, needsClarification, question, usage, err := p.perceive(ctx, input, sessionContext)
		totalUsage.PromptTokens += usage.PromptTokens
		totalUsage.CompletionTokens += usage.CompletionTokens
		totalUsage.TotalTokens += usage.TotalTokens
		totalUsage.ElapsedMs += usage.ElapsedMs
		if err != nil {
			return ProcessResult{Usage: totalUsage}, fmt.Errorf("perceiver: %w", err)
		}

		if !needsClarification {
			taskID, err := p.publish(result.Spec)
			return ProcessResult{TaskID: taskID, Usage: totalUsage}, err
		}

		// Ask user for clarification
		answer, err := p.clarify(question)
		if err != nil {
			return ProcessResult{Usage: totalUsage}, fmt.Errorf("perceiver: clarification: %w", err)
		}
		// Empty answer means "just do your best" — stop asking and proceed.
		if strings.TrimSpace(answer) == "" {
			break
		}
		// Append the Q&A to the input for next round; keep session context unchanged.
		input = fmt.Sprintf("%s\n\nClarification: Q: %s A: %s", rawInput, question, answer)
		sessionContext = "" // already embedded in the first prompt; don't duplicate
	}

	// Max rounds reached or user gave empty answer — one final call with instruction to commit.
	finalInput := input + "\n\n[Instruction: proceed with the best interpretation; do not request further clarification.]"
	result, _, _, usage, err := p.perceive(ctx, finalInput, "")
	totalUsage.PromptTokens += usage.PromptTokens
	totalUsage.CompletionTokens += usage.CompletionTokens
	totalUsage.TotalTokens += usage.TotalTokens
	totalUsage.ElapsedMs += usage.ElapsedMs
	if err != nil {
		return ProcessResult{Usage: totalUsage}, fmt.Errorf("perceiver: %w", err)
	}
	taskID, err := p.publish(result.Spec)
	return ProcessResult{TaskID: taskID, Usage: totalUsage}, err
}

func (p *Perceiver) publish(spec types.TaskSpec) (string, error) {
	p.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RolePerceiver,
		To:        types.RolePlanner,
		Type:      types.MsgTaskSpec,
		Payload:   spec,
	})
	slog.Info("[R1] published TaskSpec", "task_id", spec.TaskID)
	return spec.TaskID, nil
}

// perceiveResult holds the parsed LLM output — exactly one of Spec or DirectResponse is set.
type perceiveResult struct {
	Spec           types.TaskSpec
	DirectResponse string
}

func (p *Perceiver) perceive(ctx context.Context, input, sessionContext string) (perceiveResult, bool, string, llm.Usage, error) {
	userPrompt := input
	if sessionContext != "" {
		userPrompt = "Recent session history:\n" + sessionContext + "\n\nNew input: " + input
	}
	raw, usage, err := p.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return perceiveResult{}, false, "", usage, err
	}

	raw = llm.StripFences(raw)

	// Check for clarification request.
	var clarCheck struct {
		NeedsClarification bool   `json:"needs_clarification"`
		Question           string `json:"question"`
	}
	if err := json.Unmarshal([]byte(raw), &clarCheck); err == nil && clarCheck.NeedsClarification {
		return perceiveResult{}, true, clarCheck.Question, usage, nil
	}

	var spec types.TaskSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return perceiveResult{}, false, "", usage, fmt.Errorf("parse TaskSpec: %w (raw: %s)", err, raw)
	}

	if spec.TaskID == "" {
		spec.TaskID = uuid.New().String()
	}
	spec.RawInput = input

	return perceiveResult{Spec: spec}, false, "", usage, nil
}

// queryGlobalMemories retrieves C-level SOPs and recent Megrams from the global:user
// space and formats them as a text block for inclusion in the fast-path prompt.
func (p *Perceiver) queryGlobalMemories(ctx context.Context) string {
	if p.mem == nil {
		return ""
	}
	var lines []string
	sops, err := p.mem.QueryC(ctx, "global:user", "env:local")
	if err == nil {
		for _, s := range sops {
			lines = append(lines, "- "+s.Content)
		}
	}
	recent, err := p.mem.QueryRecent(ctx, "global:user", "env:local", 3)
	if err == nil {
		for _, m := range recent {
			if m.Content != "" {
				lines = append(lines, "- "+m.Content)
			}
		}
	}
	return strings.Join(lines, "\n")
}
