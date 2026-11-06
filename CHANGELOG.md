# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
