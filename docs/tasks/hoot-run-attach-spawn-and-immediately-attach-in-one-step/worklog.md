---
status: done
section: hoot run --attach: spawn and immediately attach in one step
slug: hoot-run-attach-spawn-and-immediately-attach-in-one-step
mode: worktree
spec: spec.md
created: 2026-05-05T16:55:12Z
---

> ## hoot run --attach: spawn and immediately attach in one step
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Add `--attach` to `hoot run`. After the supervisor starts and the session id is assigned, the same process flips into attach mode against the new session — equivalent to `hoot run -- ...; hoot attach <key>` chained, but in one invocation.
>
> Behavior:
> - `hoot run --attach -- bash` — start a bash session, attach to it. On detach (`<prefix>.`), the session keeps running in the background (same as a separate `hoot attach` that detaches).
> - Default: attach is local (rpc.sock dialer). `--remote` is honored — `hoot run --remote ssh://devbox --attach -- bash` spawns on the remote and attaches via the same transport. (This composes with the in-flight `--remote` work; the `--attach` flag itself is transport-agnostic.)
> - All existing `hoot attach` flags work: `--no-ascii-cinema-playback`, `--prefix-key`, `--no-reconnect`. They're applied to the post-spawn attach phase.
> - Banner: the connect banner already names the session, so the user sees `[connected. <key> @ ...]` after the spawn.
>
> Implementation notes:
> - The `runAttach` core in `cmd/hoot/attach.go` is already factored to take a dialer parameter (added in the remote-attach work). Reuse it directly.
> - Race: between supervisor start and rpc.sock readiness, attach may dial too soon. The serve-bridge already handles "session not ready yet" — but if attaching directly to the local sock, we may need to wait for `rpc.sock` to exist or for the supervisor's first state publish.
>
> - [ ] add the flag, wire it through, verify
>   - evidence: `hoot run --attach -- bash -c 'echo hi; sleep 30'` shows the connect banner + `hi`, detaches cleanly, leaves the session running (`hoot list` confirms)
>   - evidence: same with `--remote ssh://...` if the --remote work has landed by then

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Inspect current `cmdRun`, `cmdAttach`, remote transport, and tests
- [x] Add `--attach` flag parsing and validation for attach-phase options
- [x] Wire local `run --attach` through `runAttachLoop` after socket readiness
- [x] Wire remote `run --attach` through `POST /sessions` then `dialAttachRaw`
- [x] Add tests for remote `run --attach` request sequencing and attach option propagation
- [x] Update CLI help and README
- [x] Run focused Go tests and manual local attach smoke evidence
- [x] Finalize evidence, trouble report, and status

## Agent log

- 2026-05-05T16:57:49Z — Checked out `github.com/hayeah/hootty` via `boss checkout`, read run/attach/remote code, and wrote `spec.md` for the implementation plan.
- 2026-05-05T17:01:35Z — Implemented `hoot run --attach`, remote/local attach handoff, attach option parsing reuse, tests, and docs in hootty commit `abb3c45`.
- 2026-05-05T17:04:39Z — Verified `go test ./...`, `make build`, local `run --attach` PTY smoke, and remote HTTP `run --attach` PTY smoke. Marking done for boss review.

## Boss log

## Evidence

- Commit: hootty `abb3c45 Add run attach mode`

- Tests:

```text
$ go test ./...
ok  	github.com/hayeah/hootty	0.588s
ok  	github.com/hayeah/hootty/cmd/hoot	(cached)
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	0.676s
ok  	github.com/hayeah/hootty/internal/sshtransport	1.083s

$ make build
go build -o bin/hoot ./cmd/hoot
```

- Local PTY smoke, driven by `tmp/run_attach_smoke.py`:

```text
$ python3 tmp/run_attach_smoke.py repos/github.com/hayeah/hootty/bin/hoot
LOCAL_TRANSCRIPT_BEGIN
hoot: session "smoke123" started (state-dir=...)

[connected. smoke123 @ local]
...hi...
[disconnected. smoke123 @ local]
LOCAL_TRANSCRIPT_END
LOCAL_LIST_BEGIN
{"session":{"key":"smoke123","pid":54287,...},"state":{"state":"running","cmd":"bash -c echo hi; sleep 30","pid":54288,...}}
LOCAL_LIST_END
```

- Remote HTTP PTY smoke from the same harness:

```text
REMOTE_HTTP_TRANSCRIPT_BEGIN
hoot: session "smoke456" started (remote=http://127.0.0.1:56540)

[connected. smoke456 @ http://127.0.0.1:56540]
...hi...
[disconnected. smoke456 @ http://127.0.0.1:56540]
REMOTE_HTTP_TRANSCRIPT_END
REMOTE_HTTP_LIST_BEGIN
{"session":{"key":"smoke456","pid":54293,...},"state":{"state":"running","cmd":"bash -c echo hi; sleep 30","pid":54294,...}}
REMOTE_HTTP_LIST_END
```

- Remote unit coverage: `TestCmdRunRemoteAttachSpawnsThenAttaches` verifies `hoot run --remote <addr> --attach` sends `POST /sessions`, upgrades `GET /sessions/<key>/attach-raw`, emits the connect banner/snapshot, and forwards playback attach options in the `Hello`.

## Trouble report

- 2026-05-05T16:57:49Z — `find` emitted a mise tracked-config warning while checking for `devport.local.toml`; no devport.local.toml was present, so no dev service was started.
- 2026-05-05T17:04:39Z — Real `ssh://` smoke was not available: `ssh -o BatchMode=yes localhost 'command -v hoot && hoot --help >/dev/null'` exited 1 with no output. I used remote HTTP smoke plus remote attach unit coverage instead.
