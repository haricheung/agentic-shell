module github.com/haricheung/agentic-shell

go 1.25.0

require (
	github.com/chzyer/readline v1.5.1
	github.com/google/uuid v1.6.0
	github.com/joho/godotenv v1.5.1
	github.com/mattn/go-runewidth v0.0.20
)

require github.com/clipperhouse/uax29/v2 v2.2.0 // indirect

// Patched local copy: fixes getBackspaceSequence() to emit Width(rune) backspaces
// per character so CJK double-width chars move the cursor correctly.
replace github.com/chzyer/readline => ./internal/readline_compat
