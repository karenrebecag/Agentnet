# agentnet relay

Status: draft for v0.3. This document is the contract between the relay (`relay/`, a Cloudflare Worker) and its clients (`agentnet link`, MCP clients).

## Why a relay

`expose` makes one machine reachable for the length of a session through a tunnel. The relay goes further: every machine connects **outbound** to one mailbox service, so agents on different computers become peers, messages wait while a machine is off, and no machine ever accepts inbound connections.

```
 laptop                      Cloudflare                       desktop
 ┌──────────────┐   WSS    ┌───────────────────────────┐   WSS   ┌──────────────┐
 │ agents ──────┼─ link ──►│ Worker                    │◄─ link ─┼────── agents │
 │ (~/.agentnet)│          │  ├ Directory DO (1)       │         │ (~/.agentnet)│
 └──────────────┘          │  └ Mailbox DO (1 per node)│         └──────────────┘
                           │      SQLite queue + WS    │
 web assistant ── HTTPS ──►│  /mcp (guests)            │
                           └───────────────────────────┘
```

## Concepts

**Node.** One participant that holds a token. There are two kinds:

- `machine`: a computer running `agentnet link`. It is a full member: it hosts agents, sends and receives.
- `guest`: a remote assistant, such as a web app connector, that only speaks MCP. It can only ask the agents it is allowed to ask.

**Address.** Agents are addressed as `<node>.<agent>`, for example `laptop.claude-api`. A node name matches `^[a-z][a-z0-9-]{0,31}$`; the agent part follows the local name rules. The relay stamps `from` on every envelope: a client can never choose it.

**Origin.** Every envelope delivered by the relay carries `origin`:

- `relay`: from an agent on another of your machines.
- `external`: from a guest. Treat it as third-party data.

## Tokens

- Format: `an_` followed by 43 base64url characters (256 random bits). They are shown once at creation or rotation.
- The relay stores only `SHA-256(token)` and compares in constant time.
- Each node has exactly one active token. `rotate` replaces it, and `revoke` disables the node and closes its connections at once.
- The admin token is a Worker secret (`ADMIN_TOKEN`) and is used only by the local CLI to manage nodes. It never reaches agents or guests.

Every request carries `Authorization: Bearer <token>`. Tokens never go in URLs.

## Envelope

The local v0 envelope, with global addresses:

```json
{"id":"…","ts":"2026-01-01T12:00:00.000Z","from":"laptop.claude-api","to":"desktop.codex",
 "text":"…","origin":"relay","reply_to":"…"}
```

- `id`: 128 random bits in hex, assigned by the relay.
- `text`: at most 64 KiB from machines and 16 KiB from guests.

## Machine link: `GET /v1/link` (WebSocket)

Authenticated with a `machine` token. The relay replaces an older socket of the same node, so there is one live socket per node.

Frames are JSON text. The client sends:

| type | fields | meaning |
|---|---|---|
| `hello` | `agents: [{name, kind, accept_external}]`, `after: seq` | Announce the local agents that are live, and resume delivery after `seq` |
| `agents` | `agents: [...]` | Replace the announced agent list |
| `send` | `ref, to, text, reply_to?` | Send. `ref` is a client id echoed in `sent` |
| `ack` | `seq` | Everything up to `seq` was written to a local inbox |

The relay sends:

| type | fields | meaning |
|---|---|---|
| `welcome` | `node` | Authenticated as this node |
| `deliver` | `seq, envelope` | One message for an agent of this node. Repeated until acked |
| `sent` | `ref, id, status` | `status` is `delivered` (recipient node online), `queued`, or `rejected` with `error` |
| `error` | `code, message` | A protocol error. Fatal errors are followed by a close |

Delivery is at least once. The client deduplicates by `envelope.id`, writes the message to the local inbox, then acks.

## Guests: `POST /mcp`

A stateless Streamable HTTP MCP endpoint, authenticated with a `guest` token. Its tools have the same names and meaning as `expose`:

- `list_agents`: the agents this guest may ask, with `live` status.
- `ask {to, text, wait_seconds}`: send and wait up to 50 s for a reply.
- `get_reply {id, wait_seconds}`: collect a reply later. Ids are bound to the guest that asked.

A guest can reach an agent only if both are true:

1. The agent's address is in the guest's `targets`, set when the guest is created.
2. The agent's machine announced it with `accept_external: true`.

## Admin: `/v1/admin/*`

Authenticated with `ADMIN_TOKEN`.

| method | path | body | result |
|---|---|---|---|
| `POST` | `/v1/admin/nodes` | `{name, kind, targets?}` | `{name, token}` |
| `GET` | `/v1/admin/nodes` | | nodes without hashes |
| `POST` | `/v1/admin/nodes/{name}/rotate` | | `{token}` |
| `POST` | `/v1/admin/nodes/{name}/revoke` | | `{}` |

## Limits

- Per node: 60 sends per minute and 2,000 per day.
- Per guest: 10 asks per minute and 100 per day, with 4 calls in flight.
- Queue: at most 1,000 unacked messages per node. Messages expire after 7 days.
- Authentication failures from one IP: 5 per 15 minutes, then 429.

## Local side

`agentnet link` reads `~/.agentnet/relay.json` (mode 0600):

```json
{"url":"https://agentnet.<you>.workers.dev","node":"laptop","token":"an_…"}
```

The link process has three jobs:

1. **Announce local agents.** It sends the live local agents in `hello` and `agents`. An agent is offered to guests only if it joined with `--accept-external`.
2. **Bring remote mail in.** Each `deliver` is written to the local inbox of the target agent, with the global `from` and `origin` kept.
3. **Send local mail out.** A local agent writes to `desktop.codex` as usual. For every remote address it learns about, link registers a local inbox under that name and forwards what lands there.

Without the link process, the local network works exactly as before.

## Deploying your own relay

Nothing in the repository is tied to an account. You deploy to your own Cloudflare account:

```bash
cd relay
npm ci
npx wrangler login                            # your account, in your browser
npx wrangler secret put ADMIN_TOKEN           # paste the output of: agentnet relay admin-token
npx wrangler deploy
agentnet relay init --url https://agentnet.<you>.workers.dev
agentnet relay node add laptop --kind machine
```

The Worker URL, the account and every token stay in local files that git ignores.
