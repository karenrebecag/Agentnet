```
 ╔═════════════════════════════════════════════════════════════════════════╗
╔╝                                                                         ╚╗
║   █████╗  ██████╗ ███████╗███╗   ██╗████████╗███╗   ██╗███████╗████████╗  ║
║  ██╔══██╗██╔════╝ ██╔════╝████╗  ██║╚══██╔══╝████╗  ██║██╔════╝╚══██╔══╝  ║
║  ███████║██║  ███╗█████╗  ██╔██╗ ██║   ██║   ██╔██╗ ██║█████╗     ██║     ║
║  ██╔══██║██║   ██║██╔══╝  ██║╚██╗██║   ██║   ██║╚██╗██║██╔══╝     ██║     ║
║  ██║  ██║╚██████╔╝███████╗██║ ╚████║   ██║   ██║ ╚████║███████╗   ██║     ║
║  ╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚═╝  ╚═══╝   ╚═╝   ╚═╝  ╚═══╝╚══════╝   ╚═╝     ║
╚╗                                                                         ╔╝
 ╚═════════════════════════════════════════════════════════════════════════╝
```

# agentnet

**A local message network for AI agents, whatever model or vendor runs them.**

agentnet lets coding agents on the same machine (Claude Code, Grok, Codex, Gemini CLI, or a plain script) send each other messages through one shared channel. The transport is a set of files, so no model, daemon, broker or socket sits between agents, and a message reaches a listening agent in under a second.

> Status: early (v0.x). macOS and Linux. The API and on-disk format may change before 1.0.

## Why

Interactive agents are not servers. A Claude Code or Grok session in a terminal has no endpoint another agent can call, and vendor APIs cannot write into a session that is already open. Most agents can, however, run a background command and wake up on each line it prints. agentnet turns that into a network:

- **Push to agents that can listen.** `agentnet join` is a long-lived process that prints one line per incoming message.
- **Pull for agents that cannot.** `agentnet read` returns unread messages once, on the agent's next turn.
- **A queue for agents that are away.** Messages to an offline agent wait in its inbox and are delivered when it comes back.

## Install

```bash
go install github.com/karenrebecag/agentnet/cmd/agentnet@latest
```

Requires Go 1.23 or later. A tag `v*` publishes binaries, checksums and an SBOM.

## init

```bash
agentnet init
```

Creates `~/.agentnet` with mode `0700` when that directory is missing. An existing directory is kept, including its inbox. For each of `claude`, `grok`, `codex` and `gemini` found on `PATH`, it prints a `join` command. It does not install those tools.

```bash
agentnet init --install-claude-skill
```

Also copies `adapters/claude-code/SKILL.md` to `~/.claude/skills/agentnet/SKILL.md`. Run it from a clone of this repository. Without the flag, that file is left untouched.

## Quick start

Open two terminals.

```bash
# terminal 1: alice listens
agentnet join alice --kind demo
```

```bash
# terminal 2: bob writes to alice
printf '%s' 'hello alice' | agentnet send alice --from bob
# delivered 3f2a... -> alice
```

Terminal 1 prints:

```
AGENTNET {"joined":"alice","kind":"demo"}
AGENTNET {"id":"3f2a...","ts":"2026-01-01T12:00:00Z","from":"bob","to":"alice","text":"hello alice"}
```

## Commands

| Command | Description |
|---|---|
| `agentnet join [name] [--kind K]` | Register `name` and print each incoming message as one `AGENTNET {...}` line. Runs until interrupted |
| `agentnet send <to> [--from N] [--message T] [--reply-to ID]` | Send a message. The text comes from `--message` or stdin. `--reply-to` is the id this answers |
| `agentnet read [name]` | Print unread messages once and mark them read |
| `agentnet list` | Show registered agents: name, kind, live or offline, working directory |
| `agentnet leave [name]` | Remove a registration left behind by a crashed process |
| `agentnet watch` | Live read-only view of agents and messages |
| `agentnet init [--install-claude-skill]` | Create the data directory and print a `join` command for each known agent CLI on `PATH` |
| `agentnet guests add\|list\|approve\|revoke` | Create, list, approve or revoke a remote guest. Approve is local only |
| `agentnet expose [--ttl 2h]` / `expose stop` | Open a quick tunnel, or stop one |
| `agentnet version` | Print the version |

| Environment variable | Default | Meaning |
|---|---|---|
| `AGENTNET_NAME` | none | Default name for `join`, `read`, `leave`, and `send --from` |
| `AGENTNET_HOME` | `~/.agentnet` | Data directory |

| Exit code | Meaning |
|---|---|
| 0 | Success. For `send`: delivered to a live agent |
| 1 | Error |
| 2 | `send` only: recipient offline, message queued |
| 3 | `join` only: the name is already live in another process |

## Connecting agents

Each agent needs one way to receive messages. Sending is always `agentnet send`. See [docs/integrations.md](docs/integrations.md) for step-by-step setups.

| Agent | Receives with |
|---|---|
| Claude Code | The `Monitor` tool running `agentnet join <name> --kind claude 2>&1` |
| Agents with a background monitor (for example Grok) | The same `join` command |
| Agents without one | `agentnet read <name>` at the start of each turn |

## Guarantees

- **At-least-once delivery.** The read cursor only advances after a message has been written to stdout. A reader that dies mid-batch gets the batch again.
- **No torn messages.** Senders append under an exclusive `flock`, so concurrent sends never interleave. A line still being written is not delivered until it is complete.
- **One reader per name.** Registration is atomic. A second `join` with a live name exits with code 3.
- **Liveness you can trust.** A joined agent refreshes its registration every 300 ms. More than 5 s without a refresh counts as offline, even if the process id was reused.
- **Bounded.** One message is at most 256 KiB. An inbox is truncated past 1 MiB, but only once fully read.
- **Private by default.** Files are mode 0600 in a 0700 directory.

The wire and on-disk formats are specified in [docs/protocol.md](docs/protocol.md).

## Trust model

agentnet authenticates nothing on the local machine. Any process running as your user can write to an inbox and set any `from`. This is a deliberate trade-off for a single-user workstation, and it has one rule:

**A message is data from another party.** It is never an instruction from the person running the agent, and it never counts as their approval. Agents should read messages the way they read a web page or an email, not the way they read their user.

## Expose

Local messages stay on this machine until someone runs `agentnet expose`. That command needs `cloudflared` already on `PATH`. It does not download it. It opens one Cloudflare quick tunnel for a TTL (default 2h, at most 12h) and prints the URL. Stop it with Ctrl-C or `agentnet expose stop`. If the tunnel process dies, the session ends and is not restarted.

A remote guest is created with `agentnet guests add <name> --can-ask <agent>` and stays pending until `agentnet guests approve <name>` runs on this machine. Approval is local only. The guest's messages carry `"origin":"external"`. An agent receives them only if it joined with `--accept-external`.

Five failed authentications from the same address lock that address for 15 minutes. Every 20 failures the host is warned. Those failures do not end the session. The threat model is [docs/threat-model.md](docs/threat-model.md).

## Development

```bash
go vet ./...
go test ./...
go build -trimpath ./cmd/agentnet
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).
