---
status: done
section: Default --state-dir to ~/.hoot; emit list as JSONL of internal structs
slug: default-state-dir-to-hoot-emit-list-as-jsonl-of-internal-structs
mode: worktree
spec:
created: 2026-05-02T10:03:16Z
---

> ## Default --state-dir to ~/.hoot; emit list as JSONL of internal structs
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Two small changes to the `hoot` CLI:
>
> - Make `--state-dir` optional across all subcommands. Default to `~/.hoot`.
> - Change `hoot list` to emit JSONL where each line is an internal struct serialized directly — drop the bespoke list-only types / mapping layer. One canonical struct, one shape.
>
> - [ ] implement and verify
>   - audit current `--state-dir` flag wiring; pick one place to resolve the default (expand `~`)
>   - identify the bespoke list types and the internal structs they wrap; switch list to marshal the internal structs and delete the dead mapping code
>   - evidence: `hoot list` output before/after, and a run with no `--state-dir` showing it picks up `~/.hoot`

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] audit `--state-dir` wiring — all 5 subcommands already use `defaultStateDir()`; only docs/usage strings show it as required
- [x] capture before-state evidence: `hoot list` text output, plus a run with no `--state-dir` (works)
- [x] update `cmdList` to emit JSONL of `*session.StateFile` directly; drop the alive/created_at text mapping
- [x] update `main.go` usage text + README to reflect optional `--state-dir` (attach.go already documented `default: ~/.hoot`)
- [x] capture after-state evidence: `hoot list` JSONL output, default-state-dir run
- [x] `go build ./... && go test ./...` clean
- [x] populate `## Evidence`, set status: done

## Agent log
- 2026-05-02T10:06Z list emits JSONL of StateFile; usage/README mark --state-dir optional (8c48916)

## Boss log

## Evidence

Commit: `8c48916` on branch `default-state-dir-to-hoot-emit-list-as-jsonl-of-internal-structs` in `~/github.com/hayeah/hootty`.

### `hoot list` — before (text mapping that this change deletes)

```
$ hoot list --state-dir /tmp/before-state
alpha	alive	2026-05-02T17:04:55+07:00
beta	dead	2026-05-02T17:04:56+07:00
```

### `hoot list` — after (JSONL of `session.StateFile`)

```
$ hoot list --state-dir /tmp/before-state
{"session":{"key":"alpha","pid":42761,"created_at":"2026-05-02T17:04:55.413637+07:00"},"state":{"state":"exited","cmd":"/bin/sh -c sleep 60","pid":42762,"started_at":"2026-05-02T10:04:55Z","exited_at":"2026-05-02T10:05:55Z"}}
{"session":{"key":"beta","pid":42770,"created_at":"2026-05-02T17:04:56.475393+07:00"},"state":{"state":"exited","cmd":"/bin/sh -c exit 0","pid":42771,"started_at":"2026-05-02T10:04:56Z","exited_at":"2026-05-02T10:04:56Z"}}
```

Each line round-trips through `jq -c .` cleanly. Each is the exact byte shape of the on-disk `state.json` for that session — no bespoke type, no derived fields. `IsAlive` is no longer in the output; callers that need liveness can probe the flock directly (`session.Store.IsAlive`) or look at `session.pid` plus `state.state`.

### `--state-dir` defaults to `~/.hoot`

Verified by overriding `HOME` so the default resolves under a clean tmp dir:

```
$ FAKE=/tmp/fake-home
$ HOME="$FAKE" hoot run --key gamma -- /bin/sh -c 'sleep 60'
hoot: session "gamma" started (state-dir=/tmp/fake-home/.hoot)
gamma
$ HOME="$FAKE" hoot list
{"session":{"key":"gamma","pid":42875,"created_at":"2026-05-02T17:05:10.979294+07:00"},"state":{"state":"running","cmd":"/bin/sh -c sleep 60","pid":42876,"started_at":"2026-05-02T10:05:10Z"}}
$ ls "$FAKE/.hoot"
gamma
```

`run` (with no `--state-dir`), `list`, and the on-disk layout all pick up `~/.hoot` automatically.

### Help text

```
$ hoot --help | head -13
hoot — reference CLI for the session library

Usage:
  hoot run     [--state-dir <d>] [--key <k>] -- <cmd> [args...]
  hoot list    [--state-dir <d>]
  hoot resolve [--state-dir <d>] <id-or-prefix>
  hoot attach  [--state-dir <d>] [--no-full-replay]
                    [--prefix-key <key>] <id-or-prefix>

The session state directory is <state-dir>/<key>/. The session
serves rpc.sock and writes pty.log inside it. --state-dir defaults
to ~/.hoot. If --key is omitted, a random short id (3-8 chars
from 0-9a-z minus l/o) is generated.
```

### Tests

```
$ go test ./...
ok  	github.com/hayeah/hootty	0.448s
?   	github.com/hayeah/hootty/cmd/hoot	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	0.609s
```

`go build ./...` is clean.

## Trouble report

- The audit was anti-climactic: `defaultStateDir()` already existed in `cmd/hoot/common.go` and every subcommand (`run`, `list`, `resolve`, `attach`, `__session`) already wired `--state-dir` with that default. Only the docs (top-level `usage()` in `main.go` and the README synopsis) still showed it as required. `cmd/hoot/attach.go`'s own `--help` already said `default: ~/.hoot`, so no change there.
- There was no separate "list-only struct" type to delete — the bespoke layer was the inline text mapping (`%s\t%s\t%s` of key/alive/created_at) inside `cmdList`. Replaced that with `json.NewEncoder(os.Stdout).Encode(st)` over `[]*session.StateFile`.
- The JSONL output drops the `alive|dead` flag because `StateFile` doesn't carry liveness — it's a runtime probe of the directory flock. The section text said "one canonical struct, one shape", so I deferred liveness to `session.Store.IsAlive` (or to inspecting `state.state` + `session.pid`) rather than re-introduce a derived field. If the boss wants liveness back, options are: (a) add an `Alive bool` to a thin envelope (re-introduces a list-only type), or (b) add a `--alive` flag that filters by `IsAlive`. Flagging here so the boss can steer.
