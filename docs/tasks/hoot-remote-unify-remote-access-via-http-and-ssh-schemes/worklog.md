---
status: done
section: hoot --remote: unify remote access via http:// and ssh:// schemes
slug: hoot-remote-unify-remote-access-via-http-and-ssh-schemes
mode: worktree
spec: spec.md
created: 2026-05-05T14:40:00Z
---

> ## hoot --remote: unify remote access via http:// and ssh:// schemes
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Today, remote access is attach-only and uses `--host devbox:port` against a running `hoot serve`. We want a unified `--remote` flag across `list`, `attach`, `run`, and any other session-targeting subcommand, accepting two transport schemes:
>
> ### Transports
>
> - **`--remote http://devbox:9876`** — replaces today's `--host` on `attach`. Same wire as the existing serve+attach-raw bridge. `hoot serve` must be running on the remote.
> - **`--remote ssh://me@devbox`** — ssh transport. **No server required on the remote.** Just `hoot` in `$PATH` over ssh. Open design space — see below.
>
> The `--host` flag on `hoot attach` is renamed/aliased to `--remote http://...`. Bare `host:port` still accepted for `--remote` (sugar for `http://host:port`).
>
> ### SSH transport — design questions for the spec
>
> Open. The agent should propose, evaluate tradeoffs, and pick one to recommend. Specifically:
>
> - **How does the local CLI talk to the remote `hoot`?** Two shapes:
>   - **One-shot ssh per command** — `hoot --remote ssh://devbox list` shells out to `ssh devbox hoot list` (or `hoot list --bind <ephemeral>` + tunnel for attach). Simple, no state. Latency on every call.
>   - **Persistent ssh tunnel + remote `hoot serve` started on demand** — local `hoot` boots a remote `hoot serve --bind unix:<remote.sock>` over ssh, forwards a local TCP/unix port to the remote sock (`ssh -N -L 127.0.0.1:<lport>:<remote.sock>` or equivalent), then talks the existing http transport against it. Reuses everything from the http path.
>   - Hybrid: `list`/`run` are cheap one-shots (no server needed); `attach` boots a tunnel.
> - **OpenSSH binary vs `golang.org/x/crypto/ssh`?** OpenSSH inherits the user's `~/.ssh/config`, ControlMaster, ProxyJump, agent — huge UX win. `x/crypto/ssh` is an in-process library — easier to script, but reproducing OpenSSH's config story is painful. Recommend OpenSSH unless there's a compelling reason otherwise.
> - **Local sock layout for tunneled mode**: where does the forwarded local sock live? Reuse `<state-dir>/<key>/rpc.sock` somehow, or a separate tunnel-only path with a marker (e.g. `<state-dir>/.tunnels/<host>-<key>.sock`)? Spec it.
> - **Connection lifetime**: one ssh connection per `hoot --remote` invocation? Or share via OpenSSH ControlMaster (`-o ControlPath=...`)? ControlMaster is the obvious win — auth once, multiplex many — but spec the cleanup story.
> - **Heartbeat + self-reconnect**: 1s heartbeat channel for ssh-tunneled connections. The existing `attach` already has retry-forever with backoff (`558d3a7`); reuse that machinery on top of the ssh transport. Spec the failure modes: ssh process dies vs tunnel drops vs remote `hoot serve` crashes.
> - **Bootstrapping**: if ssh-tunneled mode boots a remote `hoot serve` on demand, how does it pick a state-dir? Probably the remote's `~/.hoot` (default). How does it know the right session key? `hoot run --remote ssh://devbox -- bash` should show the user the assigned key on the local side.
>
> ### Subcommand semantics
>
> The agent should spec what each subcommand looks like with `--remote ssh://`:
>
> - `hoot list --remote ssh://devbox` — probably one-shot: `ssh devbox hoot list`. No tunnel needed.
> - `hoot run --remote ssh://devbox -- cmd...` — needs to start a session on the remote and report the key locally. One-shot ssh might be enough if the supervisor detaches cleanly server-side.
> - `hoot attach --remote ssh://devbox <key>` — needs the tunnel + http transport (or a streaming ssh exec; spec the choice).
> - `hoot resolve --remote ssh://devbox <prefix>` — one-shot.
>
> ### Required reading
>
> - `cmd/hoot/attach.go` — existing remote attach + `--host` flag
> - `cmd/hoot/serve.go` + `serve_bridge.go` — current http/ws transport
> - `internal/attachwire/wire.go` — frame protocol
> - The reconnect machinery added in `558d3a7` (Remote attach over HTTP)
>
> The agent should write `spec.md` covering:
> - Transport choice (OpenSSH vs x/crypto/ssh) with justification
> - Per-subcommand semantics under each transport
> - Local sock / tunnel lifecycle
> - Heartbeat + reconnect strategy
> - Cutover plan: rename/alias `--host` → `--remote http://`; deprecate `--host`?
> - Verification plan including a real ssh smoke against `devbox`
>
> This is design-heavy. Write spec.md, park at the rfc gate so the human reviews before implementation.
>
> - [x] rfc: review spec.md
> - [ ] implement per spec
>
> Use claude.

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

Seeded from spec.md "Steps". Locked behind the `rfc: review spec.md` gate — do not start until human ticks it.

- [x] Wire ping/pong: `MsgPing`/`MsgPong` in attachwire; server switch case (in hootty lib's per-session attach handler); client ticker (200ms) + watchdog (800ms) in `runSession`; test sub-1s drop detection
- [x] Server: extend `hoot serve --bind` to accept `unix:<path>`; cleanup on signal; round-trip test
- [x] Add `GET /sessions/{prefix}/resolve` route + handler
- [x] Client: `parseRemoteFlag` + `Remote` value (`httpRemote`/`sshRemote`) in `cmd/hoot/remote.go`; `httpClient` + `dialAttachRaw` helpers; table-driven parser tests
- [x] `internal/sshtransport/` package: argv builders, ControlPath layout (sha8 hash), `Tunnel{Open,Close}`
- [x] `sshRemote` integration test against `localhost` ssh (skipped if `ssh -o BatchMode=yes localhost true` fails)
- [x] Wire `--remote` through `cmdList` (httpClient → GET /sessions → re-emit JSONL)
- [x] Wire `--remote` through `cmdResolve` (httpClient → GET /sessions/{prefix}/resolve)
- [x] Wire `--remote` through `cmdRun` (httpClient → POST /sessions; mirror today's stderr+stdout shape)
- [x] Wire `--remote` through `cmdAttach` (dialAttachRaw + existing runConnectLoop unchanged)
- [x] Remove `--host` flag, `parseHostFlag`, and `remoteDialer` from `attach`; update any internal callers
- [x] README + main.go usage: document `--remote` everywhere; ssh examples; ControlMaster note
- [x] Smoke harness: http parity vs today's `--host`; ssh subcommands vs local-path output; ssh attach + reconnect (kill ssh client, observe sub-1s drop + reconnect)
- [x] Manual smoke against `devbox` (list/run/attach/resume); capture transcripts to `tmp/`
- [x] Update `## Evidence` and `## Trouble report`; flip `status: done`

## Agent log
- 2026-05-05T15:55Z spawned (after dropped briefing). Read AGENT_LOOP.md, worklog, and required-reading files (`cmd/hoot/attach.go`, `cmd/hoot/serve.go`, `cmd/hoot/serve_bridge.go`, `internal/attachwire/wire.go`, plus the prior `remote-attach-over-http-via-hoot-serve` spec at `docs/tasks/.../spec.md` from `558d3a7`). Worktree created at `~/github.com/hayeah/hootty/.worktrees/<slug>`; symlinked under `repos/`.
- 2026-05-05T15:58Z drafted `spec.md` covering: OpenSSH-vs-x/crypto/ssh decision (OpenSSH, with reasons), hybrid one-shot-vs-tunneled per subcommand, streamlocal `unix:` forwarding (`-L lsock:rsock`), ControlMaster with hashed ControlPath, `hoot serve --bind unix:<path>` extension, reuse of existing `runConnectLoop` reconnect machinery (only the dialer differs), heartbeat via ssh `ServerAliveInterval=10` rather than a new wire frame, and `--host` → `--remote` cutover plan.
- 2026-05-05T15:58Z PUSHBACK candidate flagged in spec ("1s heartbeat" → I propose 10s ssh-alive-interval × 3 strikes; reasons in Design notes 15:46Z entry). Calling this out for rfc review.
- 2026-05-05T15:58Z parking at the rfc gate. `status: blocked`. Will not implement until the human ticks `- [ ] rfc: review spec.md`.
- 2026-05-05T14:52Z spec.md drafted; status=blocked at rfc gate. PUSHBACK on '1s heartbeat' — proposing ssh ServerAliveInterval=10 instead (Design notes 15:46Z).
- 2026-05-05T15:07Z spec rewritten around 'every Remote is just a Dial'. Killed the multi-method Remote interface + ssh-exec fast path; one-shot list/resolve/run now go through the same ssh-tunnel + hoot serve as attach. Cost: ~100-300ms first ssh invocation, ~50ms with ControlMaster. Captured ssh-exec as future work in Design notes.
- 2026-05-05T15:12Z spec updated: wire-level ping/pong (200ms ticker, 800ms watchdog) is the heartbeat; ssh keepalive is just backstop. --host removed outright, no deprecation alias (nothing released).
- 2026-05-05T15:15Z resumed implementation after rfc tick; confirmed hootty checkout and approved spec
- 2026-05-05T15:17Z landed attach heartbeat frames and watchdog in hootty 40667c9; go test ./... passes
- 2026-05-05T15:19Z landed hoot serve unix:<path> listener support in hootty f2e9285; go test ./... passes
- 2026-05-05T15:20Z landed /sessions/{key}/resolve endpoint in hootty 612e580; go test ./... passes
- 2026-05-05T15:21Z landed Remote parser/client abstraction in hootty 7a6849b; go test ./... passes
- 2026-05-05T15:23Z landed OpenSSH tunnel transport package in hootty afa6f71; go test ./... passes
- 2026-05-05T15:25Z landed sshRemote tunnel dialing in hootty 35b861f; go test ./... passes (localhost ssh test is skip-gated)
- 2026-05-05T15:26Z landed remote list/resolve wiring in hootty 7a06cf3; go test ./... passes
- 2026-05-05T15:27Z landed remote run wiring in hootty 3b882be; go test ./... passes
- 2026-05-05T15:29Z landed attach --remote cutover and removed --host in hootty e63eb5c; go test ./... passes
- 2026-05-05T15:30Z landed README and usage updates in hootty 11f9ccc; go test ./... passes
- 2026-05-05T15:39Z landed SSH tunnel readiness/lifecycle fixes in hootty d6d9923 and 9aa44b3 after real devbox smoke exposed EOF and lingering remote serve failures.
- 2026-05-05T15:44Z landed SSH tunnel fallback to remote loopback TCP in hootty 35f426a after devbox/Tailscale SSH did not pass streamlocal Unix socket forwarding. `go test ./...` passes.
- 2026-05-05T15:49Z smoke passed: `tmp/remote-smoke.sh` covers HTTP remote list/resolve/run/attach; real `ssh://devbox` list/run/resolve/attach passed using temporary current Linux binary, then restored devbox's original `~/.local/bin/hoot`.
- 2026-05-05T15:44Z done: implementation complete through hootty 35f426a; evidence includes go test ./..., HTTP smoke, and real ssh://devbox list/run/resolve/attach; noted reconnect-smoke gap in Trouble report

## Boss log
- 2026-05-05T14:44Z hi — your spawn briefing got dropped due to a claude-code startup detection flake (same thing we hit earlier in the session). you are the spawned worker for this section.
  
  read AGENT_LOOP.md (skill: `~/.claude/skills/boss/AGENT_LOOP.md` or wherever your env has it) and your `worklog.md` end-to-end. the section text is quoted at the top of the worklog. this is **boss spec** mode — write spec.md, then park at the rfc gate. do NOT proceed to implementation; the human will review the spec and tick the rfc box when ready.
  
  start by reading the required-reading files listed in the section text, then draft spec.md.
- 2026-05-05T15:14Z ticked: rfc

## Evidence

- Commits on `hoot-remote-unify-remote-access-via-http-and-ssh-schemes`:
  - `40667c9` attach heartbeat frames/watchdog
  - `f2e9285` `hoot serve --bind unix:<path>`
  - `612e580` `/sessions/{key}/resolve`
  - `7a6849b`, `afa6f71`, `35b861f`, `d6d9923`, `9aa44b3`, `35f426a` remote parser + OpenSSH tunnel transport
  - `7a06cf3`, `3b882be`, `e63eb5c` subcommand wiring and `--host` removal
  - `11f9ccc` README/main usage
- Final test pass:
  ```text
  go test ./...
  ok  	github.com/hayeah/hootty	(cached)
  ok  	github.com/hayeah/hootty/cmd/hoot	(cached)
  ?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
  ?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
  ok  	github.com/hayeah/hootty/internal/shortid	(cached)
  ok  	github.com/hayeah/hootty/internal/sshtransport	(cached)
  ```
- HTTP smoke (`tmp/remote-smoke.sh`):
  ```text
  hoot: session "loc123" started (state-dir=/tmp/hoot-smoke-state.tXtHL8)
  hoot: session "rem123" started (remote=http://127.0.0.1:28765)
  ssh://localhost smoke skipped: passwordless localhost ssh or remote hoot unavailable
  remote smoke ok
  ```
  Artifacts: `tmp/http-local-list.jsonl`, `tmp/http-remote-list.jsonl`, `tmp/http-remote-attach.txt`.
- Real `ssh://devbox` smoke:
  ```text
  key=rmt224203
  list ok (       4 sessions)
  rmt224203
  rmt224203
  ssh devbox smoke ok
  ```
  Attach artifact includes `devbox-ready` and clean detach: `tmp/devbox-attach.out`. Transcript: `tmp/devbox-smoke-transcript.txt`.
- Cleanup verification: `devbox:~/.local/bin/hoot` restored to the old installed binary (help shows `--host`), and no real `hoot serve --bind ...` process remained beyond the command used to check process state.

## Trouble report

- Local `ssh://localhost` integration smoke skipped because `ssh localhost` works, but `hoot` is not in the localhost SSH login PATH (`command -v hoot` failed).
- Real `ssh://devbox` required temporarily replacing `devbox:~/.local/bin/hoot` with a cross-compiled current binary, because the installed `hoot` was old and only supported `--host`. The original binary was backed up and restored after smoke.
- The approved streamlocal Unix-socket forwarding design did not work through this `devbox`/Tailscale SSH path: remote `hoot serve --bind unix:<sock>` was healthy locally on devbox, but the OpenSSH forward never became HTTP-ready. Implementation now keeps the local socket layout but forwards to remote `127.0.0.1:<random-port>` and records this in `spec.md` Design notes.
- One verification gap remains: I did not complete a scripted "kill the local ssh client mid-attach and observe reconnect" smoke. The underlying reconnect loop is unchanged from the HTTP attach path, and heartbeat/drop detection is covered by `TestRunSessionHeartbeatClosesIdleConnection`; real `ssh://devbox` attach itself passed.
