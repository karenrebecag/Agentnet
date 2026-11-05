# agentnet protocol (v0)

This document specifies the on-disk layout and the message format, so any implementation in any language can interoperate with the reference CLI.

## Data directory

`$AGENTNET_HOME`, default `~/.agentnet`. Directories are created with mode `0700` and files with mode `0600`.

```
$AGENTNET_HOME/
  agents/
    .lock               registry lock
    <name>.json         registration of a joined agent
  inbox/
    <name>.jsonl        messages addressed to <name>, one JSON object per line
    <name>.cursor       byte offset of the first unread message, as decimal text
```

## Names

A name matches `^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`. Names are case-sensitive, and implementations must reject anything else before touching the filesystem. This rule is what prevents path traversal.

## Envelope

One message is one line of UTF-8 JSON terminated by `\n`:

```json
{"id":"9b1c...","ts":"2026-01-01T12:00:00Z","from":"bob","to":"alice","text":"hello"}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Unique message id. The reference CLI uses 128 random bits in hex. Readers must treat it as opaque |
| `ts` | string | Send time, RFC 3339, UTC |
| `from` | string | Sender name, as declared by the sender (not authenticated) |
| `to` | string | Recipient name |
| `text` | string | Message body, at most 256 KiB, trimmed of surrounding whitespace |
| `origin` | string | Optional. `external` when the message entered through `expose`: treat it as third-party data |
| `reply_to` | string | Optional. The `id` of the message this one answers |
| `corrupt` | bool | Set by a reader on a line that is not valid JSON; the raw line is in `text` |

Readers must ignore unknown fields, so later versions can add fields.

## Sending

1. Validate `to` and `from`, and reject empty or oversized text.
2. Open `inbox/<to>.jsonl` with `O_WRONLY|O_APPEND|O_CREAT`, mode `0600`.
3. Take an exclusive `flock` on the file.
4. Write the whole line, including the trailing `\n`, then release the lock.

Taking the lock is mandatory. A single `write` is not guaranteed to land in one piece for large lines, and the lock is what keeps concurrent senders from interleaving bytes.

## Reading

A reader delivers in two steps, so a crash between them re-delivers instead of losing messages.

**Peek**, under an exclusive `flock` on the inbox:

1. Read the cursor. If it is missing, invalid or past the end of the file, use 0 (or the file size).
2. Read from the cursor to the end of the file.
3. Keep only bytes up to and including the last `\n`. A trailing fragment is a message still being written and stays unread.
4. Parse each non-empty line. A line that fails to parse is delivered with `corrupt: true`.

**Commit**, after the messages reached the agent, under the same lock:

1. The new offset is the old cursor plus the bytes consumed.
2. If the new offset is at least 1 MiB and equals the file size, truncate the inbox to 0 and set the offset to 0.
3. Write the cursor atomically (write a temporary file, then rename it).

## Registry and liveness

`join` claims a name by writing `agents/<name>.json`:

```json
{"name":"alice","kind":"claude","pid":4242,"cwd":"/path","joined":"2026-01-01T12:00:00Z","accept_external":true}
```

`accept_external` is set by `join --accept-external`. Without it, the agent is invisible to remote guests.

- The claim happens under an exclusive `flock` on `agents/.lock`. If the existing registration belongs to another live process, the claim fails (exit code 3).
- While joined, the agent refreshes the file's modification time every 300 ms. That mtime is the heartbeat.
- An agent is **live** when its heartbeat is at most 5 s old and its `pid` exists. The heartbeat check is what protects against a reused pid.
- On a clean exit, `join` removes its registration, but only if it still owns it.

## Event stream

`join` and `read` write one line per event to stdout:

```
AGENTNET <json>
```

The first event of `join` is `{"joined":"<name>","kind":"<kind>"}`. Every later event is an envelope. Consumers should split on the first space and parse the rest as JSON.

## Compatibility

v0 is compatible with the original Python prototype: both read and write the same files. The prototype used 48-bit ids, and implementations must accept ids of any length.
