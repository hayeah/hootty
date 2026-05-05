# Remote attach over HTTP via `hoot serve`

## Goal

Make `hoot attach` able to connect to a remote `hoot serve` host over HTTP/WebSocket, instead of only dialing a local `rpc.sock`. Target invocation:

```
hoot attach --host m4mini:20000 <short-id>
```

The remote path reuses the existing `attachwire` framed protocol, tunnelled inside a WebSocket exposed by `serve`. Local-socket attach (no `--host`) keeps working unchanged. Auth is intentionally out of scope — Tailscale ACLs gate the network.

In scope:

- New `--host` flag on `hoot attach`.
- New WS endpoint on `serve` that pass-throughs `attachwire` frames end-to-end (so the CLI can send its own `Hello` with real cols/rows + `replay_mode`).
- Short-id resolution against the remote server.
- Automatic reconnect with backoff and a transient status line.
- Reconnect interaction with the prefix-key state machine.
- Drive-by: collapse `hoot serve`'s `--addr` + `--port` into one `--bind` flag using Go's `net.Listen` `host:port` convention. Parallels the new `--host` on `attach` (one address-shaped flag per side).

Out of scope:

- TLS server cert pinning, auth tokens, anything that's not "trust the network".
- Any browser-side change. The existing `/sessions/{key}/attach` keeps its current shape (bridge synthesizes Hello at 80×24) for `hootty-webui`.
- Detach-on-disconnect mode (could be revisited; see Open questions).

## Architecture

### Files touched

- `cmd/hoot/attach.go` — extend with `--host` flag, remote dialer, reconnect loop. Local path stays the default.
- `cmd/hoot/serve.go` — register one new route; rename `--addr`+`--port` to `--bind <host:port>` (see "Serve `--bind`" below).
- `cmd/hoot/main.go` — usage block reflects `--bind`.
- `devport.toml` — update the `hoot serve` argv to `--bind 127.0.0.1:<port>`.
- `cmd/hoot/serve_bridge.go` — add `handleAttachRaw` (pure attachwire pass-through). The existing `handleAttach` is left untouched for browser callers.
- `cmd/hoot/serve_spawn.go` (or wherever short-id helpers live) — no change needed; client resolves via the existing `GET /sessions` endpoint.
- `README.md` — document `--host`.

### New server route

```
GET /sessions/{key}/attach-raw   →  HTTP/1.1 Upgrade: hoot-attach/1
                                    (hijacked TCP relay to rpc.sock /attach)
```

Wire shape:

- **HTTP/1.1 Upgrade**, not WebSocket. Same `hoot-attach/1` token the local `rpc.sock` `/attach` already uses. After the `101 Switching Protocols` exchange, the connection is raw bytes carrying attachwire frames in both directions. No second framing layer.
- The bridge implementation is a pure relay: resolve `{key}` → dial `rpc.sock` → perform the Upgrade dance against `rpc.sock` (existing `dialAttach`) → reply to the client with our own `101 Switching Protocols / Upgrade: hoot-attach/1` → `io.Copy` both directions until either side closes. The bridge does **not** parse attachwire; it just shovels bytes.
- `{key}` is resolved via `store.Resolve` (full key OR unique prefix) **before** hijack, so `404` no-match / `409` ambiguous come back as ordinary HTTP responses (the client hasn't upgraded yet).
- TLS: if the client connects via `https://`, the TLS terminator is the usual `http.Server` with `ListenAndServeTLS`. Hijack works through TLS — `Hijack()` returns the TLS `*tls.Conn`, `io.Copy` does the right thing.

Why HTTP Upgrade instead of WebSocket:

- **CLI-side simplicity**: the existing `runAttach` function already does the HTTP Upgrade dance over a `net.Conn` (against `rpc.sock`). Swapping the dialer from `net.Dial("unix", sock)` to `net.Dial("tcp", host)` (or `tls.Dial`) is a one-line change. The `runStdinLoop` / `runServerLoop` goroutines see a `net.Conn`; they don't care which transport it is.
- **No extra dep on the client**: no `coder/websocket` import in `cmd/hoot/attach.go`.
- **No double-framing**: WebSocket would wrap attachwire frames inside binary WS messages — two length headers per write. Hijack is end-to-end attachwire, byte-identical to the local path.
- **Bridge becomes trivial**: ~30 lines, no goroutines parsing frames in either direction, just `io.Copy` × 2.
- **Tailscale + direct `serve` is the only deployment path on the table.** Tailscale is L3; it transports any TCP traffic. There's no L7 proxy in the loop that would force WebSocket. If we ever sit `attach-raw` behind a Cloudflare-style HTTP/2-only L7 that mandates `Upgrade: websocket`, we add a *second* parallel route then. Not now.

Why a separate route from the existing `/attach`:

- The existing `/attach` is a WebSocket bridge tailored for `hootty-webui` (xterm.js sends JSON `{type:"resize"}`, bridge synthesizes `Hello{80x24}`). Conflating "browser WS bridge" and "CLI Upgrade pass-through" on one URL would mean branching on the request's `Upgrade` header inside the handler — possible but uglier than two routes that each do one thing.
- Naming: `attach-raw` signals "raw attachwire pass-through" (no transcoding) without baking in *who* the caller is. A future Go library, a CI smoke client, or a hand-rolled `nc`-driven test all use this route.

### Short-id resolution

The server already owns `session.Store.Resolve` (handles exact-match + unique-prefix + ambiguous + missing). The client passes its raw user-typed argument straight through as the `{key}` path segment, and the bridge resolves on the server side.

Wire shape on the new attach-raw route:

- `GET /sessions/{key}/attach-raw` where `{key}` may be a full key OR a unique prefix.
- Bridge first calls `store.Resolve({key})`. On success it dials the resolved session's `rpc.sock`. On failure it returns:
  - `404 Not Found` — no session matched.
  - `409 Conflict` — ambiguous prefix (body lists the matching keys).

The client maps these statuses to the same exit codes the local path uses today (no-match / ambiguous → exit 2). On reconnect the client *keeps the original prefix string* and re-resolves on each attempt — that means a session restart with the same key is followed transparently, and a session deletion surfaces as a clean 404 on next reconnect.

Why server-side resolve over client-side `GET /sessions` + filter:

- One round-trip instead of two on first connect.
- The resolution rules (exact > unique-prefix, with the existing edge cases) live in one place. No risk of the client and server drifting.
- Listing sessions just to attach to one of them leaks more state than needed when the host is shared. Resolve-by-prefix returns nothing about other sessions.

A separate `GET /sessions` is still useful for `hoot list --host …` later, but it's not on the path for `attach`.

### Connection URL

`--host m4mini:20000` is interpreted as follows:

1. If the value contains `://`, parse it as a URL and use the scheme (`http`/`https`).
2. Otherwise treat as bare `host[:port]`, default scheme `http`, default port `20000` (matches devportv3 convention; document explicitly).
3. WebSocket scheme is derived: `http` → `ws`, `https` → `wss`.
4. `--host` is the chosen flag name (see Design notes for the `--addr` rejection).

Final URL constructed (one, since there's no client-side `GET /sessions` step anymore):

```
GET <scheme>://<host>/sessions/<key>/attach-raw     # with Upgrade: hoot-attach/1
```

For `https://`, the client uses `tls.Dial`; otherwise plain `net.Dial("tcp", ...)`. Either way, after the `101` handshake the conn is handed to the existing attachwire pumps unchanged.

`--prefix` (path prefix on `serve`) is **not** plumbed in v1; if the user is fronting `serve` with a strip-prefix=false proxy, they pass `--host m4mini:20000` and aim at the bare-path mount that `serve` already registers. Adding a `--path-prefix` later is a one-line addition.

### Serve `--bind`

```
hoot serve [--state-dir <d>] --bind <host:port> [--prefix /api]
```

- Required (replaces today's required `--port` + optional `--addr`). No default — fail loudly if missing.
- Accepts the full Go `net.Listen` convention:
  - `127.0.0.1:20000` — loopback only (the common case; what devport uses).
  - `:20000` — all interfaces (handy on a Tailscale host).
  - `[::1]:20000` — IPv6 loopback.
  - `m4mini.tail-scale-name:20000` — a specific hostname/IP.
- Implementation is one line: pass the value straight to `http.ListenAndServe(*bind, mux)`.
- Migration: this is a breaking flag rename. `devport.toml` is updated in the same change. The previous flags don't get a deprecation alias — `serve` only has one in-tree caller, and the error from `flag.Parse` ("flag provided but not defined: -addr") is self-explanatory.

### Reconnect lifecycle

State machine (per-session, lives inside `runAttach`):

```
[connecting] ─success→ [connected] ─drop→ [reconnecting] ─success→ [connected]
     │                                          │
     │ first-connect-failed                     │ giveup (max attempts)
     ▼                                          ▼
   exit 1                                     exit 1
```

Behaviors per state:

- **connecting** (first attempt only). Failure → exit 1 with the dial error printed verbatim. No retry on first connect — if the user typo'd the host, fail loudly. (See Open questions: should this also retry?)
- **connected**. Run the existing stdin-pump / server-pump goroutines. Normal server EOF → exit 0 (clean detach). Any other read/write error → transition to **reconnecting**.
- **reconnecting**. Termios stays in raw mode (we never restore between drops — restoring + re-raw would flicker and might race with the user's keystrokes). Backoff schedule: `1s, 2s, 4s, 8s, 16s, 30s, 30s, …`, capped at 30s. **No total cap — retry forever.** The user owns the lifecycle; only `<prefix>d` / SIGINT / SIGTERM ends a hung attach. Rationale: cover overnight-laptop and other long-outage scenarios where the session outlives the network blip. On each retry tick, try the WS dial; on success, send a fresh Hello with current local cols/rows + the original `replay_mode`, transition to **connected**.
- **Keystroke wakes the backoff.** While in **reconnecting**, any byte read from stdin (other than a `<prefix>` sequence) cancels the current sleep and triggers an immediate redial attempt. Failed redial returns to the next backoff tier as if the timer had elapsed. This means "wiggle a key when you wake your laptop" reconnects instantly instead of waiting up to 30s for the next tick. The keystroke itself is **not** sent to the remote (consistent with the "drop input during reconnect" rule) — it's purely a wake signal.

Replay on reconnect:

- **Default (`--full-replay`, the existing default)**: every reconnect issues `Hello{replay_mode:"full"}`. The session streams the full pty.log again, repainting the screen — this is what makes the status-line hack work without a dedicated reserved row (see Status line below). This is the recommended mode for ssh-style sessions.
- **`--no-full-replay`**: every reconnect issues `Hello{replay_mode:"snapshot"}`. The session sends a libghostty snapshot then streams. Faster on long-running sessions with huge pty.logs, but the disconnect line stays visible until the snapshot paint overwrites that row (mostly fine — alt-screen apps repaint everything).

### Status line for connection state

Approach: **transient stderr lines + replay-driven cleanup**. No reserved row, no scroll-region, no libghostty cell stomp.

When transitioning to **reconnecting**, write to stderr:

```
\r\n\x1b[2m[hoot: disconnected, reconnecting in 2s — press any key to retry now]\x1b[0m\r\n
```

The countdown text gets refreshed on each backoff tier change (1s → 2s → 4s …) using `\r\x1b[K` to clear-and-rewrite the line in place, so the user sees the schedule advance without scrollback spam.

When transitioning back to **connected**, write nothing — the very next frame is a full-replay (or snapshot) which repaints the entire screen, naturally overwriting the disconnect line. (For alt-screen apps the line is already off-screen by the time the user looks.)

This is the cheapest thing that works. It piggybacks on the existing replay machinery instead of fighting the terminal.

Alternatives considered (with tradeoffs) — see Design notes for full reasoning.

### Prefix-key state machine interaction

The prefix-key FSM (`stateNormal` ↔ `stateAfterPref`) lives in the stdin pump. Reconnect implications:

- The stdin pump must keep running across reconnects so the user can `<prefix>d` to detach during a disconnect window. The pump is decoupled from the WS conn; it sends frames via a channel.
- The pump is aware of the current connection state. While **reconnecting**:
  - Bytes that are *not* part of a `<prefix>` sequence: not sent over the wire (no conn) **and** signal a `wakeReconnect` channel that the backoff sleeper selects on. Net effect: idle for hours costs nothing; user wiggles a key and the redial fires within a few ms.
  - `<prefix>d` still detaches cleanly (cancels the context, tears down the reconnect loop, exits 0).
  - `<prefix>?` still prints local help.
  - `<prefix><prefix>` (literal prefix) is dropped on the floor — no conn to send it on, no wake (it's not "the user is here, retry now", it's "send this byte"; user retries after reconnect).
- The `send` channel for the connected path stays buffered (256). When a drop fires, we drain the channel before transitioning to reconnecting, so old in-flight frames don't replay onto the freshly-repainted screen post-reconnect.
- SIGWINCH during reconnect: latch the latest size in the connection state and send it as part of the new Hello on reconnect (Hello's cols/rows reflect current terminal). No separate Size frame needed.

### Error surfaces

| Surface | Behavior | Exit code |
|---|---|---|
| `--host` parse error | Print + usage | 2 |
| WS dial fails on first connect | Print error, exit | 1 |
| Server returns 404 (no-match) on `attach-raw` | Print "no session matched" | 2 |
| Server returns 409 (ambiguous) on `attach-raw` | Print candidates from body | 2 |
| Server returns non-101 to WS upgrade (other) | Print status + body | 1 |
| Mid-stream drop | Enter reconnecting (forever) | (0 on user detach) |
| User `<prefix>d` during any state | Clean detach | 0 |
| `kill -INT` to client | Trap | 130 |

## Steps

0. Drive-by rename: `hoot serve --addr/--port` → `--bind <host:port>`. Update `cmd/hoot/serve.go`, `cmd/hoot/main.go` usage, `README.md`, `devport.toml`. One commit.
1. Add `--host` flag parsing + URL normalization in `cmd/hoot/attach.go`. Pure flag plumbing, no behavior change yet (still local).
2. Add `handleAttachRaw` in `cmd/hoot/serve_bridge.go` and register the route in `serve.go`. Implementation: resolve key → call existing `dialAttach(ctx, sockPath)` which gives us an upstream `net.Conn` already past the rpc.sock 101 → hijack the client conn, write `101 Switching Protocols / Upgrade: hoot-attach/1`, then `io.Copy` both directions in two goroutines and wait for either side to close. ~30 lines. No attachwire parsing in the bridge.
3. Add a remote-attach code path: factor the existing local `runAttach` so the dialer is a parameter (`func(ctx) (net.Conn, error)`). The local path passes a unix dialer; the remote path passes a TCP-or-TLS dialer that also writes the HTTP Upgrade request and reads the `101` (the request line is `GET /sessions/<short-id>/attach-raw`; on `404`/`409` the function returns a sentinel error mapped to exit 2). The `runStdinLoop` / `runServerLoop` goroutines are unchanged.
4. Wire `runAttach` to dispatch to local-or-remote based on `--host`. Same goroutines, different dialer.
5. Add the reconnect loop: wrap the inner attach loop. On non-clean error, sleep backoff (with keystroke-wake), redial, re-Hello, resume. Termios stays raw across drops; restore only on user detach / signal / `--no-reconnect` exit.
6. Status-line writes on disconnect/giveup. Bracket-stderr write helpers.
7. Manual smoke + scripted test:
   - Local: `hoot serve --port 20000` + `hoot attach --host 127.0.0.1:20000 <short-id>`. Run vim, resize terminal, kill `serve`, restart it, watch the reconnect.
   - Remote: same against `m4mini:20000` over Tailscale.
   - `--no-full-replay` reconnect path.
   - Drop network (`pfctl` block) for 10s, observe disconnect line + clean reconnect.
8. README update.

## Verification

- `go test ./...` — all green.
- New test: WS attach-raw round-trips a Hello + Input + Output frame between a real `serve` and a fake CLI. (Mirror the table-driven style of `pty_libghostty_test.go`.)
- Manual transcript captured into `tmp/<HHMMSS>_<ms>-attach-host-vim.txt`:
  1. spawn vim in a session via `hoot run`;
  2. attach via `--host`, edit a line, detach with `<prefix>d`;
  3. re-attach, kill `serve`, restart, observe reconnect;
  4. confirm exit 0.
- Same transcript against `--no-full-replay`.

## Open questions

- **First-connect retry?** Today's draft says "fail fast on first dial". Argument for retrying: laptops where the Tailscale interface comes up a few seconds after wake. Argument against: typo'd host should fail in a beat, not forever. **Recommendation: fail-fast in v1, add `--retry-initial` later if anyone asks.**
- **Detach-on-disconnect mode?** A `--no-reconnect` flag for users who want the old "lose connection → exit" semantics. Trivially adds; worth adding now? **Recommendation: yes, ship `--no-reconnect` in v1; one extra `if`.**
- ~~`--reconnect-timeout`~~ — resolved: retry forever, no cap. User detaches with `<prefix>d` or kills the process.

## Design notes

- 2026-05-02T11:10Z — `--host` over `--addr`. `--addr` reads as the listener side (`serve` already uses `--addr 127.0.0.1`); the client side is the *target*, which lines up with ssh's vocabulary (`ssh user@host`) and with browsers ("the host you're connecting to"). `--addr` would also be ambiguous if we ever add a separate `--bind` for the client. No alternatives beyond these two were considered seriously.
- 2026-05-02T11:11Z — URL handling: `host:port` bare form is the path of least resistance, but accepting a full URL too is one extra line and lets `https://` work for free. `wss://` is the obvious upgrade for cross-org Tailscale-less deployments later. Rejected: making the user always type `http://` (annoying) and inventing a `--scheme` flag (one variable spread across two flags).
- 2026-05-02T11:12Z — Status-line approach: transient stderr line + replay-driven repaint. Alternatives considered:
  - **Reserved bottom row via DECSTBM scroll region** — write `\x1b[1;<rows-1>r` at startup, claim the last row. Rejected: child shell apps (vim, htop) emit their own DECSTBM and stomp ours. We'd have to re-apply on every SIGWINCH and on every replay; messy and brittle.
  - **Cursor save / restore + clear-line on the last row** — `\x1b[s\x1b[<rows>;1H[hoot: …]\x1b[K\x1b[u`. Cleaner but still races with full-screen apps that own the cursor and assume their saved-cursor stack is private.
  - **libghostty cell stomp** — drive a libghostty parser locally, render to it, then composite our status row over the rendered frame. Architecturally most correct (the bridge already has libghostty for snapshots) but a huge surface for "show 1 line of text on disconnect". Save it for a future "remote attach with side panel".
  - **Transient stderr line + full-replay repaint** (picked) — leverages the fact that reconnect *already* repaints the screen. Cost: ~5 lines of code. Failure mode: the disconnect line briefly survives if the user is on `--no-snapshot --no-full-replay` (not a thing today). For alt-screen apps the line lives in the scrollback, invisible.
- 2026-05-02T11:14Z — Two routes (`/attach` browser-friendly, `/attach-raw` pass-through) over one stateful route. The browser bridge has to translate JSON `{type:"resize"}` → `MsgSize`; the CLI sends `MsgSize` already. Branching on "did the first frame look like a Hello?" makes the bridge a partial parser of its own payloads. A new route is one method on `serveState`, ~30 lines, and the wire contract for each route is one sentence. Not worth saving the URL.
- 2026-05-02T11:15Z — Drop input-frames during reconnect rather than buffer. Buffering means the user types `vi notes.txt<Enter>` during a 4-second outage and that sequence replays *after* full-replay repaints the original screen state — the buffered keystrokes hit the wrong UI. Dropping is honest: the user sees the disconnect line, knows their typing is lost, retries. A future `--input-buffer` flag could opt in to buffering for low-stakes shell sessions.
- 2026-05-02T11:40Z — Reconnect retries forever (no time cap), and any non-prefix keystroke during reconnecting wakes the backoff sleep for an immediate redial. Human-driven feedback. Reasons:
  - Primary use case is overnight-laptop / multi-hour outages where the session outlives the network blip. A 5-min cap defeats that.
  - The user already has a clean exit (`<prefix>d` or SIGINT/SIGTERM) — there's no scenario where they're "stuck attached forever" without an out.
  - Keystroke-wake makes the post-resume latency feel instant: instead of waiting up to 30s for the next backoff tick, wiggle a key and the redial fires immediately.
  - The wake byte is *not* forwarded to the remote — same "drop input during reconnect" rule. It's a control signal, not data.
  - Status-line text gains "press any key to retry now" so the affordance is discoverable.
  - Rejected: making this configurable (`--reconnect-timeout`). Adds a flag for one-bit-of-policy that the user can already enforce by killing the process.
- 2026-05-02T11:30Z — `hoot serve --bind <host:port>` replaces `--addr` + `--port`. Human-driven feedback. Reasons:
  - Matches Go's `net.Listen` convention — single string, accepts `:port`, `host:port`, `[v6]:port`. The two-flag form was an awkward intermediate that needed `fmt.Sprintf("%s:%d", addr, port)` to recombine.
  - Visual parallel to the new `--host` on `attach`: one address-shaped flag per side. Easy to remember which one names the listener vs. the target.
  - Single in-tree caller (`devport.toml`) so no compatibility shim. A future deprecation alias is cheap if needed.
  - Rejected alternatives: keep `--addr`+`--port` and only add `--host` to attach (asymmetric); add `--bind` *and* keep the old flags as deprecated aliases (extra surface for zero benefit at this scale).
- 2026-05-02T11:16Z — ~~Short-id resolved client-side via `GET /sessions` rather than adding `/sessions/{prefix}/resolve`.~~ Superseded by 2026-05-02T11:50Z below.
- 2026-05-02T12:05Z — `attach-raw` uses HTTP/1.1 Upgrade + connection hijack, not WebSocket. Human-driven feedback. Reasons:
  - The local `rpc.sock` `/attach` already speaks HTTP Upgrade with token `hoot-attach/1`; reusing it across HTTP makes the bridge a pure TCP relay (`io.Copy` × 2, ~30 LOC) instead of a frame-aware translator.
  - CLI client reuses the existing local Upgrade dance verbatim — only the dialer changes (`net.Dial("unix", sock)` → `net.Dial("tcp", host)` or `tls.Dial`). `runStdinLoop` / `runServerLoop` see a `net.Conn` and don't care.
  - No `coder/websocket` import on the CLI; no double-framing (WS binary message wrapping attachwire-framed bytes).
  - Browser path is unaffected — the existing `/attach` WS bridge stays for `hootty-webui`. `attach-raw` is the CLI/tooling route.
  - Rejected alternatives:
    - **WebSocket on `attach-raw`** (previous draft): more code on both sides for zero functional gain in the Tailscale + direct-`serve` deployment we actually target.
    - **Conflate `attach-raw` and `attach` on one URL**: would require sniffing the `Upgrade` header inside the handler. Possible, uglier than two single-purpose routes.
  - Caveat / future work: an HTTP/2-only L7 proxy (Cloudflare) that mandates `Upgrade: websocket` would block this. Not on the roadmap; we'd add a parallel WS-shaped `attach-raw-ws` route then.
  - TLS: `https://` host triggers `tls.Dial` on the client; `http.Server` `Hijack()` returns the underlying `*tls.Conn` on the bridge — `io.Copy` works through it.
- 2026-05-02T11:50Z — Short-id resolved **server-side**: the bridge calls `store.Resolve({key})` on the path param, treating it as either a full key or a unique prefix. Human-driven feedback. Reasons:
  - `Resolve` already exists and handles all the edge cases (exact, unique-prefix, ambiguous, missing). The earlier client-side scheme would have duplicated those rules in Go just to call them over an extra round-trip.
  - One round-trip instead of two on first connect.
  - Single source of truth for the resolution rules — they can change in one place (`session.Store.Resolve`) and both the local CLI and the remote CLI follow.
  - On reconnect, the client keeps the *prefix string* and re-resolves each attempt. A session restart with the same key is followed transparently; a session deletion surfaces as `404` and exits 2.
  - Information disclosure: resolve-by-prefix doesn't enumerate other sessions, unlike `GET /sessions`. Better default on a shared `serve` host.
  - Status mapping: bridge returns `404` for no-match, `409` for ambiguous (body lists candidates). Client maps both to its existing exit-2 path.
