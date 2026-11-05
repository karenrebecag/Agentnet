# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-05

### Added

- `join`, `send`, `read`, `list` and `leave` commands.
- At-least-once delivery with a per-agent read cursor.
- `flock`-based appends, so concurrent senders never interleave.
- A heartbeat-based liveness check that tolerates reused process ids.
- Inbox rotation past 1 MiB once fully read.
- Protocol specification and integration guides.

[Unreleased]: https://github.com/karenrebecag/agentnet/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/karenrebecag/agentnet/releases/tag/v0.1.0
