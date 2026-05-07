---
status: done
section: Hoot — consolidate `serve.go` per-session routes into a reverse proxy
slug: hoot-consolidate-serve-go-per-session-routes-into-a-reverse-proxy
mode: worktree
spec: spec.md
created: 2026-05-07T08:11:13Z
---

> ## Hoot — consolidate `serve.go` per-session routes into a reverse proxy
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`. Direct implementation (boss todo).
>
> Spec: `~/Dropbox/notes/2026-05-06/hoot-serve-proxy-consolidation_claude.md` — read end-to-end before starting. It has the design, the survives/deletes lists, caveats, and the order-of-operations recipe.
>
> ### Summary (do not skip the spec — this is just orientation)
>
> Today every per-session verb in `cmd/hoot/serve.go` (`handleClone`, `handleSignal`, `handleInput`, `handleAttachments`, `handleAttachmentByID`, `handleEvents`, `handleAttachRaw`, `handleState`) is wired in two files: the session-side mux + the serve-side forwarder. Replace the forwarders with one `httputil.ReverseProxy` mounted at `/sessions/{key}/{path...}` that dials `<state-dir>/<key>/rpc.sock` and forwards.
>
> `httputil.ReverseProxy` since Go 1.20 transparently handles HTTP/1.1 Upgrade (so `attach-raw` collapses) and `FlushInterval: -1` makes SSE work (so `/events` collapses). Plain JSON verbs collapse trivially.
>
> ### What survives as bespoke
>
> - `handleSessions` (GET list / POST spawn — no rpc.sock for "all sessions")
> - `handleSessionRoot` (GET state / DELETE kill)
> - `handleResolve` (pure serve-side store lookup)
> - `handleAttach` (browser WS bridge — real protocol translation)
> - `handleHealth`
>
> ### What gets deleted
>
> - `handleSignal`, `handleClone`, `handleInput`, `handleAttachments`, `handleAttachmentByID`, `handleEvents`, `handleState`, `handleAttachRaw`
> - The unix-socket-`http.Client` plumbing in `serve_bridge.go` (SSE copy loop + hijack/relay path).
> - Keep `dialSock`, `writeResolveError`, `dialAttach`, the WS pump pair.
>
> ### Order of operations (from the spec)
>
> 1. Land `handleSessionProxy` + the catch-all route alongside existing per-verb handlers (no behavior change yet — specific routes win in `http.ServeMux`).
> 2. Add an integration test that exercises the proxy path explicitly (mount it at `/proxy/sessions/{key}/{path...}` in tests, hit `/proxy/sessions/abc/signal`).
> 3. Delete one forwarder at a time, smallest blast radius first (`handleSignal`). Run tests after each.
> 4. Delete `handleAttachRaw` — verify CLI `hoot attach --remote …` still works.
> 5. Delete `handleEvents` — verify the dashboard's SSE consumer still reconnects cleanly.
> 6. Final pass: prune now-dead helpers in `serve_bridge.go`.
>
> ### Caveats to verify (don't skip)
>
> - `go.mod` must be Go 1.22+ for `{path...}`. Confirm.
> - Path resolution: catch-all uses resolved (full) key for the socket dial, but `pr.Out.URL.Path` is rewritten from the request's `{path...}`. That is correct — only the dial cares about the resolved key; upstream path is verb-only.
> - Prefix-matching keys still work because resolution happens up front.
> - Error mapping: upstream dial failures surface as `ReverseProxy`'s default 502 (matches today).
> - Header hygiene: confirm `ReverseProxy` strips hop-by-hop headers correctly for SSE + Upgrade. Manual smoke.
> - Tests: `kill_test.go`, `serve_bridge_test.go`, `clone_test.go` register handlers individually against test muxes — they may need either to register `handleSessionProxy` with a stub upstream, or keep using direct handlers. The proxy path needs its own integration test that spins up a real session-side mux on a unix socket and exercises one verb end-to-end.
>
> ### Note on overlap
>
> `cmd/hoot/serve.go` has gained `input` and `attachments[/{id}]` since the note was written. Same shape — they fold into the proxy too. Verify by listing all per-`{key}/...` registrations in current `serve.go` and confirming each survives or is on the delete list.
>
> ### Surface
>
> - [ ] implement and verify
>   - land `handleSessionProxy` + catch-all per spec; existing handlers untouched in step 1
>   - integration test exercising the proxy path end-to-end
>   - delete forwarders one at a time per the spec's order, watching tests
>   - prune dead helpers in `serve_bridge.go`
>   - update README's `hoot serve` section if it referenced any of the deleted handlers
>   - evidence: `go test ./...` tail per phase, plus a remote-CLI smoke (`hoot kill --remote`, `hoot detach --remote`, `hoot attach --remote`, `hoot list --remote`) confirming nothing regressed

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### Phase 1 — land proxy alongside existing handlers

- [x] enumerate every `register(...)` in `serve.go` and tag each as survive / delete
- [x] add `handleSessionProxy` + catch-all registration; specific routes still win
- [x] `go build ./...` clean; `go test ./cmd/hoot/...` clean

### Phase 2 — proxy integration test

- [x] write `serve_proxy_test.go` standing up a real session-side mux on a unix socket
- [x] exercise JSON verbs, SSE, query preservation, prefix resolve, dial-fail 502, and HTTP/1.1 Upgrade through the proxy

### Phase 3 — delete forwarders one at a time

- [x] delete `handleSignal`; update `TestKillSignalReturnsBadRequestForUnknown` to register proxy (796ed9d)
- [x] delete `handleInput`; route writeRemote at /pty/input; update three TestServeHandleInput_* tests (326f244)
- [x] delete `handleAttachments` + `handleAttachmentByID` (f9c3909)
- [x] delete `handleState` (registration only; helper still used by handleSession) (31d0e4a)
- [x] delete `handleClone`; drop `TestHandleClone` (redundant with proxy + cloneSession + CLI tests) (f561aaa)
- [x] delete `handleAttachRaw`; reroute through `handleAttachRawProxy` with attach-raw → attach upstream rewrite (503965b)
- [x] delete `handleEvents`; SSE through proxy (e601b5d)

### Phase 4 — prune & polish

- [x] prune `serve_bridge.go` of dead helpers (`relay`, `closeWriteConn`, SSE client loop) — folded into 503965b + e601b5d
- [x] update `serve.go` route comment block (7a7f1f0)
- [x] README check + update if necessary (7a7f1f0)
- [x] final `go test ./...` tail in evidence
- [x] remote-CLI smoke (`hoot list/kill/detach/attach --remote`) in evidence

## Agent log

- 2026-05-07T08:30Z — phase 1 landed (261e31c). handleSessionProxy + catch-all live alongside existing forwarders; specific routes still win. `go test ./cmd/hoot/...` green.
- 2026-05-07T08:35Z — phase 2 landed (e8c80c7). serve_proxy_test.go covers JSON verbs, SSE flush, query preservation, prefix resolve, dial-fail 502, and HTTP/1.1 Upgrade end-to-end. All 7 new tests pass; full cmd/hoot suite green.
- 2026-05-07T09:10Z — phase 3 done. Forwarders deleted one at a time (signal → 796ed9d, input → 326f244, attachments → f9c3909, state → 31d0e4a, clone → f561aaa, attach-raw → 503965b, events → e601b5d). cmd/hoot tests green after each commit.
- 2026-05-07T09:30Z — phase 4 done (7a7f1f0). serve.go route comment + README updated. Final `go test -count=1 ./...` green; remote-CLI smoke against TCP-bound `hoot serve` confirms write/clone/detach/kill/events/attach-raw all flow through the catch-all proxy correctly (101 Upgrade comes back, SSE state event arrives, signals delivered, attachments closed). Setting status=done.

## Boss log

## Evidence

### Tests

`go test -count=1 ./...` (final, in `~/github.com/hayeah/hootty/.worktrees/hoot-consolidate-serve-go-per-session-routes-into-a-reverse-proxy`):

```
ok  	github.com/hayeah/hootty	6.997s
ok  	github.com/hayeah/hootty/cmd/hoot	5.947s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.536s
ok  	github.com/hayeah/hootty/internal/shortid	0.634s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.485s
```

New integration tests in `cmd/hoot/serve_proxy_test.go` (each one stands up a real session-side mux on a unix socket and exercises the catch-all):

- `TestSessionProxyJSONVerb` — POST /signal with body + Content-Type round-trip
- `TestSessionProxyPreservesQueryString` — `?paste=on` survives through `/pty/input`
- `TestSessionProxySSE` — three SSE events stream out chunk-by-chunk under FlushInterval=-1
- `TestSessionProxyResolvesPrefix` — ambiguous prefix → 409 with matches
- `TestSessionProxyUnknownKey` — unresolved → 404 (no upstream dial)
- `TestSessionProxyUpstreamDialFails` — state.json present, no rpc.sock → 502
- `TestSessionProxyAttachRawUpgrade` — HTTP/1.1 Upgrade with Hello + Input → echoed Output

`TestHandleAttachRawRoundTrip` / `…NotFound` / `…Ambiguous` were retained but now register `handleAttachRawProxy` instead of the deleted bespoke handler — same fixtures, same assertions, exercising the proxy.

### Remote-CLI smoke (TCP-bound `hoot serve`)

```
healthz: {"ok":true,"started_at":...,"state_dir":"/tmp/hoot-smoke.../state"}

hoot run --remote http://127.0.0.1:58999 --key smoketest -- sleep 30
  → "session 'smoketest' started"

hoot write --remote ... smoketest "hello\r"
  → exit=0  (proxy: POST /sessions/smoketest/pty/input)

curl -sN -H "Connection: Upgrade" -H "Upgrade: hoot-attach/1" \
     http://127.0.0.1:58999/sessions/smoketest/attach-raw
  → HTTP/1.1 101 Switching Protocols
    Connection: Upgrade
    Upgrade: hoot-attach/1
  (proxy: GET /sessions/smoketest/attach-raw → upstream /attach)

curl -sN http://127.0.0.1:58999/sessions/smoketest/events  (perl-alarm 1s)
  → event: state
    data: {"state":"running","cmd":"sleep 30","pid":...,"started_at":...}
  (proxy: GET /sessions/smoketest/events with FlushInterval=-1)

hoot clone --remote ... --strict --key cloned smoketest
  → cloned  (proxy: POST /sessions/smoketest/clone)

hoot detach --strict --remote ... smoketest
  → "detached all attachments from session 'smoketest'"
  (proxy: DELETE /sessions/smoketest/attachments)

hoot kill --remote ... -s TERM --strict smoketest
  hoot kill --remote ... -s TERM --strict cloned
  → exit=0 each  (proxy: POST /sessions/{key}/signal)

hoot list --remote ... (after kill)
  → smoketest exited
    cloned exited
  (bespoke handleSessions, unaffected by the consolidation)
```

`hoot attach --remote` interactively was not smoke-tested directly because `attach` requires a TTY; the TestSessionProxyAttachRawUpgrade integration test plus the curl 101 above cover the wire shape.

### Branch diff

```
README.md                     |  45 ++---
cmd/hoot/clone_test.go        |  41 -----
cmd/hoot/kill_test.go         |  10 +-
cmd/hoot/serve.go             |  52 +++---
cmd/hoot/serve_bridge.go      | 230 +++++++------------------
cmd/hoot/serve_bridge_test.go |   6 +-
cmd/hoot/serve_proxy_test.go  | 378 ++++++++++++++++++++++++++++++++++++++++++
cmd/hoot/serve_spawn.go       | 208 -----------------------
cmd/hoot/write.go             |   2 +-
cmd/hoot/write_test.go        |  24 +--
10 files changed, 515 insertions(+), 481 deletions(-)
```

Net: +34 LoC, but ~470 lines of bespoke forwarder code replaced with one ~70-line `handleSessionProxy` + ~20-line `handleAttachRawProxy`, 8 forwarders deleted, `/clone`, `/signal`, `/input`, `/attachments[/{id}]`, `/state`, `/events`, `/attach-raw` all flow through the catch-all.

## Trouble report

- `/input` → `/pty/input` rename surfaced as the only path mismatch between serve-side and session-side. Resolved by updating `writeRemote` to use the canonical session-library path; documented in `spec.md` Design notes (2026-05-07T08:50Z entry). Out-of-tree scripts hitting `/sessions/{key}/input` will start 404'ing — accepted, since this surface was undocumented.
- `TestHandleClone` deleted as redundant rather than rewritten. Coverage paths: `cloneSession` → `TestCloneSessionPreservesMetadataAndEnvOverride`; HTTP wire shape → `TestSessionProxyJSONVerb`; CLI roundtrip → `TestCmdCloneRemote`. The deleted test was the only one testing serve-side `handleClone` HTTP, which no longer exists.
- The `/sessions/{key}/state` route (disk-read alias) had no callers (CLI uses `/sessions/{key}` for the same data; webui never queried it). Routing it through the proxy now returns the live in-memory snapshot from the session writer instead of a disk read — slightly fresher, observably the same.
- `--remote unix:/path/to/sock` is not supported by the CLI's `--remote` parsing (only http/https/ssh), so smoke had to bind hoot serve on a TCP port. Not a regression — predates this section. Flagging as #friction for any future "expose unix socket scheme to --remote" follow-up.
