# Contributing

Thanks for your interest in agentnet.

## Before you start

- Open an issue first for anything larger than a bug fix. agentnet tries to stay small: a feature that needs a daemon, a network listener or a new dependency needs a strong reason.
- Changes to the on-disk format or the envelope must update [docs/protocol.md](docs/protocol.md) in the same pull request, and must stay readable by the previous version.

## Development

```bash
go vet ./...
go test ./...
```

- One test per behavior or bug fix. A bug fix comes with a test that fails without it.
- Tests must not depend on wall-clock timing. Pass `time.Time` values into the function under test instead.
- No new runtime dependencies. The standard library is the default answer.

## Pull requests

- Commit messages follow `type(scope): description`, for example `fix(core): keep cursor on short read`. The body explains why, not what.
- Do not commit personal data: home directory paths, real session ids, e-mail addresses or account names. Use `alice`, `bob` and placeholder values in tests and docs.

## Security issues

Do not open a public issue for a vulnerability. Contact the maintainer privately through GitHub (Security → Report a vulnerability).
