module github.com/haricheung/agentic-shell

go 1.25.0

require (
	github.com/chzyer/readline v1.5.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	golang.org/x/sys v0.0.0-20220310020820-b874c991c1a5 // indirect
)

// Patched local copy: fixes getBackspaceSequence() to emit Width(rune) backspaces
// per character so CJK double-width chars move the cursor correctly.
replace github.com/chzyer/readline => ./internal/readline_compat
