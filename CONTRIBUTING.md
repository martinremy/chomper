# Contributing to chomper

Thanks for your interest in improving chomper. This doc is short on
purpose — the codebase is small and the conventions are conventional.

## Local development

```sh
git clone https://github.com/martinremy/chomper.git
cd chomper
go build ./...
go test ./...
```

To install your working copy onto your `PATH`:

```sh
bin/install
```

Re-run `bin/install` any time to rebuild and re-link. See the [README's
Install section](README.md#-install) for the full rationale.

## Before opening a PR

- `go vet ./...` — must pass
- `go test -race ./...` — must pass
- `gofmt -s -w .` — keep the diff free of formatting churn
- New behavior gets a test if the code is pure logic (see the testing
  notes in [README.md](README.md#-testing) for what we test vs. what
  we treat as integration territory)

CI runs the same three commands (`go vet`, `go build`, `go test
-race`); see [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Filing issues

[Open an issue](https://github.com/martinremy/chomper/issues/new) with:

- What you tried
- What happened
- What you expected
- Output of `chomper doctor` (it lists your harness, gh, git, and
  config state in one shot)

## Scope

Chomper is deliberately small — a thin orchestrator around `gh`, `git`,
and a harness CLI. Features that push it toward "owns more state" or
"reimplements a thing `gh` already does" are unlikely to land. Features
that make the loop more honest under failure are very welcome.

## License

By contributing, you agree your contributions will be licensed under
the project's [MPL-2.0 license](LICENSE). Please leave the Mozilla
short-notice header at the top of new source files (see any existing
`.go` file for the canonical form).
