---
status: done
section: Remote attach over HTTP via hoot serve
slug: remote-attach-over-http-via-hoot-serve
mode: worktree
spec: spec.md
created: 2026-05-02T10:55:14Z
---

> ## Remote attach over HTTP via hoot serve
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> `hoot attach` today only talks to a local `rpc.sock`. We want remote attach over HTTP against a `hoot serve` host (typically a Tailscale hostname + port — no auth required since Tailscale handles network ACLs).
>
> Target shape:
>
> ```
> hoot attach --host m4mini:20000 <short-id>
> ```
>
> …connecting via the `serve`'s WS bridge instead of dialing rpc.sock directly. Local-socket attach should keep working unchanged.
>
> Open design questions for the spec to resolve:
>
> - **Flag name**: `--host` vs `--addr`? Pick one and justify briefly. Should bare `host:port` work, or do we want a full URL form (`http://m4mini:20000`) — and what about `https://`?
> - **Connection monitoring**: any way to hack a status line — commandeer the first or last line of the terminal — that flushes on reconnect to show connection state? Sketch the approach (libghostty cell stomp? raw escape? overlay region?) and the tradeoffs.
> - **Automatic reconnect**: assume yes. On reconnect:
>   - full-replay mode → restore termios, full replay again. Default.
>   - `--no-full-replay` → snapshot + stream as on first connect.
>   - Backoff strategy and visible feedback to the user during disconnect.
>
> The agent should:
> - write `spec.md` covering the above questions with concrete recommendations
> - include reconnect lifecycle (state machine: connected → disconnected → reconnecting → connected) and how it interacts with the prefix-key state machine
> - consider error surfaces (host unreachable on first connect vs mid-stream drop)
> - keep the surface minimal — reuse `attachwire` framing as-is over WS where possible
>
> - [ ] rfc: review spec.md
> - [ ] implement per spec

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### Phase 1 — spec
- [x] Read existing local attach + WS bridge to understand reuse surface
- [x] Draft `spec.md` covering flag, URL handling, route shape, reconnect lifecycle, status-line, prefix-key interaction, error surfaces
- [ ] **rfc gate** — wait for human to review spec.md and tick `rfc:`

### Phase 2 — implement (rfc green 2026-05-02T11:12Z)
- [x] Drive-by: `hoot serve` `--addr`+`--port` → `--bind <host:port>` (serve.go, main.go usage, README, devport.toml)
- [x] Factor `runAttach` so the dialer is a parameter `func(ctx) (net.Conn, error)`; existing local path passes a unix dialer
- [x] Add `--host` flag + URL normalization (`host:port` | `http://…` | `https://…`) in `cmd/hoot/attach.go`
- [x] Add `handleAttachRaw` in `cmd/hoot/serve_bridge.go` (resolve → dialAttach(rpc.sock) → hijack → io.Copy×2), register route in `serve.go`
- [x] Implement remote dialer: TCP / `tls.Dial`, write `GET /sessions/<id>/attach-raw` Upgrade, handle 404/409 → exit 2
- [x] Implement reconnect loop (connecting → connected → reconnecting → connected) with backoff `1,2,4,8,16,30,30…s` capped at 30s, **forever**, keystroke-wake
- [x] Status-line writes on disconnect (in-place rewrite per backoff tier)
- [x] Add `--no-reconnect` flag
- [x] attach-raw round-trip test (Upgrade hijack against an in-process serve) + 404/409 cases
- [x] Manual smoke: kill+restart serve mid-attach; observe reconnect (transcript in `tmp/`)
- [x] README updates: `--bind` on serve, `--host` and reconnect behavior on attach

## Agent log
- 2026-05-02T12:15Z — rfc gate ticked by boss. Aligned Phase 2 todos with the final spec shape (server-side resolve, HTTP Upgrade/hijack, retry-forever, keystroke-wake). status: working. Starting with Step 0: `--bind` rename on `hoot serve`.
- 2026-05-02T12:08Z — Human said "use HTTP Upgrade/hijack instead of WebSocket". Updated spec: `attach-raw` is HTTP/1.1 Upgrade with token `hoot-attach/1`, bridge is a pure `io.Copy` relay, CLI reuses the existing local Upgrade dance with a swapped dialer. No `coder/websocket` import on the CLI side. Updated route description, URL section, Steps, and design note. Browser path (`/attach` WS) unchanged. Still blocked on rfc.
- 2026-05-02T11:55Z — Human said "let the server resolve". Updated spec: bridge calls `store.Resolve({key})` on the path segment (full key or unique prefix); `404` no-match, `409` ambiguous. Dropped the client-side `GET /sessions` two-step. Updated route description, error-surfaces table, Steps, and a superseding design note. Reconnect now re-resolves each attempt — same-key session restarts are followed transparently. Still blocked on rfc.
- 2026-05-02T11:42Z — Human said "retry forever, and on keystroke immediately retry". Updated spec: dropped the 5-min cap, added keystroke-wake-the-backoff in the prefix-key state machine section, status line gains "press any key to retry now", error-surfaces table dropped the giveup row, design note added. Still blocked on rfc.
- 2026-05-02T11:32Z — Human asked to collapse `serve`'s `--addr`+`--port` into `--bind <host:port>` (Go `net.Listen` convention). Folded into spec as a Step 0 drive-by rename and added a "Serve `--bind`" subsection + design note. Still blocked on rfc.
- 2026-05-02T11:20Z — Read `cmd/hoot/attach.go`, `serve.go`, `serve_bridge.go`, `attach_handler.go`, `internal/attachwire/wire.go`. Drafted `spec.md` with concrete recommendations for flag (`--host`, accepts bare host:port and full URL), status line (transient stderr + replay repaint), reconnect lifecycle, and a new `/sessions/{key}/attach-raw` WS route for pure attachwire pass-through. Stopped at the rfc gate per AGENT_LOOP rules. status: blocked waiting on rfc.
- 2026-05-02T11:14Z step 0: --addr+--port → --bind <host:port> on hoot serve. devport.toml/README/main.go usage updated. 56d6f0d
- 2026-05-02T11:16Z step 2: handleAttachRaw + route registered. resolve→hijack→io.Copy×2 with half-close, 404/409 mapping. 2efd94c
- 2026-05-02T11:31Z phase 2 complete; status: done. commits: 56d6f0d (--bind), 2efd94c (handleAttachRaw), c855867 (--host + reconnect), bf2bbfe (tests), c8d590e (README), 0115313 (clean-exit fix). evidence in tmp/183035_438-attach-host-evidence.txt

## Boss log
- 2026-05-02T11:12Z ticked: rfc

## Evidence

Full transcript: `tmp/183035_438-attach-host-evidence.txt`. Highlights:

**Unit tests (all green):**

```
ok  	github.com/hayeah/hootty
ok  	github.com/hayeah/hootty/cmd/hoot   ← new TestHandleAttachRaw{RoundTrip,NotFound,Ambiguous}
ok  	github.com/hayeah/hootty/internal/shortid
```

**`hoot serve` rename (Step 0) — `--bind <host:port>`:**

```
$ hoot serve --bind 127.0.0.1:20100 --state-dir /tmp/...
hoot serve: listening on http://127.0.0.1:20100 (state-dir=/tmp/...)
```

**Local attach (regression, exit 0):** stdout = `hello-from-hoot line2`, detach via `<prefix>d`.

**Remote attach over `--host` (exit 0):**

```
$ hoot attach --host 127.0.0.1:20100 smoke
hello-from-hoot
line2
(detach via <prefix>d → exit 0)
```

**404 mapping (exit 2):**

```
$ hoot attach --host 127.0.0.1:20100 zzznotreal
hoot attach: no match for "zzznotreal"
exit=2
```

**Reconnect (kill + restart serve mid-attach, exit 0):**

stdout (replay appears TWICE — initial connect + post-reconnect full-replay):

```
hello-from-hoot
line2
hello-from-hoot
line2
```

stderr (disconnect line + in-place countdown rewrite via `\r\x1b[2K`):

```
[hoot: disconnected: EOF]
[hoot: reconnecting in 1s — press any key to retry now]
[hoot: reconnecting in 2s — press any key to retry now]
[hoot: reconnecting in 1s — press any key to retry now]
```

The countdown line is rewritten in place each tier (1s → 2s on backoff escalation; reset to 1s after the second serve came up and the session reconnected successfully — `shared.resetTier()` on connect).

## Trouble report

- **EOF ambiguity (resolved in code, called out in design notes)**: a clean child-exit at the hootty and a bridge-process death both surface as `io.EOF` on the client TCP conn. With reconnect on, EOF triggers a reconnect (which 404s if the session is genuinely gone → exit 2); with `--no-reconnect` it's treated as clean detach (matches local-socket "child exited" semantics). A wire-level `MsgClose` would let us disambiguate; out of scope here.
- **Worktree LSP false positives** in this session: gopls flagged "could not import" / "undefined: defaultStateDir" repeatedly because the worktree wasn't in any `go.work` file. `go build ./...` and `go test ./...` were always the source of truth and stayed clean.
- **One real bug found via smoke and fixed (`0115313`):** `<prefix>d` mid-backoff returned `ctx.Err()` up the call chain → "hoot attach: context canceled" exit 1. Mapped to clean exit 0 instead.
- **macOS `sun_path` 104-byte limit** bit the round-trip test (`t.TempDir()` paths exceed it). Fixed by adding a `/tmp`-rooted `shortTempDir` helper, mirroring the one in `hootty_test.go`.
