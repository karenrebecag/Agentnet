# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- `watch`: choosing an agent of this machine in `/add`, or pressing `e` on its row, opens that agent's live session to web guests. No rejoin with `--accept-external`; the opening ends with the session, so a later session under the same name starts closed.

## [0.5.1] - 2026-11-06

### Fixed

- `watch` no longer captures the mouse, so text on screen can be selected and copied as in any terminal. On the code screen, `c` copies the code and `u` the connector URL.

### Changed

- `watch`: `/add` with the name of a live web guest gives that guest a new code instead of failing, and a name the relay will not reuse moves to the next free one (`perplexityweb-2`). Relay errors read as the relay's own message, and the status line uses the width it has.

### Fixed

- Relay: the consent form was refused with "forbidden origin". Its page sent `Referrer-Policy: no-referrer`, under which browsers post with `Origin: null`; it now sends `same-origin`.

### Changed

- Relay: the sign-in pages follow Arc's sign-in card and OTP input, with one slot per character of the code and paste across slots. The one script is inline and allowed by hash; the form works without it.
- `watch`: a client waiting for approval opens the approval prompt at once, and the code screen of that guest closes since its code is spent.

## [0.5.0] - 2026-11-06

### Added

- `watch`: the Agents tab also lists the agent CLIs open on this machine that have not joined (Claude Code, Grok, Codex, Gemini, ...). Enter on one copies a join line to paste into that session, the same for every CLI; `e` copies one that also accepts web guests.
- `watch`: `/add` connects a web AI in two answers, a name and the agents it can talk to, and shows the connector URL and the code. `/pair <guest>` makes a new code.
- Relay: a pairing code without `--for` is bound by its first use, and the client waits until the person approves the host it returns to, in `watch` or with `relay signins approve <id>`.

## [0.4.0] - 2026-11-06

### Added

- Relay: OAuth 2.1 sign-in for guests, so assistants that only speak OAuth (claude.ai, Perplexity) can connect. A pairing code bound to the client's host proves ownership of the guest; refresh tokens rotate with reuse detection; revoking the guest ends every grant.
- Relay: `inbox` and `wait` tools for guests created with `inbox`, so their agents can write first.
- `relay node pair <guest> --for <host>`: prints a one-use pairing code bound to the client's host, and `relay node add --inbox`.

### Changed

- `watch` is now a network manager: tabs for agents, relay nodes, guests and messages, a filter, and actions on the selected row (add, rotate, revoke, approve). The same actions run as `/` commands with zsh-style completion of node names and `node.agent` addresses. Destructive actions ask first, and a new token is shown once and never logged. Expose and relay guests stay separate, so an action always reaches the store its row came from.

## [0.3.0] - 2026-11-06

### Added

- Relay: a Cloudflare Worker (`relay/`) with one Durable Object per node, so machines on different networks and remote assistants can reach each other through a fixed URL you deploy to your own account.
- `link`: keeps a machine connected to the relay over one outbound WebSocket, with at-least-once delivery and replay from the last ack.
- `relay init|admin-token|node add|list|rotate|revoke`: relay configuration and node management. Tokens live in 0600 files.
- Relay guests use the same MCP tools as `expose` (`list_agents`, `ask`, `get_reply`) at `/mcp`.

## [0.2.1] - 2026-11-05

### Fixed

- `agentnet version` printed `dev` in release binaries, and release archives were named after the capitalized repository.

## [0.2.0] - 2026-11-05

### Added

- `expose`: an MCP endpoint (tools `list_agents`, `ask`, `get_reply`) published through a Cloudflare quick tunnel for a bounded time, so remote assistants can message opted-in local agents.
- `guests add|list|approve|revoke`: per-guest tokens stored as hashes, an allowlist of reachable agents, and pairing that can only be approved locally.
- `join --accept-external` and `send --reply-to`.
- `watch`: a live, read-only terminal view of agents, unread counts, messages and pending guests. It never moves a cursor.
- Envelope fields `origin` and `reply_to`.

## [0.1.0] - 2026-11-05

### Added

- `join`, `send`, `read`, `list` and `leave` commands.
- At-least-once delivery with a per-agent read cursor.
- `flock`-based appends, so concurrent senders never interleave.
- A heartbeat-based liveness check that tolerates reused process ids.
- Inbox rotation past 1 MiB once fully read.
- Protocol specification and integration guides.

[Unreleased]: https://github.com/karenrebecag/agentnet/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/karenrebecag/agentnet/releases/tag/v0.2.1
[0.2.0]: https://github.com/karenrebecag/agentnet/releases/tag/v0.2.0
[0.1.0]: https://github.com/karenrebecag/agentnet/releases/tag/v0.1.0
