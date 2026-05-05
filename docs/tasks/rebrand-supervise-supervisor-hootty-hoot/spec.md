# Rebrand supervise/supervisor to hootty/hoot

## Goal

Rename the standalone `github.com/hayeah/supervisor` repo to its future public identity, `github.com/hayeah/hootty`, without moving the checkout or renaming the GitHub repository. The shipped CLI becomes `hoot`, the default state directory becomes `~/.hoot`, and the TypeScript packages become `@hayeah/hootty-*`. This is intended as a behavioral no-op beyond names, paths, help text, and defaults.

Out of scope: `gh repo rename`, moving `~/github.com/hayeah/supervisor` on disk, and landing a real `hootty.dev` site.

## Architecture

- Go module and imports:
  - `go.mod`: `module github.com/hayeah/hootty`
  - Go imports under root, `cmd/supervise`, and `internal/*`: rewrite `github.com/hayeah/supervisor` to `github.com/hayeah/hootty`.
  - Keep the external module/import path as `github.com/hayeah/hootty`, but use `package session` and `Session*` public API/data names for runtime internals.
  - Rename the state.json namespace from `supervisor` to `session`.
- CLI:
  - Rename `cmd/supervise/` to `cmd/hoot/`.
  - `Makefile`: build `bin/hoot` from `./cmd/hoot`.
  - CLI user-facing text: `supervise` to `hoot`.
  - Internal subcommand: use `__session`, because it preserves the existing one-hidden-subcommand dispatch shape and names the long-lived per-session runtime instead of the external brand.
  - State dir fallback: `~/.hoot`, with relative fallback `.hoot`.
- TypeScript workspace:
  - Move `packages/supervisor-termui` to `packages/hootty-termui`.
  - Move `packages/supervisor-webui` to `packages/hootty-webui`.
  - Package names: `@hayeah/hootty-termui`, `@hayeah/hootty-webui`.
  - Update every workspace import, consumer dependency, root script, lockfile, docs, and comments.
- Docs/config:
  - README: update examples, commands, package names, module import, and add `hootty.dev` in the tagline/footer.
  - `devport.toml`: update session name, binary path, state dir, and webui cwd.
  - Existing historical docs under `docs/tasks/**` are intentionally historical and can keep old names; README should have zero old-name occurrences except explicitly historical reference.
- Sibling dotfiles:
  - Check the worktree for `github.com/hayeah/supervisor`, `hayeah-go/supervisor`, and Go workspace replace directives. Update only if a live module/workspace reference exists.

## Steps

- Check out `hayeah/supervisor` and `hayeah/dotfiles`; inspect worklog/spec state.
- Write this spec and seed the worklog todos.
- Rename Go module/imports, CLI directory/binary/help text, internal subcommand, state dir default, and Go identifiers/comments where they are part of the renamed surface.
- Rename TypeScript package directories, package names, workspace consumers, scripts, and lockfile references.
- Update README and config/user-facing docs for `hoot`, `hootty`, package names, default state dir, and `hootty.dev`.
- Scan for remaining `supervise`/`supervisor` occurrences and classify or fix them.
- Run Go formatting and commit the mechanical rename.
- Verify Go build/test, TS builds, CLI smokes, state-dir creation, dotfiles no-reference check, and README scan.
- Populate evidence and set worklog status to done.

## Verification

- `go build ./...`
- `go test ./...`
- `pnpm -r build`
- `go build -o bin/hoot ./cmd/hoot`
- `./bin/hoot list --state-dir <workspace-tmp>`
- `./bin/hoot run --state-dir <workspace-tmp> --key <key> -- bash -lc 'echo ...; sleep ...'`
- `./bin/hoot serve --state-dir <workspace-tmp> --bind 127.0.0.1:<port>` plus `/healthz` and session list curl.
- `./bin/hoot attach --host 127.0.0.1:<port> <key>` smoke as far as non-interactive PTY automation permits.
- Confirm first run with default `HOME=<workspace-tmp>/home` creates `<home>/.hoot` and not `<home>/.supervise`.
- `rg -n "supervise|supervisor" README.md packages package.json pnpm-lock.yaml devport.toml cmd internal *.go` with any remaining matches explained.
- Dotfiles check: `rg -n "github.com/hayeah/supervisor|hayeah-go/supervisor|supervise|supervisor" -g 'go.mod' -g 'go.work'`.

## Open questions

None. The internal subcommand choice is `__session`.

## Design notes

- 2026-05-05T13:44Z - Picked `__hoot` for the internal subcommand.
  - Alternatives considered:
    - `__hoot`: mirrors the existing `__supervise` implementation, keeps `cmdRun` and `spawnSupervise` as simple argv rewrites, and remains hidden from normal help. This is the lowest-risk mechanical rename.
    - `hoot internal`: nicer English, but it would introduce a second dispatch layer for a private command. That adds code movement during a rename-only section without improving user behavior.
  - Follow-on: helper names like `cmdSupervise` and `spawnSupervise` should become `cmdHoot` / `spawnHoot` to avoid half-renamed internals.
- 2026-05-05T13:49Z - Renamed the public Go package and `state.json` namespace, not just module/import strings.
  - Alternatives considered:
    - Keep `package supervisor`, `SupervisorConfig`, and `state.json.supervisor`: smaller public API delta, but it would leave the most visible code identifiers half-renamed after the section explicitly asked for identifiers too.
    - Rename package/types but keep JSON `supervisor` for backward compatibility: less disruptive for existing state files, but this repo is still in active pre-release task work and the rebrand scope calls for the default state dir and user-facing serialized shape to move together.
    - Full `hootty` rename (picked): `package hootty`, `HoottyConfig`, `Hootty` interface, `HoottyState`, `StateFile.Hootty json:"hootty"`, and `hootty.log`. This makes `hoot list` output and README examples match the final product name.
  - Follow-on: this intentionally changes existing `state.json` shape from `{"supervisor": ...}` to `{"hootty": ...}`. The section framed this as a rename, not a compatibility migration; tests and smoke evidence now assert the new shape.
- 2026-05-05T13:58Z - Superseded the earlier `__hoot` / `Hootty*` internal naming after human feedback.
  - What changed:
    - Hidden command is now `hoot __session`, not `hoot __hoot`.
    - Root package name is `session` while the module path remains `github.com/hayeah/hootty`.
    - Public runtime API/data names are `SessionConfig`, `Session`, `SessionState`, and `StateFile.Session json:"session"`.
    - Runtime log file is `session.log`, not `hootty.log`.
  - Reason: `hootty` is the external brand and CLI/module identity. The long-lived process, state namespace, and API concepts are about a session; using the cute product name there made state and internals less clear.
  - Alternatives considered:
    - Keep `__hoot`/`Hootty*`: mechanically consistent with the external rebrand, but too brand-heavy for internal data and process naming.
    - Use `_session`: close, but the existing hidden-command convention uses a double underscore for private plumbing. The human clarified `__session`.
    - Use `__spawner`: rejected because the internal process does more than spawn; it owns the PTY, recorder, state writer, flock, and rpc.sock for the session lifetime.
  - Follow-on: README examples import the module explicitly as `session "github.com/hayeah/hootty"` so the package name is clear despite the branded module path.
