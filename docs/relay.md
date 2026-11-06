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

**Address.** Agents are addressed as `<node>.<agent>`, for example `laptop.claude-api`. A node name matches `^[a-z][a-z0-9-]{0,31}$` and has no dots, so an address always splits at its first dot; the agent part follows the local name rules and may contain dots. The node names `ext` and `local` are reserved: `ext.<guest>` is already used by `expose` for local guest inboxes.

The relay stamps `from` on every envelope as `<authenticated node>.<agent>`. A machine names the sending agent in each `send`, and the relay accepts it only if that agent is in the node's current announced list. A machine cannot send as another node.

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
| `hello` | `agents: [{name, kind, accept_external}]` | Announce the local agents that are live. Delivery resumes from the relay's own record of the last ack |
| `agents` | `agents: [...]` | Replace the announced agent list |
| `send` | `ref, from, to, text, reply_to?` | Send as local agent `from` (must be announced). `ref` is a client id echoed in `sent` |
| `ack` | `seq` | Cumulative: every message up to `seq` was written to a local inbox. A client never acks past a message it has not written |

The relay sends:

| type | fields | meaning |
|---|---|---|
| `welcome` | `node` | Authenticated as this node |
| `deliver` | `seq, envelope` | One message for an agent of this node. Repeated until acked |
| `sent` | `ref, id, status, error?` | `status` is `delivered` (recipient node online), `queued`, or `rejected`; a rejection carries `error` in the same frame and has no `id` |
| `error` | `code, message` | A protocol error. Fatal errors are followed by a close |

Delivery is at least once. On every connection the relay redelivers everything after the last ack, so a client cannot skip mail it never acknowledged. The client deduplicates by `envelope.id`, writes the message to the local inbox, then acks.

## Guests: `POST /mcp`

A stateless Streamable HTTP MCP endpoint. A guest authenticates with its static token (`Authorization: Bearer an_…`, for curl or clients that take an API key) or with an OAuth access token (see [Signing in from a web client](#signing-in-from-a-web-client)). Its tools have the same names and meaning as `expose`:

- `list_agents`: the agents this guest may ask, with `live` status.
- `ask {to, text, wait_seconds}`: send and wait up to 50 s for a reply.
- `get_reply {id, wait_seconds}`: collect a reply later. Ids are bound to the guest that asked.

A guest created with `inbox: true` also gets:

- `inbox`: read and clear messages its agents sent without being asked. Delivery is at most once: a message is gone once handed out, even if the response is lost on the way.
- `wait {wait_seconds}`: wait up to 50 s for the next one. MCP gives a web client no way to be woken up between turns (no supported client implements sampling or resource subscriptions), so a client listens by calling `wait` in a loop during a turn.

Only an agent in the guest's `targets` can write to its inbox, at most 10 messages a minute and 200 a day per agent. Everything else a machine sends to `<guest>.mcp` must be the one reply to an `ask`.

A guest can reach an agent only if both are true:

1. The agent's address is in the guest's `targets`, set when the guest is created.
2. The agent's machine announced it with `accept_external: true`.

## Admin: `/v1/admin/*`

Authenticated with `ADMIN_TOKEN`.

| method | path | body | result |
|---|---|---|---|
| `POST` | `/v1/admin/nodes` | `{name, kind, targets?, inbox?}` | `{name, token}` |
| `GET` | `/v1/admin/nodes` | | `[{name, kind, targets, revoked, created, last_seen, agents}]`, never token hashes |
| `POST` | `/v1/admin/nodes/{name}/rotate` | | `{token}` |
| `POST` | `/v1/admin/nodes/{name}/revoke` | | `{}`. Also ends every OAuth grant of a guest |
| `POST` | `/v1/admin/nodes/{name}/pair` | `{for: "claude.ai"}` | `{code, expires, connector_url}` |

## Signing in from a web client

claude.ai, Perplexity and other assistants connect to remote MCP servers with OAuth, so the relay is also an OAuth 2.1 authorization server for its guests. It follows the [MCP authorization spec](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization):

- `/mcp` answers an unauthenticated call with `401` and `WWW-Authenticate: Bearer resource_metadata=…`.
- `/.well-known/oauth-protected-resource` (RFC 9728) names this host as the authorization server; `/.well-known/oauth-authorization-server` (RFC 8414) advertises PKCE `S256`, public clients only, and Client ID Metadata Documents.
- Clients identify themselves with a Client ID Metadata Document (preferred; Dynamic Client Registration is deprecated as of MCP 2026-07-28) or register at `/oauth/register`.

There is no account to log in to. The person proves they own a guest with a **pairing code**:

```bash
agentnet relay node add claude-web --kind guest --targets laptop.claude [--inbox]
agentnet relay node pair claude-web --for claude.ai     # prints the code once and the connector URL
```

Add the connector URL in the assistant. Its sign-in opens the relay's consent page, which shows the client and the full address it returns to; the person types the code there. The code:

- works once, for 10 minutes;
- is bound to the host given with `--for`: a client that returns anywhere else is refused, so a code read over someone's shoulder cannot be used from another site;
- is stored only as a hash. Five wrong codes from one address lock that address out of the consent page for 15 minutes; nobody else's pairing is affected. A pairing cannot be bound to a host where anyone can get a subdomain (`workers.dev`, `vercel.app`, `github.io`, ...).

Tokens: access tokens last 1 hour; refresh tokens last 30 days and rotate on every use, and presenting a used one ends the whole grant. A sign-in lasts at most 90 days however often it refreshes. Rotating or revoking the guest ends its sign-ins. Access tokens are bound to this relay's `/mcp` (RFC 8707) and resolve to the guest, so routing and limits are the same as with the static token. Revoking the guest ends all of it at once.

The consent page runs no script, cannot be framed, and its form posts only to this host. It is the only route that accepts a browser `Origin`, and only its own.

## Limits

- Per node: 60 sends per minute and 2,000 per day.
- Per guest: 10 asks per minute and 100 per day, with 4 calls in flight. Counters are stored, so they survive the object being evicted.
- Per ask: one reply.
- Queue: at most 1,000 unacked messages per node, at most 250 of them from one sender. Messages expire after 7 days.
- Authentication failures from one IP: 10 per 60 seconds (Workers Rate Limiting binding, per location), then 429. Valid tokens are never counted.
- OAuth endpoints: 30 requests per minute per IP. Dynamic registration: 10 per IP per hour; at most 200 clients waiting for their first sign-in (the oldest is dropped past that), and an unused registration expires after 1 hour. At most 1,000 open consent pages.

## Local side

`agentnet link` reads `~/.agentnet/relay.json` (mode 0600). The admin token lives in a separate file, `~/.agentnet/relay-admin.json` (also 0600), which `link` never reads, so a compromised link process cannot manage nodes:

```json
{"url":"https://agentnet-relay.<subdomain>.workers.dev","node":"laptop","token":"an_…"}
```

The link process has three jobs:

1. **Announce local agents.** It sends the live local agents in `hello` and `agents`. An agent is offered to guests only if it joined with `--accept-external`.
2. **Bring remote mail in.** Each `deliver` is written to the local inbox of the target agent, with the global `from` and `origin` kept.
3. **Send local mail out.** A local agent writes to `desktop.codex` as usual. Link watches the inbox directory for any name shaped like `<node>.<agent>` whose node is not its own and not reserved, takes it as reader, and forwards what lands there. There is no directory lookup first: an unknown node comes back as `rejected`, and link writes that rejection to the sender's inbox.

Without the link process, the local network works exactly as before.

## Deploying your own relay

Nothing in the repository is tied to an account. You deploy to your own Cloudflare account:

```bash
cd relay
npm ci
npx wrangler login                                       # your account, in your browser
npx wrangler deploy                                      # prints https://agentnet-relay.<subdomain>.workers.dev
agentnet relay admin-token | npx wrangler secret put ADMIN_TOKEN
agentnet relay init --url https://agentnet-relay.<subdomain>.workers.dev
agentnet relay node add laptop --kind machine            # saves the machine token to relay.json
agentnet link
```

`admin-token` writes the token to `~/.agentnet/relay-admin.json` and prints it once, so piping it into `wrangler` keeps it off the screen. A guest is a node too: `agentnet relay node add <name> --kind guest --targets laptop.claude` prints its token, which goes in the remote assistant's connector as a Bearer header for `https://agentnet-relay.<subdomain>.workers.dev/mcp`.

The Worker URL, the account and every token stay in local files that git ignores.
