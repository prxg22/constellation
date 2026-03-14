# Configuration

## Manager Config

`Config` is passed to `NewManager()` to initialize the library.

Constellation currently keeps the `agents-mux` module path and binary name for compatibility. Config examples below use the library API, independent of the transitional CLI name.

```go
cfg := mux.Config{
    // Required: SQLite database file path.
    DBPath: "./data/agents.db",

    // Optional: Base directory for per-session files.
    // Default: {DBPath_dir}/sessions
    SessionDir: "./data/sessions",

    // Optional: Directory for handoff markdown files.
    // Default: {DBPath_dir}/handoffs
    HandoffDir: "./data/handoffs",

    // Optional: Per-session SSE ring buffer capacity.
    // Default: 1024
    RingBufferSize: 1024,

    // Optional: Duration before auto-handoff on idle.
    // Default: 10 minutes
    IdleTimeout: 10 * time.Minute,

    // Optional: Environment variables for spawned agent processes.
    // Default: os.Environ()
    AgentEnv: func() []string {
        return os.Environ()
    },
}
```

## Optional Interfaces

All interfaces are optional. The library provides sensible defaults or skips the feature when not configured.

### MCPConfigProvider

Generates MCP (Model Context Protocol) JSON configuration for agent subprocesses.

```go
type MCPConfigProvider interface {
    MCPConfig(sessionID, agent string) (string, error)
}
```

The returned JSON string is written to a temp file and passed via `--mcp-config` flag to the agent CLI.

### ActionSummaryFormatter

Converts tool calls into human-readable summaries for the action status stream.

```go
type ActionSummaryFormatter interface {
    FormatAction(tool string, args map[string]interface{}) string
}
```

Default: `DefaultActionFormatter` converts `tool_name` to `"Tool name"`.

### HandoffHandler

Called when a handoff is triggered (idle timeout, token exhaustion, screenshot limit).

```go
type HandoffHandler interface {
    HandleHandoff(sessionID, summary, currentState, pendingTasks string) error
}
```

Default: Writes a markdown file to `HandoffDir`.

### TitleGenerator

Generates session titles from the first user prompt.

```go
type TitleGenerator interface {
    GenerateTitle(prompt string) string
}
```

Default: Calls `claude` CLI with a 5-word summary prompt, falls back to first 5 words of the prompt.

### SummaryGenerator

Generates session summaries from conversation history.

```go
type SummaryGenerator interface {
    GenerateSummary(entries []ConversationEntry) (*SessionSummary, error)
}
```

### Transcriber

Abstracts speech-to-text for audio queue items.

```go
type Transcriber interface {
    Transcribe(audioPath, language string) (string, error)
}
```

A built-in `whisper.NewTranscriber()` implementation is provided using `whisper.cpp`.

### ToolExecutor

Handles tool calls from HTTP-based agent loops (not used by subprocess agents).

```go
type ToolExecutor interface {
    ExecuteTool(sessionID, toolName string, args map[string]interface{}) (string, error)
}
```

## Environment Variables

### Agent Process Isolation

The library automatically isolates agent subprocess environments:

| Variable | Purpose |
|----------|---------|
| `CODEX_HOME` | Isolated Codex config directory |
| `OPENCODE_CONFIG_DIR` | Isolated OpenCode config directory |

Claude Code-specific environment variables (`CLAUDE_CODE_*`, `ENABLE_CLAUDE_CODE_*`) are stripped from spawned processes.

### Whisper Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `WHISPER_BIN` | `whisper-cli` | Path to whisper binary |
| `WHISPER_MODEL` | `base.en` | Model name |
| `WHISPER_MODEL_PATH` | — | Full path to model file |

## Directory Structure

After initialization, the library creates:

```
{DBPath_dir}/
├── agents.db              # SQLite database
├── sessions/
│   └── {session-id}/
│       ├── conversation.jsonl   # Rich conversation log
│       ├── summary.json         # Session summary
│       └── attachments/
│           └── {id}_{filename}  # Uploaded files
└── handoffs/
    └── {session-id}.md    # Handoff state files
```
