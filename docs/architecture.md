# Architecture

## Overview

agents-mux is built around a central `Manager` that coordinates session lifecycle, agent subprocess management, event broadcasting, and queue processing. All state is persisted in SQLite with per-session JSONL conversation logs.

## Core Components

```
┌──────────────────────────────────────────────────────┐
│                     Manager                           │
│  ┌──────────┐  ┌──────────┐  ┌────────────────────┐  │
│  │ SQLite DB│  │ Processes│  │ SessionBroadcaster │  │
│  └──────────┘  └──────────┘  └────────────────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌────────────────────┐  │
│  │  Queue   │  │ Lifecycle│  │   Attachments      │  │
│  └──────────┘  └──────────┘  └────────────────────┘  │
└──────────────────────────────────────────────────────┘
         │                              │
    ┌────┴────┐                   ┌─────┴──────┐
    │  Agent  │                   │ SSE Client │
    │ Process │                   │ Subscriber │
    └─────────┘                   └────────────┘
```

### Manager (`session.go`)

Central coordinator. Owns the SQLite database, broadcaster, process tracking maps, and idle timers. All public API methods are on `Manager`.

### Agent Spawner (`agent.go`)

Spawns agent CLI processes with:
- Process group isolation (`Setpgid: true`) for clean MCP child cleanup
- Environment isolation to prevent config leakage between agents
- Agent-specific command construction (`buildClaudeCommand`, `buildCodexCommand`, etc.)
- NDJSON stdout streaming via agent-specific parsers

### Stream Parsers (`parser.go`)

Each supported agent has a dedicated NDJSON parser that normalizes output into `ChanEvent` values:
- **Claude**: Parses `assistant` events (text/tool_use blocks) and `result` events
- **Codex**: Parses `item.completed`, `item.delta`, and `turn.completed` events
- **OpenCode**: Parses `text`, `tool_use`, `step_finish`, and `error` events
- **Cursor**: Parses `assistant`, `tool_call`, `result`, and `error` events

### SessionBroadcaster (`broadcast.go`)

Fans out events to SSE subscribers using a per-session ring buffer for reconnection support. Supports two subscription types:
- **Session subscriptions**: Per-session event stream with sequence-based delta replay
- **Notify subscriptions**: Global stream for session creation/deletion/status changes

### Queue System (`queue.go`)

Position-based follow-up queue with:
- Atomic pop operations via database transactions
- Per-session pause/resume control
- Automatic processing after agent completion
- Support for reordering, clearing, and status tracking

### Lifecycle Manager (`lifecycle.go`)

Handles:
- **Idle timeouts**: Configurable timer triggers handoff when session is idle
- **Handoff**: Persists session state to markdown files on token exhaustion (>50% usage) or screenshot limit (20+)
- **Stop/StopAll**: Kills process groups, cleans up orphaned PIDs from database

### Conversation Persistence (`conversation.go`)

Dual storage:
- **SQLite `messages` table**: Simple role/content pairs for quick queries
- **JSONL files**: Rich `ConversationEntry` records with tool calls, attachments, and metadata

### Attachment Manager (`attachment.go`)

File upload with:
- MIME type validation
- Size limits
- Per-session atomic ID counters
- Resolution from ID to full `AttachmentRef` with path

## Data Flow

### Prompt Execution

```
User Prompt
    │
    ▼
Manager.Send()
    │
    ├─ Persist message to DB + JSONL
    ├─ Resolve attachments
    ├─ Build context (handoff file or last 20 messages)
    ├─ Construct agent command with flags
    ├─ Spawn subprocess with process group
    │
    ▼
Agent Process (stdout NDJSON)
    │
    ▼
Stream Parser
    │
    ├─ ChanText  ──► Broadcaster ──► SSE Subscribers
    ├─ ChanAction ──► Broadcaster ──► SSE Subscribers
    └─ ChanAskUser ──► Broadcaster ──► SSE Subscribers
    │
    ▼
Process Completion
    │
    ├─ Persist assistant message
    ├─ Update conversation_id + token_usage
    ├─ Check handoff triggers (token usage, screenshots)
    └─ Process next queue item
```

### SSE Reconnection

```
Client connects with lastSeq
    │
    ▼
RingBuffer.EventsAfter(lastSeq)
    │
    ├─ Events found ──► Send delta replay
    └─ Gap detected  ──► Signal full flush needed
```

## Database Schema

### sessions
| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | UUID |
| status | TEXT | active/idle/waiting |
| conversation_id | TEXT | Agent's conversation ID for resume |
| token_usage | INTEGER | Last known token count |
| title | TEXT | Auto-generated session title |
| last_agent | TEXT | Last agent used (claude/codex/etc.) |
| pid | INTEGER | Active process PID (0 if idle) |
| screenshot_count | INTEGER | Handoff trigger counter |

### messages
| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER PK | Auto-increment |
| session_id | TEXT | Foreign key to sessions |
| role | TEXT | user/assistant |
| content | TEXT | Message text |

### follow_up_queue
| Column | Type | Description |
|--------|------|-------------|
| id | TEXT PK | UUID |
| session_id | TEXT | Foreign key to sessions |
| text | TEXT | Prompt text |
| position | INTEGER | Queue ordering |
| status | TEXT | pending/processing/completed/failed |
| agent | TEXT | Target agent |
| attachments | TEXT | JSON array of attachment IDs |

## Design Decisions

- **Pure-Go SQLite** (`modernc.org/sqlite`): Zero CGO dependencies for simple cross-compilation
- **Process groups**: `Setpgid: true` + kill `-pid` ensures MCP child servers are terminated with the agent
- **Ring buffer**: Fixed-size circular buffer avoids unbounded memory for SSE replay
- **Per-session file mutexes**: `sync.Map` of `*sync.Mutex` for concurrent JSONL writes
- **Idempotent migrations**: `ALTER TABLE ADD COLUMN` with silent failure for schema evolution
- **Interface injection**: 7 optional interfaces for customizing title generation, handoff handling, MCP config, etc.
