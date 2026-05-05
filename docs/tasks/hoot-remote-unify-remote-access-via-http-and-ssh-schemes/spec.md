# `hoot --remote`: unify remote access via `http://` and `ssh://` schemes

## Goal

Replace the attach-only `--host devbox:port` flag with a unified `--remote <url>` flag that works on every session-targeting subcommand (`list`, `resolve`, `run`, `attach`) and accepts two transport schemes:

- `--remote http://devbox:9876` — same wire as today's serve+attach-raw bridge. `hoot serve` must already be running on the remote.
- `--remote ssh://me@devbox` — ssh transport. **No long-lived server required on the remote.** Just `hoot` in `$PATH` reachable over ssh.

The existing `--host` on `hoot attach` is renamed to `--remote`. Bare `host:port` is sugar for `http://host:port` (matches the current `--host` accept-list). The local-default path (no flag) is unchanged.

### Design pivot: every remote is "an HTTP byte stream"

Earlier draft modelled remotes as a multi-method `Remote` interface with separate code paths per subcommand (`ExecOneShot` for cheap commands via raw ssh exec, `DialAttach` for the upgrade dance). Killed in favor of:

> **A `Remote` is a `Dial(ctx) (net.Conn, error)` that returns a byte stream speaking HTTP to a `hoot serve`. Every subcommand — list/resolve/run/attach — is just an HTTP request over that stream.**

For ssh: every invocation spawns a remote `hoot serve --bind unix:<rsock>` and forwards a local sock to it via `ssh -L`. When the local `hoot` process exits, ssh terminates, the remote serve dies with SIGHUP and unlinks rsock. Zero persistent state. Tradeoff is ~100–300ms of ssh+serve spawn cost on each `hoot --remote ssh://…` invocation; ControlMaster amortizes it for back-to-back invocations.

This is consciously the prototype-first shape. We can layer optimizations (long-lived per-host serves, in-process daemon, ssh-exec fast path for `list`) later without changing the contract — they each just become a different `Remote` implementation. See "Why not the ssh-exec fast path" in Design notes.

### In scope

- One `--remote` flag with consistent parsing across subcommands.
- `Remote` value type with two concrete implementations: `httpRemote`, `sshRemote`.
- Every subcommand routed through `http.Client` (or the upgrade-dance wrapper for attach).
- New `hoot serve --bind unix:<path>` so the tunneled serve can bind a per-attach unix socket.
- New `GET /sessions/{prefix}/resolve` route so `hoot resolve` over remote doesn't need any new wire shape.
- **Wire-level ping/pong on attachwire** (new `MsgPing`/`MsgPong` frames) for sub-1s dead-connection detection. Works on every transport — local, http, ssh.
- OpenSSH ControlMaster reuse for cheap back-to-back invocations.
- Reuse of the existing reconnect machinery (`558d3a7`) on the ssh path — only the dialer differs.
- Remove `--host` from `attach` outright — replaced by `--remote`. Nothing released, no compat needed.

### Out of scope

- TLS cert pinning, auth tokens (Tailscale + OpenSSH gates the network).
- `golang.org/x/crypto/ssh` in-process implementation. We shell out to `ssh(1)`. See "OpenSSH vs x/crypto/ssh" in Design notes.
- Browser-side changes (`/sessions/{key}/attach` keeps its current shape).
- Multi-host fan-out (`--remote ssh://a,b,c`).
- A "deploy `hoot` to remote" bootstrapper. The user installs `hoot` on the remote `$PATH` themselves.
- Any optimization for ssh one-shot latency (long-lived per-host serve, ssh-exec fast path, etc.). Captured as future work.

## Architecture

### Files touched

| File | Change |
|---|---|
| `cmd/hoot/main.go` | Usage block: document `--remote` on every targeting subcommand. |
| `cmd/hoot/remote.go` (new) | `parseRemoteFlag`, `Remote` struct (`Dial` + `Close`), `httpRemote` and `sshRemote` constructors, `httpClient(r) *http.Client`, `dialAttachRaw(r, key)`. |
| `internal/sshtransport/` (new) | `Tunnel{Open, Close}` and helpers: argv builder, ControlPath layout (sha8 hash of `user@host:port`), readiness probe. |
| `cmd/hoot/attach.go` | Replace `--host` with `--remote`. Use `dialAttachRaw(r, key)` from remote.go in place of `remoteDialer`. Add ping ticker + watchdog inside `runSession`; `runConnectLoop` is unchanged. |
| `internal/attachwire/wire.go` | Add `MsgPing = 0x06`, `MsgPong = 0x07`. Empty payloads. |
| hootty per-session attach handler (in the library) | On `MsgPing` → write `MsgPong`. One switch case. |
| `cmd/hoot/list.go` | Add `--remote`. Local path unchanged. Remote path: `httpClient(r).Get("/sessions")`, decode and re-emit as JSONL. |
| `cmd/hoot/list.go` (`cmdResolve`) | Add `--remote`. Remote path: `GET /sessions/{prefix}/resolve` → `{key}`. |
| `cmd/hoot/run.go` | Add `--remote`. Remote path: `POST /sessions {cmd, argv, key?}`, decode `{key}`, mirror today's stderr+stdout shape. |
| `cmd/hoot/serve.go` | Extend `--bind` to accept `unix:<path>`; add SIGHUP handler that unlinks the sock and exits cleanly. |
| `cmd/hoot/serve_bridge.go` | Add `GET /sessions/{prefix}/resolve` (small handler — `store.Resolve` + JSON). The existing `handleAttachRaw` already handles tunneled clients without modification. |
| `README.md` | Document `--remote`, `ssh://` examples, ControlMaster note. |
| `devport.toml` | No change (devport drives the local `hoot serve`; `--remote` is a client-side flag). |

### `--remote` value parsing

`parseRemoteFlag(s string) (Remote, error)`:

| Input | Parsed as |
|---|---|
| `""` | nil (no flag — caller falls back to local path) |
| `m4mini:20000` | `httpRemote{URL: http://m4mini:20000}` (sugar; matches today's `--host`) |
| `http://m4mini:20000` | `httpRemote{...}` |
| `https://m4mini` | `httpRemote{...}` (TLS) |
| `ssh://devbox` | `sshRemote{user: "", host: "devbox"}` |
| `ssh://me@devbox` | `sshRemote{user: "me", host: "devbox"}` |
| `ssh://me@devbox:2222` | `sshRemote{user: "me", host: "devbox", port: 2222}` |
| anything else | error → exit 2 |

Path/query are stripped (we never want the user to type a path in `--remote`).

### The `Remote` value

```go
type Remote struct {
    // Dial returns a byte stream speaking HTTP to a hoot serve.
    // For sshRemote, the first call lazily spawns the tunnel.
    Dial func(ctx context.Context) (net.Conn, error)

    // Close tears down any persistent state held by the remote
    // (e.g. the ssh process for sshRemote). Safe to call multiple times.
    Close func() error
}
```

Helpers built on top:

```go
// httpClient returns an *http.Client whose transport dials via r.Dial.
// All non-attach subcommands use this.
func httpClient(r *Remote) *http.Client {
    return &http.Client{Transport: &http.Transport{
        DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
            return r.Dial(ctx)
        },
    }}
}

// dialAttachRaw is the attach path: dial + write the
// GET /sessions/<key>/attach-raw Upgrade request + read the 101.
// Returns a conn ready for attachwire frames. Reuses the existing
// upgradeAttachConn logic from attach.go verbatim.
func dialAttachRaw(r *Remote, key string) func(ctx) (net.Conn, error) { … }
```

Subcommand wiring:

```go
// list:
resp, err := httpClient(r).Get("http://hoot/sessions")  // host doesn't matter; DialContext ignores it
…
// attach:
exitCode, err := runAttachLoop(dialAttachRaw(r, key), …)
defer r.Close()
```

The HTTP host in the URL is meaningless for ssh-tunneled and even for http (we already dial directly), but Go's `http.Client` requires a parseable URL. We use `http://hoot/...` as a placeholder in client code — symmetrical with `http://hoot/attach` already used in `serve_bridge.go:203`.

### `httpRemote` implementation

```go
Dial = func(ctx) (net.Conn, error) {
    if scheme == "https" {
        return tls.Dial("tcp", host, tlsConfig)
    }
    return net.Dial("tcp", host)
}
Close = func() error { return nil }
```

That's it. Identical to today's `remoteDialer` minus the upgrade step (which moved to `dialAttachRaw`).

### `sshRemote` implementation

State: a `*sshtransport.Tunnel` initialized lazily on first `Dial`.

```go
Dial = func(ctx) (net.Conn, error) {
    if tunnel == nil {
        t, err := sshtransport.Open(ctx, sshtransport.Config{
            User: r.user, Host: r.host, Port: r.port,
            Lsock: <state-dir>/.tunnels/fwd-<host>-<pid>-<uuid>.sock,
            Rsock: ~/.hoot/.tunnels/<uuid>.sock,
            ControlPath: <state-dir>/.tunnels/cm-<sha8(user@host:port)>,
        })
        if err != nil { return nil, err }
        tunnel = t
    }
    return net.Dial("unix", tunnel.Lsock)
}
Close = func() error {
    if tunnel != nil { return tunnel.Close() }
    return nil
}
```

`Tunnel.Open` does:

1. Pick `lsock` and `rsock` paths.
2. `os.Remove(lsock)` if stale.
3. Spawn:
   ```
   ssh -o ControlMaster=auto
       -o ControlPath=<cm path>
       -o ControlPersist=60
       -o ServerAliveInterval=10
       -o ServerAliveCountMax=3
       -o ExitOnForwardFailure=yes
       -L <lsock>:<rsock>
       <user@host or host>
       hoot serve --bind unix:<rsock>
   ```
4. Poll `lsock` for up to 5s with 50ms backoff. First successful `unix.Dial` proceeds. Failure → kill ssh, capture its stderr, return error with that stderr included.
5. Return `&Tunnel{cmd, lsock}`.

`Tunnel.Close`:

1. `cmd.Process.Signal(SIGTERM)` (own pgrp via `Setpgid: true`, so this hits the pgrp leader cleanly).
2. Wait up to 1s; on timeout `cmd.Process.Kill()`.
3. `os.Remove(lsock)` (best-effort).
4. ControlMaster sock is left for `ControlPersist=60s` to reap — not our problem.

Lifecycle: `r.Close()` is called from the subcommand on exit (`defer r.Close()` after parsing the flag). For attach, that's after the connect loop returns; for one-shots, after the http call.

### Per-subcommand semantics

All use the same `Remote.Dial` underneath. Per-subcommand differences are just which HTTP request they make.

| Subcommand | Local | Remote (both `http://` and `ssh://`) |
|---|---|---|
| `list` | `store.List()` | `GET /sessions` → decode → emit JSONL |
| `resolve` | `store.Resolve(prefix)` | `GET /sessions/{prefix}/resolve` → `{key}` |
| `run` | fork `__session` | `POST /sessions {cmd, argv, key?}` → `{key}` → mirror today's stderr+stdout |
| `attach` | dial `<state-dir>/<key>/rpc.sock` | dial via `Remote.Dial`, then HTTP/1.1 Upgrade against `/sessions/{key}/attach-raw` |

The local path stays special (per-session `rpc.sock`, no `serve` mux). The `Remote` abstraction kicks in only when `--remote` is set.

### Server changes

1. **`hoot serve --bind unix:<path>`** in `cmd/hoot/serve.go`:
   - Parse `unix:` prefix; on match, `net.Listen("unix", path)` and `(&http.Server{Handler: mux}).Serve(listener)`.
   - Ensure parent dir exists (mode 0700); unlink stale path before bind.
   - SIGTERM/SIGHUP/SIGINT handler: graceful shutdown + unlink the sock.
   - `host:port` form unchanged — keeps using `http.ListenAndServe`.

2. **`GET /sessions/{prefix}/resolve`** in `cmd/hoot/serve_bridge.go`:
   ```
   GET /sessions/{prefix}/resolve   → 200 {"key":"abc123"}
                                       404 no match
                                       409 ambiguous (body lists candidates)
   ```
   ~15 lines: `store.Resolve(prefix)` + the same error mapping `handleAttachRaw` already does.

### Removing `--host`

Nothing's been released. Just delete the `--host` flag, the `parseHostFlag` helper, and the `remoteDialer` plumbing in one commit; replace with `--remote` parsing via `parseRemoteFlag`. Update README + any internal callers (devport, etc.) in the same commit.

### Heartbeat: wire-level ping/pong

Sub-1s dead-connection detection, transport-agnostic. New attachwire frames:

```
MsgPing = 0x06   (C → S, empty payload)
MsgPong = 0x07   (S → C, empty payload)
```

Client side, inside `runSession` (`attach.go`):

- **Ping ticker**: fires every 200ms. Sends `MsgPing` only if `time.Since(lastSent) > 200ms`. Any outbound frame (Input, Size) already resets `lastSent`, so during chatty sessions the ping rate drops to zero. Idle case: clean 5 pings/sec.
- **Watchdog**: tracks `lastRecv` (updated on *any* inbound frame — Output, Pong, Size, Snapshot, Ping). If `time.Since(lastRecv) > 800ms`, close the conn. `runConnectLoop` sees the EOF, transitions to backoff+redial. The existing reconnect machinery handles everything from there.
- Detection budget: ≤800ms to drop a dead conn + 1s first backoff = ≤1.8s to first reconnect attempt. Full-replay repaint usually lands within ~2s of the network actually coming back.

Server side, in the per-session attach handler (hootty library, not in `serve_bridge`):

- On `MsgPing`: write `MsgPong`. No state, no timer.
- The `attach-raw` bridge stays a pure `io.Copy` relay — pings pass through to rpc.sock and get answered there. No bridge change needed.

Compatibility: client and server live in the same repo and ship together. New clients ↔ old servers: server errors on the unknown 0x06 frame → clean break we accept on the same release. Old clients ↔ new servers: no Pings sent, no Pongs returned — server's switch case is dormant.

ssh-layer keepalive (`ServerAliveInterval=15 / ServerAliveCountMax=2`) stays in the ssh argv as belt-and-suspenders for "host fell off the network" cases that even a 200ms ping can't catch faster than ssh's own TCP detection. Wire-level ping is the primary mechanism.

### Reconnect

Unchanged from `558d3a7`. `runConnectLoop` calls `dial(ctx)` per attempt; for ssh, `dial` lazily reopens the tunnel if it died (sshRemote's `Dial` rebuilds the `Tunnel` if `tunnel == nil` — and on a hard ssh-process death we set `tunnel = nil` in the dial-failure path so the next attempt rebuilds). Termios stays raw, infinite retry, keystroke wake — all reused.

Failure modes:

| Failure | Behavior |
|---|---|
| ssh process dies (`kill`, network drop → `ServerAliveCountMax` exceeded) | Next `Dial` rebuilds the tunnel; reconnect loop's backoff covers any churn. |
| Remote `hoot serve` crashes mid-attach | Conn EOF; reconnect loop redials; `Dial` finds the tunnel still up but the inner conn refused → tear down tunnel, rebuild, redial. |
| Session deleted on remote (404 from `/attach-raw`) | `*httpErrExit2` → exit 2 (existing semantics). |
| Auth failure | `Tunnel.Open` returns the captured ssh stderr; first-connect: exit 1. Mid-stream: backoff & retry forever (existing). |

Detection comes from the wire-level ping/pong above (sub-1s budget). ssh-layer keepalive is just a backstop.

### Local sock layout

```
<state-dir>/                       ($HOME/.hoot by default)
  <key>/                           local sessions (existing)
    rpc.sock
    pty.cast
    ...
  .tunnels/                        (new, mode 0700)
    cm-<sha8(user@host:port)>      ssh ControlMaster socket
    fwd-<host>-<pid>-<uuid>.sock   per-invocation forwarded unix sock
```

Cleanup:

- `fwd-…sock`: `Tunnel.Close` removes it.
- `cm-…`: ssh manages it; `ControlPersist=60s` reaps. We never touch it.
- Stale `fwd-…sock` from a crashed prior run: `Tunnel.Open` does `os.Remove(lsock)` before passing to `ssh -L` so `ExitOnForwardFailure=yes` doesn't trip.

The hashed ControlPath bounds the path to fit `sun_path` (104 bytes on macOS, 108 on Linux). `dialUnixSock` already has a chdir trick for long socket paths (`attach.go:831`), but ssh's ControlPath isn't dialed through Go — the ssh binary opens it directly — so we need it short by construction.

### OpenSSH process management

ssh is spawned via `exec.CommandContext`. `cmd.SysProcAttr.Setpgid = true` so we can SIGTERM the pgrp leader cleanly. Stdout/stderr captured to internal buffers — surfaced if `Tunnel.Open` fails; otherwise discarded. Stdin is wired to `/dev/null` (we never send anything to ssh).

The remote `hoot serve` is the ssh-exec'd process; when ssh disconnects (local SIGTERM → ssh → remote sshd → SIGHUP child), `hoot serve` exits and unlinks `rsock`. We rely on this — there's no separate teardown signal.

## Steps

1. **Wire ping/pong**: add `MsgPing`/`MsgPong` to `internal/attachwire/wire.go`; add server-side handler (one switch case in the per-session attach handler); add client-side ticker + watchdog in `runSession`. Test: kill a session mid-attach, observe drop in <1s.
2. **Server: `hoot serve --bind unix:<path>`** — parsing, listener, SIGHUP cleanup. Extend `serve_bridge_test.go` with a unix-bind round-trip.
3. **Server: `GET /sessions/{prefix}/resolve`** — handler + test.
4. **Client: `cmd/hoot/remote.go`** — `parseRemoteFlag`, `Remote`, `httpRemote`, `httpClient`, `dialAttachRaw`. Table-driven parser tests; http transport unit-tested against a `httptest.Server`.
5. **`internal/sshtransport/`** — `Tunnel{Open, Close}`, ControlPath helpers, argv builder. Unit tests for ControlPath length budget and argv shape.
6. **`sshRemote`** wired up in `remote.go`; integration test against `localhost` ssh (skipped if `ssh localhost true` doesn't work).
7. **Wire `--remote` through `cmdList`/`cmdResolve`/`cmdRun`/`cmdAttach`** — small per-file changes, all hitting `httpClient` or `dialAttachRaw`.
8. **Remove `--host`** from `attach`: delete the flag, `parseHostFlag`, and the `remoteDialer` helper; replace with `--remote` via `parseRemoteFlag`.
9. **README + main.go usage**: document `--remote` everywhere; ssh examples; ControlMaster note.
10. **Smoke harness + manual smoke against `devbox`** (see Verification).

## Verification

### Unit / table tests

- `parseRemoteFlag` table: every accepted form + a handful of error forms.
- `sshtransport.ControlPath` length budget on all platforms.
- `serve_bridge_test.go`: extend with `--bind unix:` round-trip; add `/sessions/{prefix}/resolve` cases (200/404/409).

### Smoke harness

A scripted smoke test in `tmp/<HHMMSS>_<ms>-remote-smoke.sh`:

- **http parity**: every subcommand against `--remote http://localhost:<port>` and (where applicable) the local path. Diff the outputs.
- **ssh path against `localhost`**: try `ssh -o BatchMode=yes localhost true`; if that works, run the full subcommand suite against `--remote ssh://localhost`. If not, log `#friction` and skip — fall back to manual against `devbox`.
- **ssh attach reconnect**: spawn a session via `hoot run --remote ssh://localhost -- bash -c 'echo hello; exec sleep 600'`, attach, kill the ssh client process, observe disconnect status line + reconnect, exit cleanly with `<prefix>.`.

### Manual smoke against `devbox`

The boss-doc names `devbox` as the real target.

- `hoot list --remote ssh://devbox`
- `hoot run --remote ssh://devbox -- htop` (reads back a key)
- `hoot attach --remote ssh://devbox <key>`, run vim, resize the local term, `<prefix>.` detach.
- Drop the laptop's wifi for 10s, observe disconnect+reconnect status line, full-replay repaint.

Capture transcripts to `tmp/<HHMMSS>_<ms>-remote-ssh-devbox.txt` and `…-remote-ssh-resume.txt`.

## Open questions

- **`hoot serve` SIGHUP handling on `--bind unix:`**: today serve's lifecycle is "ListenAndServe forever; let `kill` end it". When bound to a unix sock, we want SIGHUP/SIGTERM/SIGINT → unlink + exit. Adding the signal handler is fine but should we also extend it to the TCP-bind case for symmetry, or scope tightly? **Recommendation**: scope tightly — SIGHUP cleanup only matters because the unix sock is a filesystem artifact.
- **ssh latency for one-shot `list`/`resolve`**: ~100–300ms first invocation, ~50ms with ControlMaster. Acceptable for v1?
- **`--prefix` (path prefix on `serve`)** for the http transport: today's `--host` doesn't support it; this spec doesn't either. Defer until someone asks.
- **`hoot run --remote ssh://…` with no `--key`**: remote generates the key, local prints it. The user sees a key only meaningful with `--remote ssh://devbox`. Print as-is plus a stderr `hoot: session "abc" started on devbox`?

## Design notes

- 2026-05-05T16:30Z — **Pivot to "every Remote is just a `Dial`"**. Earlier draft had a `Remote` interface with `Dial`, `ExecOneShot`, `StreamOneShot` — three methods, two of which existed only to support an "ssh exec one-shot" fast path for cheap commands like `hoot list --remote ssh://devbox`. Killed in favor of: one `Dial`, every subcommand goes through `http.Client`. Reasoning, all human-driven feedback:
  - Adding a new transport (e.g. WebSocket-via-Cloudflared, in-process for tests) is now "implement `Dial`"; nothing else to think about.
  - Subcommand surface area collapses: `list`/`resolve`/`run` are ~10 lines each that build an HTTP request and decode the response. Same code on local-vs-remote diverges only at "do we hit `store.X` or HTTP".
  - Cost is on `ssh://` one-shots: every `hoot list --remote ssh://devbox` now spawns a `hoot serve` on the remote instead of just `ssh devbox hoot list`. Roughly 100–300ms first invocation, 50ms with ControlMaster reuse. Acceptable for v1 prototype; revisit if we feel it.
  - The "exec ssh + parse stdout" fast path is captured below as the rejected alternative we know how to add later — same `Remote` type, additional implementation. No re-architecture needed.

- 2026-05-05T16:32Z — **Why not the ssh-exec fast path** (rejected, kept here for future-us):
  - Add a third `Remote` shape: `sshExecRemote` whose `Dial` doesn't tunnel, instead it makes each subcommand exec `ssh host hoot <subcmd> <args>` and translates stdout/stdin/stderr back into something HTTP-shaped.
  - Two ways to do that: (a) bypass HTTP entirely for ssh-exec — adds an `ExecOneShot`-style method back to `Remote`, breaks the "one Dial fits all" model. (b) Have `hoot serve-stdio` mode that handles ONE HTTP request from stdin/stdout and exits, then `Dial` becomes "exec ssh, hand back its stdio as a `net.Conn`". (b) keeps the abstraction intact but adds a new server mode.
  - For attach, neither helps — attach is a long-lived connection, the tunnel cost amortizes immediately. Only `list`/`resolve`/`run` would benefit.
  - Verdict: not worth it for the prototype. Revisit when someone profiles `hoot list --remote ssh://…` and finds the latency offensive.

- 2026-05-05T16:34Z — **Auto-exit-on-idle for `hoot serve --bind unix:`**: rejected for v1.
  - The hack would be: `hoot serve --exit-on-idle 5s` so the ephemeral remote serve self-terminates if the tunnel goes silent.
  - We don't need it: ssh's disconnect already SIGHUPs the remote serve via the parent ssh-channel-close; serve's existing default-handler exits.
  - Add `--exit-on-idle` only if we observe lingering remote serves in practice (e.g. on a remote sshd config that doesn't propagate disconnect).

- 2026-05-05T16:36Z — **OpenSSH binary over `golang.org/x/crypto/ssh`**. Picked OpenSSH.
  - OpenSSH wins on:
    - `~/.ssh/config` inheritance: `Host`, `ProxyJump`, `IdentityFile`, `User`, `Port`, `HostName`, agent forwarding. Reproducing in `x/crypto/ssh` means writing a config-file parser, agent client, known_hosts checker, ProxyJump driver — months for parity the user already has.
    - ControlMaster gives free auth-once-multiplex-many across CLI invocations. `x/crypto/ssh` can multiplex within one process but doesn't share state across `hoot ...` calls.
    - Hardware-key, ssh-agent, ssh-askpass — transparent through the binary; bespoke through the library.
    - `ssh -L lsock:rsock` streamlocal forwarding is one flag.
  - Against (and why they don't bite):
    - "External dep": ssh is on every macOS, Linux, BSD by default; Windows ships OpenSSH client. Anyone using `--remote ssh://…` already has ssh.
    - "Process management is annoying": ~50 lines of Go vs ~5,000 reimplementing ssh client semantics.
    - "No structured errors": stderr is human-formatted; we surface verbatim.

- 2026-05-05T16:38Z — **Streamlocal (`unix:rsock`) over TCP `127.0.0.1:rport`** for the remote bind.
  - Alternatives:
    - **TCP loopback on remote**: parse port from `serve`'s startup log (brittle) OR pre-pick rport (collision risk if two attaches collide).
    - **Streamlocal unix sock** (picked): UUID path, no port, no parsing, no collision.
  - Needs OpenSSH ≥ 6.7 on both ends. macOS has shipped this since 10.11 (2015); any non-museum Linux has it. Acceptable floor.

- 2026-05-05T16:40Z — **Per-invocation tunnel rather than long-lived per-host serve**.
  - Long-lived: a `hoot serve` would persist on the remote for the laptop's session, reused across `hoot --remote ssh://devbox` invocations. Cheaper repeat latency.
  - Per-invocation: each `hoot --remote ssh://…` spawns its own serve, which dies when ssh exits.
  - Picked per-invocation: zero local daemon state to manage, no "did I leave a serve running on devbox" anxiety, ControlMaster amortizes most of the ssh cost anyway. Long-lived shape becomes a different `Remote` later if we want it.

- 2026-05-05T16:42Z — **`--bind unix:<path>` extension to `hoot serve`** rather than a new `hoot serve-tunnel` subcommand. Pure listener-flavor change; existing handlers and routing unchanged. A separate subcommand would duplicate everything.

- 2026-05-05T16:44Z — **ControlMaster with hashed ControlPath** (`<state-dir>/.tunnels/cm-<sha8(user@host:port)>`).
  - Hashing into 8 chars bounds the path. `sun_path` is 104 bytes on macOS; we already have a chdir trick (`attach.go:831`) for Go-side dials, but ssh consumes ControlPath directly — we can't chdir for it. So short by construction.
  - Why not `~/.ssh/cm-…`: that's the user's ssh dir; polluting it confuses other ssh tooling.

- 2026-05-05T16:46Z — **Reconnect reuses the http transport's `runConnectLoop` verbatim**. Only the dialer differs. `dial(ctx)` already supports arbitrary delay (it's a `func(ctx) (net.Conn, error)`), so a multi-second tunnel rebuild is fine.

- 2026-05-05T16:48Z — **Wire-level ping/pong at 200ms / 800ms watchdog**, not ssh-layer keepalive. Reversed from an earlier draft of this spec that proposed 10s ssh-alive.
  - Human feedback: detection should be sub-1s. ssh keepalive is too coarse and feels "strangely unresponsive" before reconnect kicks in.
  - Why wire-level over ssh-level:
    - Works on every transport uniformly: local rpc.sock, http+TCP, ssh-tunneled. ssh-only keepalive doesn't help the http path.
    - Catches L7 deadlock (remote `hoot serve` wedged but TCP/ssh socket fine) that L4 keepalive misses.
    - One implementation in `runSession` covers everything.
  - Cadence trade-off: 200ms/800ms picked.
    - 200ms ping interval, but adaptive: only sends if `time.Since(lastSent) > 200ms`. Any outbound Input/Size already proves the conn alive locally and resets the timer. Idle = 5 pings/sec; chatty = ~zero pings.
    - 800ms watchdog (4× ping interval) — one dropped packet doesn't false-positive.
    - Detection budget: ≤800ms drop + 1s first backoff = ≤1.8s to first reconnect attempt.
    - 1s heartbeat (per the section text) was on the table — picked 200ms instead because pings are cheap and the user explicitly asked for fast detection. The "spam wire on flaky link" concern from an earlier draft doesn't bite at this rate (5 frames/sec/client = rounding error).
  - ssh keepalive stays as belt-and-suspenders at relaxed `15` / `2` strikes — only relevant for "host vanished from network entirely" cases.
  - Compat: client and server ship together in this repo; one wire-protocol bump, no version negotiation. Captured in spec's Compatibility note.

- 2026-05-05T16:50Z — **`--host` removed outright, no deprecation alias**. Nothing released yet, no external scripts depending on it, only in-tree caller is devport. Reversed from an earlier draft of this spec that proposed a one-release nudge — the human flagged that the compat surface was unnecessary cost for zero benefit.

- 2026-05-05T15:37Z — **Implementation detail: SSH tunnel resolves remote `$HOME` before forwarding**. The spec described the remote socket as `~/.hoot/.tunnels/<uuid>.sock`, but OpenSSH streamlocal forwarding receives the remote socket path as data, not as a shell-expanded command word. In practice `ssh -L <lsock>:~/.hoot/.tunnels/x.sock ...` cannot rely on `~` expansion for the forwarded remote socket.
  - Picked: `internal/sshtransport.Open` first runs a short `ssh <target> sh -lc 'printf %s "$HOME"'` using the same ControlMaster/ControlPath options, then builds an absolute remote socket path `<home>/.hoot/.tunnels/<uuid>.sock`.
  - The subsequent tunnel command executes `hoot serve --bind 'unix:<absolute-rsock>'`; `hoot serve --bind unix:<path>` creates the parent directory on the remote.
  - Alternatives considered:
    - Use literal `~/.hoot/...` in `-L`: simpler, but depends on undocumented/non-shell expansion behavior in streamlocal forwarding.
    - Put remote sockets under `/tmp`: avoids the home probe, but violates the approved state-dir story and leaves a more globally visible namespace.
    - Start a remote shell that prints the chosen socket path and then serves: possible, but it makes readiness parsing depend on stdout from a long-lived process. The explicit home probe is easier to test and keeps the long-lived tunnel command quiet.

- 2026-05-05T15:47Z — **SSH readiness must be HTTP readiness, not socket readiness**. The first real `ssh://devbox` smoke failed on `hoot list --remote ssh://devbox` with `Get "http://hoot/sessions": EOF`.
  - What happened: OpenSSH's local stream socket can accept a client before the remote Unix socket is actually accepting `hoot serve` HTTP. The original `Tunnel.Open` probe only did a bare Unix dial to the local forwarded socket, so it returned "ready" too early. The first real HTTP request then hit a forwarded stream whose remote open failed and closed with EOF.
  - Picked: `internal/sshtransport.waitHTTPReady` now sends `GET /healthz` through the forwarded Unix socket and only returns when it receives HTTP 200. This is slightly more transport-specific, but the tunnel is deliberately a `hoot serve` tunnel, not a generic SSH forwarding library.
  - Alternative considered: keep the bare socket probe and make the higher-level HTTP client retry EOF once. That would hide this one race but leave every first request responsible for readiness. Centralizing the probe in tunnel open gives all subcommands the same behavior.

- 2026-05-05T15:52Z — **Remote `hoot serve` needs an explicit shell trap for cleanup**. The first failed `ssh://devbox` smoke left a process like `hoot serve --bind unix:/home/me/.hoot/.tunnels/<id>.sock` running on `devbox`, even after the local ssh process was gone.
  - What changed: instead of `exec hoot serve --bind ...` as the remote SSH command, `internal/sshtransport.TunnelArgs` now runs `sh -lc '<serve> & pid=$!; trap ... HUP INT TERM EXIT; wait "$pid"'`.
  - Why: in this Tailscale/OpenSSH environment, relying on the remote `hoot serve` process itself to receive SIGHUP was not reliable. Keeping a parent shell with an EXIT/HUP/TERM trap gives us a cleanup owner that kills the child when the SSH session ends.
  - Alternative considered: add an idle timeout to `hoot serve --bind unix:`. That was rejected earlier as unnecessary, and this failure mode still does not require it; a trap-wrapped parent solves the actual lifecycle problem without adding another serve flag.

- 2026-05-05T16:01Z — **SSH tunnel remote side switched from streamlocal Unix socket to loopback TCP on `devbox`**. After the HTTP-readiness fix, `ssh://devbox` still failed: the remote `hoot serve --bind unix:/home/me/.hoot/.tunnels/<id>.sock` was healthy when curled on `devbox`, but the local OpenSSH streamlocal forward never delivered HTTP through it.
  - Picked: keep the local forwarded endpoint as a Unix socket under `<state-dir>/.tunnels`, but forward it to a random remote `127.0.0.1:<port>` and start remote `hoot serve --bind 127.0.0.1:<port>`.
  - Why this is acceptable:
    - The remote TCP listener is loopback-only, not externally exposed.
    - The local client abstraction is unchanged (`Remote.Dial` still returns a Unix-socket-backed HTTP stream).
    - `hoot serve --bind unix:<path>` remains implemented and tested; it just is not the default ssh tunnel backend in this environment.
  - Tradeoff: a random remote port can theoretically collide. The port is derived from the per-invocation random id in the 20000-49999 range. If it collides, readiness fails quickly and the command exits with a useful tunnel error. A future retry loop could remove even that low-probability failure.
  - Added cleanup belt-and-suspenders: the remote wrapper writes `<home>/.hoot/.tunnels/<id>.pid`, and `Tunnel.Close` runs a best-effort cleanup ssh command that kills that pid and removes the pidfile. This handles environments where the SSH channel does not propagate HUP/EXIT cleanly.
