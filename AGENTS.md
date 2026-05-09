# Repository Guidelines

## Project Structure & Module Organization

This repository contains a Go command-line tool for bridging Docker volumes into Kopia. The module is `github.com/yewfence/volume-backup`. `main.go` wires the executable to the Cobra command tree in `cmd/`. Command implementations, Docker bridge logic, restore flows, hints, and tests live in `cmd/`. `compose.yml` starts Kopia and seed test volumes for local integration work. Tool versions and repeatable tasks are defined in `mise.toml`. Build output belongs in `bin/`, and generated logs belong in `logs/`.

## Build, Test, and Development Commands

Use `mise` so Go and lint versions match the repository.

```sh
mise run test
mise run build
mise run check
mise run run -- --help
```

`mise run test` runs `go test -v ./...`. `mise run build` creates `bin/volume-backup` with the development version string. `mise run check` runs formatting checks, `go vet`, build, and lint. `mise run run` starts the CLI through `go run .`. Run `mise run tidy` after dependency changes.

## Coding Style & Naming Conventions

Use standard Go formatting with tabs, `gofmt`, and idiomatic mixedCaps names. Keep Cobra command constructors named `new<Name>Command`, option structs named after their command area, and constants near the behavior they configure. Prefer small functions with explicit error returns. Existing user-facing command errors are Chinese, so keep new CLI errors consistent.

## Testing Guidelines

Tests use the standard Go `testing` package and live next to the code as `*_test.go`, mainly under `cmd/`. Name tests with `Test<Behavior>` and use table tests for validation rules or path sanitization. Run `mise run test` before opening a pull request. Add focused tests for command validation, Docker option construction, restore safety checks, and helpers that transform names or paths.

## Commit & Pull Request Guidelines

Recent history uses short Conventional Commit style messages such as `feat: restore command`, `fix: backup single /volumes/<name> not working`, and `refactor: standardized go cli project`. Prefer `feat`, `fix`, `docs`, `refactor`, `test`, or `chore` with an imperative summary. Pull requests should describe behavior changes, list validation commands, link issues when available, and include terminal output only when command behavior or Kopia integration output changes.

## Security & Configuration Tips

This tool talks to the Docker daemon and may mount Docker volumes. Do not commit real repository credentials, Kopia passwords, generated logs, or local backup data. Treat `compose.yml` credentials as development-only defaults. Be careful with `/var/run/docker.sock`, `/mnt/volumes-backup`, `/volumes`, and restore targets because mistakes can expose or overwrite data.
