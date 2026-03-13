# Queue System

The queue system enables follow-up messages to be queued while an agent is processing, then executed sequentially when the agent completes.

## How It Works

1. While an agent is active, new messages are added to the queue via `AddToQueue()`
2. When the agent completes, `ProcessNextFromQueue()` is called automatically
3. The next pending item is popped atomically (DB transaction) and sent to the agent
4. Events from queue processing are broadcast to SSE subscribers
5. On completion, the next item is processed, continuing until the queue is empty

## Queue Item Lifecycle

```
pending ──► processing ──► completed
                │
                └──► failed (with error message)
```

## Pause/Resume

Queue processing can be paused per session:
- Processing pauses automatically when `ask_user` is pending
- `ResumeQueue()` unpauses and triggers the next item
- `Stop()` pauses the queue for that session

## Ordering

Items are ordered by a `position` field. The `ReorderQueue()` method accepts an ordered list of item IDs and updates their positions accordingly.

## Audio Queue Items

Queue items can have `source: "audio"` with an attached audio file. When processed, the audio is transcribed via the configured `Transcriber` before sending to the agent. The transcript is stored on the queue item.

## Example

```go
// Add follow-up while agent is busy
mgr.AddToQueue("session-123", mux.QueueItem{
    Text:  "Now refactor the tests",
    Agent: "claude",
})

// Add another
mgr.AddToQueue("session-123", mux.QueueItem{
    Text:  "Then update the README",
    Agent: "claude",
})

// Check queue
items, _ := mgr.ListQueue("session-123")
// items[0].Text = "Now refactor the tests" (position 0)
// items[1].Text = "Then update the README" (position 1)

// Reorder if needed
mgr.ReorderQueue("session-123", []string{items[1].ID, items[0].ID})
```
