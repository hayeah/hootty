---
title: Refuse nested hoot attach
slug: refuse-nested-hoot-attach
---

## Goal

Stop users from accidentally nesting `hoot` attach sessions. When the
local CLI runs an attach path (`hoot` bare, `hoot @host`, `hoot host`,
`hoot attach`, `hoot run --attach`) inside a shell that hoot itself
spawned, refuse with a tmux-shaped error that names the env var to
unset for an override. Read-only verbs (`hoot list`, `hoot log`,
`hoot resolve`, `hoot kill`, `hoot detach`, `hoot write`) keep working
inside a session — the rule is "don't take over the same terminal
twice", not "ban hoot under hoot".

Out of scope: switching endpoints from inside a session, broadcasting
detach to the outermost attach, handing the inner attach to a sibling
window. Per the section: too much policy, hard to address the right
session.

## Architecture

Two pieces:

- **Mark spawned children.** The user's command runs inside the
  session via `RunCmdService.Run` in `cmd/hoot/service.go:84`, which
  builds `cmd.Env = sanitizeChildEnv(os.Environ())` at line 88.
  Extend `sanitizeChildEnv` to also accept the session key and append
  `HOOT_SESSION=<key>`. Call sites: `service.go:88` (the only one).

- **Refuse nested attaches.** Add a small guard
  `errIfNestedHootSession(verb string) error` in a new
  `cmd/hoot/nesting.go` (or top of `attach.go` — agent's call). It
  reads `os.Getenv("HOOT_SESSION")` and, if non-empty, returns an
  error in the tmux house style:

      hoot <verb>: sessions should be nested with care, unset $HOOT_SESSION to force

  Call it at:

  - `cmdAttach` (cmd/hoot/attach.go) — first thing after flag parse,
    before any dial.
  - `cmdShell` (cmd/hoot/shell.go) — first thing after flag parse,
    before the local spawn or remote POST.
  - `cmdRun` (cmd/hoot/run.go) — only when `--attach` is set, after
    flag parse but before any spawn or remote POST. `hoot run` without
    `--attach` is fire-and-forget; it doesn't take over the terminal,
    so it stays allowed inside a session.

  Verb names: `attach`, `shell` (for bare `hoot` / `hoot @host` /
  fallthrough), `run --attach`.

Why the guard sits in the local CLI process and not in the server:
the env var is on the user's *interactive* shell, which is the same
process tree as the one that fires off the nested `hoot` invocation.
The local CLI process inherits `HOOT_SESSION` automatically. The
remote server doesn't see local env, doesn't need to — refusal is a
terminal-takeover concern, not a session-state concern.

## Steps

- Read existing code and confirm flow (done).
- Inject `HOOT_SESSION=<key>` into the spawned child env via
  `sanitizeChildEnv`.
- Add `errIfNestedHootSession` helper + a small message string.
- Wire the guard into `cmdAttach`, `cmdShell`, `cmdRun` (when
  `--attach` is set).
- Unit tests:
  - `sanitizeChildEnv` injects `HOOT_SESSION=<key>`.
  - The guard returns an error when env set, nil otherwise.
  - `cmdAttach`, `cmdShell` refuse when env set; both produce a
    sentinel exit/error with the tmux-shaped message.
  - `cmdRun --attach` refuses when env set; `cmdRun` without
    `--attach` is allowed.
  - `cmdList` is allowed (sanity).
- Update README and `usage()` text in `main.go` to mention nested
  refusal + the env-var override knob.
- Verify: build the binary, drive a real local nest manually
  (`hoot run -- bash`, attach, then from inside try `hoot`,
  `hoot attach`, `hoot list`).

## Verification

- `go test ./cmd/hoot/...` passes including the new tests.
- Manual e2e in a real terminal:
  - Spawn a hoot session, attach, drop into bash.
  - Inside that bash, `echo $HOOT_SESSION` shows the session key.
  - `hoot` (bare) → refuse with the tmux-shaped error, exit non-zero.
  - `hoot attach <key>` → refuse.
  - `hoot run --attach -- bash` → refuse.
  - `hoot list`, `hoot log <key>` → succeed.
  - `unset HOOT_SESSION; hoot list` → succeed (override path works).

Captured transcripts go into `## Evidence`.

## Open questions

None — section text is specific enough; env var name is `HOOT_SESSION`
per the section's suggestion; value is the session key (matches
`$TMUX` carrying its socket path).

## Design notes

(to be appended as work proceeds)
