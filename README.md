# Constellation

<p align="center">
  <img src="assets/brand/svg/constellation_logo_dark.svg" alt="Constellation" width="320" />
</p>

Constellation is the Apsis orchestration library for multiplexing AI agent CLIs. It currently ships under the compatibility module path and CLI binary name `agents-mux` while the user-facing product name transitions to Constellation.

## Features

- **Multi-agent support** — Claude, Codex, OpenCode, and Cursor via subprocess spawning
- **Session management** — Create, list, delete sessions with SQLite persistence
- **Real-time streaming** — SSE broadcasting with ring buffer for reconnection support
- **Queue system** — Position-based follow-up queue with pause/resume and atomic processing
- **Conversation persistence** — Dual storage via SQLite messages table and JSONL files
- **Lifecycle management** — Idle timeouts, handoff on token exhaustion, process group cleanup
- **Attachment handling** — File upload, validation, and resolution for agent prompts
- **Agent registry** — Static + dynamic agent registration with binary discovery
- **Speech-to-text** — Whisper integration for audio transcription
- **Environment isolation** — Isolated config directories prevent agent CLI leakage

## Installation

The public module path remains `github.com/prxg22/agents-mux` for compatibility in this pass.

```bash
go get github.com/prxg22/agents-mux
```

Requires Go 1.24+.

## Quick Start

```go
package main

import (
    "fmt"
    "log"

    mux "github.com/prxg22/agents-mux"
)

func main() {
    mgr, err := mux.NewManager(mux.Config{
        DBPath: "./data/agents.db",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer mgr.Close()

    // Send a prompt to Claude
    result, err := mgr.Send(mux.SendRequest{
        Prompt: "Hello, what can you help me with?",
        Agent:  "claude",
    })
    if err != nil {
        log.Fatal(err)
    }

    // Stream events
    for event := range result.Events {
        switch event.Type {
        case mux.ChanText:
            fmt.Print(event.Text)
        case mux.ChanAction:
            fmt.Printf("\n[Action] %s\n", event.Text)
        case mux.ChanAskUser:
            fmt.Printf("\n[Question] %s\n", event.Text)
        }
    }
}
```

## Documentation

- [Architecture](docs/architecture.md) — System design, data flow, and component overview
- [Configuration](docs/configuration.md) — Manager config, interfaces, and environment setup
- [API Reference](docs/api.md) — Public API methods and types
- [Queue System](docs/queue.md) — Follow-up queue management
- [Broadcasting](docs/broadcasting.md) — SSE event streaming and reconnection
- [Agents](docs/agents.md) — Supported agents, parsers, and registry

## Supported Agents

| Agent | CLI Binary | Default Model | Output Format |
|-------|-----------|---------------|---------------|
| Claude | `claude` | sonnet | NDJSON (assistant/result) |
| Codex | `codex` | o4-mini | NDJSON (item.completed/turn.completed) |
| OpenCode | `opencode` | — | NDJSON (text/tool_use/step_finish) |
| Cursor | `agent` | — | NDJSON (assistant/tool_call/result) |

## Related Projects

- **[Perigee](https://github.com/apsis-ai/perigee)** — Apsis remote desktop workspace. This library was extracted from its session management. Both projects are co-developed and may be modified together.

## Requirements

- Go 1.24+
- At least one agent CLI installed on PATH (`claude`, `codex`, `opencode`, or `agent`)
- Optional: `whisper.cpp` binary for speech-to-text

## License

MIT
