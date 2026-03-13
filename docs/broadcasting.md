# Broadcasting

The broadcasting system provides real-time event streaming via SSE (Server-Sent Events) with reconnection support.

## Session Events

Each session has its own event stream. Events are published as the agent produces output:

| Event Type | Description | Blocking |
|------------|-------------|----------|
| `chunk` | Text output from agent | No (dropped if subscriber is slow) |
| `action` | Tool/action status update | No |
| `ask_user` | Agent is asking a question | Yes |
| `done` | Agent completed | Yes |
| `error` | Agent errored | Yes |
| `status` | Session status changed | Yes |
| `queue_done` | Queue item completed | Yes |

### Subscribing

```go
broadcaster := mgr.GetBroadcaster()

// Subscribe to a session
events, replay, needsFlush, unsubscribe := broadcaster.SubscribeSession("session-123", 0)
defer unsubscribe()

// Process replay events first (if reconnecting)
for _, event := range replay {
    handleEvent(event)
}

// Then stream live events
for event := range events {
    handleEvent(event)
}
```

## Ring Buffer & Reconnection

Each session maintains a ring buffer (default 1024 events) with monotonic sequence numbers. When a client reconnects with a `lastSeq`:

1. **Events in buffer**: Returns all events after `lastSeq` as replay, then streams live
2. **Gap detected** (buffer has wrapped): Returns `needsFlush = true`, signaling the client should do a full state refresh
3. **No previous seq** (`lastSeq = 0`): Starts streaming from now, no replay

This allows efficient reconnection without maintaining per-client state on the server.

## Notify Stream

A global notification stream broadcasts session-level events:

```go
notifications, unsubscribe := broadcaster.SubscribeNotify()
defer unsubscribe()

for event := range notifications {
    // event.Type: "session_created", "session_deleted", "status"
    // event.SessionID: affected session
}
```

## Non-blocking vs Blocking

- **Chunk/action events** are sent non-blocking — if a subscriber's channel is full, the event is dropped. This prevents slow clients from blocking agent output.
- **Terminal events** (done, error, status) are sent with blocking semantics to ensure delivery.

## Concurrency

The broadcaster is safe for concurrent use. Multiple subscribers can watch the same session simultaneously. Publishing and subscribing are protected by a read-write mutex.
