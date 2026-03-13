# Agents

## Supported Agents

### Claude

- **Binary**: `claude`
- **Default model**: `sonnet`
- **Flags**: `--output-format stream-json`, `--resume` (if resuming), `--model`, `--agent` (sub-agent), `--mcp-config`
- **Output format**: NDJSON with `type: "assistant"` (text/tool_use content blocks) and `type: "result"` (session ID, token usage)
- **Resume**: Supports conversation resume via `conversation_id`

### Codex

- **Binary**: `codex`
- **Default model**: `o4-mini`
- **Flags**: `--full-auto`, `--json`, `--model`
- **Output format**: NDJSON with `type: "item.completed"`, `type: "item.delta"`, `type: "turn.completed"`
- **Resume**: Not supported (always fresh context)

### OpenCode

- **Binary**: `opencode`
- **Flags**: `--json`, `--model`
- **Output format**: NDJSON with `type: "text"`, `type: "tool_use"`, `type: "step_finish"`, `type: "error"`
- **Resume**: Supports resume via session ID from events

### Cursor

- **Binary**: `agent`
- **Flags**: `--output-format stream-json`, `--resume`, `--model`
- **Output format**: NDJSON with `type: "assistant"`, `type: "tool_call"` (MCP/function subtypes), `type: "result"`, `type: "error"`
- **Resume**: Supports conversation resume via `conversation_id`

## Agent Resolution

The `resolveAgentBinary()` function maps agent names to CLI binaries and verifies they exist on PATH:

| Agent ID | Binary |
|----------|--------|
| `claude` | `claude` |
| `codex` | `codex` |
| `opencode` | `opencode` |
| `cursor` | `agent` |

## Registry

The `Registry` manages known agents with availability checking:

```go
registry := mux.NewRegistry()

// Check which agents are available
registry.Discover()

// List all agents
agents := registry.ListAgents()
for _, a := range agents {
    fmt.Printf("%s: available=%v\n", a.Name, a.Available)
}

// Register a custom agent
registry.Register(mux.AgentInfo{
    ID:        "my-agent",
    Name:      "My Custom Agent",
    Available: true,
    Models:    []string{"gpt-4", "gpt-3.5"},
})
```

## Process Management

Agent processes are spawned with process group isolation:

- `Setpgid: true` creates a new process group for each agent
- On stop, the entire process group is killed (`kill -pid`), ensuring MCP child servers are terminated
- PIDs are tracked in both memory (`activeProcesses` map) and database for orphan cleanup
- `StopAll()` queries the database for PIDs not in memory and kills those too

## Environment Isolation

Each agent subprocess gets an isolated environment:

- Claude Code environment variables (`CLAUDE_CODE_*`, `ENABLE_CLAUDE_CODE_*`) are stripped
- Codex gets an isolated `CODEX_HOME` directory
- OpenCode gets an isolated `OPENCODE_CONFIG_DIR` directory
- Custom environment can be provided via `Config.AgentEnv`

## Conversation Resume

When sending to a session that previously used the same agent:

1. Manager checks if the session has a `conversation_id` and the agent matches `last_agent`
2. If resumable, passes `--resume {conversation_id}` to the CLI
3. If not resumable (different agent or no ID), builds context from the handoff file or last 20 messages

## Handoff Triggers

Automatic handoff occurs when:
- **Token usage > 50%** of estimated context window (after agent completion)
- **Screenshot count >= 20** (via `TrackScreenshot()`)
- **Idle timeout** expires (default 10 minutes)

Handoff writes a markdown file with summary, current state, and pending tasks, then resets the session for fresh context.
