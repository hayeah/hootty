# hoot run --attach

## Goal

Add `--attach` to `hoot run` so a single invocation starts a session and then attaches the caller's terminal to the newly-created session. The flag should work for local sessions and with the existing `--remote` transport, and the attach phase should honor the same user-facing attach options as `hoot attach`. Out of scope: changing attach protocol behavior, adding a new transport, or changing session lifecycle semantics after detach.

## Architecture

- `cmd/hoot/run.go`
  - Add `--attach` and attach-phase flags to the `run` flag set: `--no-ascii-cinema-playback`, `--ascii-cinema-playback-window`, `--ascii-cinema-playback-speed`, `--prefix-key`, and `--no-reconnect`.
  - Factor local and remote spawn enough for `cmdRun` to receive the assigned key and, when `--attach` is set, call the existing attach loop instead of printing the key as the final action.
  - For local attach, keep the existing `waitForSocket` readiness barrier and dial `<state-dir>/<key>/rpc.sock` directly through `localDialer`.
  - For remote attach, reuse the same parsed `Remote`, call `POST /sessions`, then attach with `dialAttachRaw(remote, key)`.
- `cmd/hoot/attach.go`
  - Reuse `runAttachLoop` directly. Avoid changing attach protocol internals unless a test exposes a bug.
- `cmd/hoot/main.go` and `README.md`
  - Update usage/help/docs to describe `hoot run --attach` and the attach flags accepted during the post-spawn attach phase.
- Tests
  - Add focused unit coverage for remote `cmdRun --attach` to prove the command POSTs `/sessions`, then upgrades `/sessions/<key>/attach-raw`, and forwards attach options into the `Hello`.
  - Keep local end-to-end verification as manual CLI evidence because it requires a real terminal to detach with the prefix sequence.

## Steps

- Inspect current `cmdRun`, `cmdAttach`, remote transport, and tests.
- Add `--attach` flag parsing and validation for attach-phase options.
- Wire local `run --attach` through `runAttachLoop` after socket readiness.
- Wire remote `run --attach` through `POST /sessions` then `dialAttachRaw`.
- Add tests for remote `run --attach` request sequencing and attach option propagation.
- Update CLI help and README.
- Run focused Go tests and manual local attach smoke evidence.

## Verification

- `go test ./cmd/hoot` passes.
- Build `bin/hoot` with `make build` or the repo's equivalent.
- Manual local transcript: `hoot run --attach -- bash -c 'echo hi; sleep 30'` prints the connect banner and `hi`; sending `<prefix>.` detaches; `hoot list` shows the session still alive.
- Remote evidence if available in this branch/environment: exercise `--remote ssh://... --attach` or provide unit-level remote transport coverage if a real SSH target is not available.

## Open questions

- None. If a real SSH remote is unavailable for evidence, unit coverage plus local CLI smoke will be recorded and the gap will be called out.

## Design notes

- 2026-05-05T16:57:49Z — `cmdRun` already has the two readiness points this feature needs: local spawn waits for `<state-dir>/<key>/rpc.sock`, and remote spawn waits for `POST /sessions` to return a created session key. That means `--attach` can remain a small orchestration layer over existing spawn and attach primitives instead of adding a separate bridge.
- 2026-05-05T17:04:39Z — Added a small `exitError` path in `main.go` so `hoot run --attach` can preserve attach-loop exit codes when the post-spawn attach exits with `2` or `130`.
  - Alternative considered: keep `cmdRun(args) error` only and let `main` map every attach failure to exit 1. That would have been a smaller patch, but it would make `hoot run --attach` less compatible with `hoot attach` for server-side resolve errors and trapped signals.
  - I kept the change narrow: normal `hoot run` errors still print as before and exit 1; only attach-loop handoff returns the sentinel.
