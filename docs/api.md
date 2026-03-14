# API Reference

Constellation currently publishes this API under the compatibility module path `github.com/prxg22/agents-mux`.

## Manager

### Constructor

```go
func NewManager(cfg Config) (*Manager, error)
```

Creates a new Manager. Opens the SQLite database, creates required directories, and initializes the broadcaster. Returns an error if `DBPath` is empty or the database cannot be opened.

### Session Operations

```go
func (m *Manager) CreateSession(sessionID string) error
```
Creates an empty session. If `sessionID` is empty, generates a UUID.

```go
func (m *Manager) ListSessions() ([]Session, error)
```
Returns all sessions ordered by `last_active_at` descending.

```go
func (m *Manager) DeleteSession(sessionID string) error
```
Deletes a session, its messages, queue items, and session directory.

```go
func (m *Manager) GetMessages(sessionID string) ([]Message, error)
```
Returns conversation messages. Prefers JSONL file, falls back to database.

```go
func (m *Manager) GetConversation(sessionID string) ([]ConversationEntry, error)
```
Returns full conversation entries from the JSONL file (includes tool calls, attachments).

```go
func (m *Manager) GetSummary(sessionID string) SessionSummary
```
Returns the session's summary from `summary.json`.

```go
func (m *Manager) Close() error
```
Closes the database connection.

### Agent Control

```go
func (m *Manager) Send(req SendRequest) (*SendResult, error)
```

Sends a prompt to an agent. Creates the session if it doesn't exist, persists the message, spawns the agent subprocess, and returns a channel of events.

**SendRequest fields:**
| Field | Type | Description |
|-------|------|-------------|
| Prompt | string | The user message (required) |
| SessionID | string | Session to use (auto-generated if empty) |
| Agent | string | Agent name: claude, codex, opencode, cursor (default: claude) |
| AgentSub | string | Sub-agent identifier |
| Model | string | Model override |
| Effort | string | Effort level |
| AttachmentIDs | []string | Attachment IDs to include |

**SendResult fields:**
| Field | Type | Description |
|-------|------|-------------|
| Events | <-chan ChanEvent | Event stream channel |
| SessionID | string | The session ID used |
| MessageID | int64 | Database message ID |

**ChanEvent types:**
| Type | Description |
|------|-------------|
| ChanText | Text output from the agent |
| ChanAction | Tool/action status update |
| ChanAskUser | Agent is asking the user a question |

```go
func (m *Manager) Stop(sessionID string) error
```
Stops the active agent process for a session. Kills the process group and marks the session as idle.

```go
func (m *Manager) StopAll() error
```
Stops all active agent processes and cleans up orphaned PIDs from the database.

```go
func (m *Manager) GetSessionStatus(sessionID string) string
```
Returns the current session status: "active", "idle", or "waiting".

```go
func (m *Manager) SkipAsk(sessionID string) error
```
Skips a pending ask_user question and resumes processing.

### Queue Operations

```go
func (m *Manager) AddToQueue(sessionID string, item QueueItem) error
```
Adds a follow-up item to the session's queue at the next position.

```go
func (m *Manager) ListQueue(sessionID string) ([]QueueItem, error)
```
Returns all queue items for a session ordered by position.

```go
func (m *Manager) UpdateQueueItem(itemID string, text string) error
```
Updates the text of a pending queue item.

```go
func (m *Manager) DeleteQueueItem(itemID string) error
```
Deletes a queue item.

```go
func (m *Manager) ReorderQueue(sessionID string, itemIDs []string) error
```
Reorders queue items by setting positions based on the provided ID order.

```go
func (m *Manager) ClearQueue(sessionID string) error
```
Removes all pending queue items for a session.

```go
func (m *Manager) ResumeQueue(sessionID string) error
```
Unpauses queue processing and processes the next item.

```go
func (m *Manager) ProcessNextFromQueue(sessionID string) error
```
Manually triggers processing of the next pending queue item.

```go
func (m *Manager) QueueLength(sessionID string) int
```
Returns the number of pending items in the queue.

### Lifecycle

```go
func (m *Manager) HandleHandoff(req HandoffRequest) error
```
Triggers a manual handoff, persisting session state for later resumption.

```go
func (m *Manager) TrackScreenshot(sessionID string) error
```
Increments the screenshot counter. Triggers handoff at 20 screenshots.

### Attachments

```go
func (m *Manager) SaveAttachmentBytes(sessionID, filename, mimeType string, data []byte) (AttachmentRef, error)
```
Saves an attachment file and returns a reference with the generated ID.

```go
func (m *Manager) ResolveAttachments(sessionID string, ids []string) ([]AttachmentRef, error)
```
Resolves attachment IDs to full references with file paths.

### Broadcasting

```go
func (m *Manager) GetBroadcaster() *SessionBroadcaster
```
Returns the broadcaster for subscribing to events.

## SessionBroadcaster

```go
func (b *SessionBroadcaster) SubscribeSession(sessionID string, lastSeq int) (<-chan SessionStreamEvent, []SessionStreamEvent, bool, func())
```
Subscribes to a session's event stream. Returns:
- Event channel
- Replay events (if reconnecting)
- Whether a full flush is needed (gap in ring buffer)
- Unsubscribe function

```go
func (b *SessionBroadcaster) SubscribeNotify() (<-chan NotifyEvent, func())
```
Subscribes to global session notifications (created, deleted, status changes).

## Registry

```go
func NewRegistry() *Registry
```
Creates a registry pre-populated with default agents (Claude, Codex, OpenCode, Cursor).

```go
func (r *Registry) ListAgents() []AgentInfo
func (r *Registry) GetAgent(id string) (AgentInfo, bool)
func (r *Registry) Register(info AgentInfo)
func (r *Registry) Discover()
```

`Discover()` checks which agent binaries are available on PATH.

## Whisper Transcriber

```go
import "github.com/prxg22/agents-mux/whisper"

t := whisper.NewTranscriber()
text, err := t.Transcribe("/path/to/audio.wav", "en")
```

Uses `whisper.cpp` CLI. Configure via environment variables (`WHISPER_BIN`, `WHISPER_MODEL`, `WHISPER_MODEL_PATH`).
