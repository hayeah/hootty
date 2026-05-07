# Consolidate `hoot serve` per-session routes into a reverse proxy

Master spec lives at `~/Dropbox/notes/2026-05-06/hoot-serve-proxy-consolidation_claude.md`.
This file is the workspace-local working copy: same goal, with the implementation
plan trimmed to fit the current code and the test rework called out.

## Goal

Replace the per-verb forwarders in `cmd/hoot/serve.go` (and its `serve_*.go`
neighbors) with one `httputil.ReverseProxy` mounted at
`/sessions/{key}/{path...}` that dials `<state-dir>/<key>/rpc.sock`. Adding a new
session verb in the future stays a single `super.Mux().HandleFunc(...)` call in
`service.go` (or wherever the session library mounts it) — no serve-side change.

Out of scope: anything that does real protocol translation (`handleAttach`'s
WS↔attachwire bridge), serve-only routes (`handleSessions`, `handleSessionRoot`,
`handleResolve`, `handleHealth`), or the spawn path (`createSession`).

## Architecture

Routes after consolidation (`cmd/hoot/serve.go`):

```
GET    /sessions                  handleSessions   (list)
POST   /sessions                  handleSessions   (spawn)
GET    /sessions/{key}            handleSession    (state)
DELETE /sessions/{key}            handleSession    (kill by pid)
GET    /sessions/{key}/resolve    handleResolve    (serve-only)
GET    /sessions/{key}/attach     handleAttach     (browser WS bridge)
*      /sessions/{key}/{path...}  handleSessionProxy  ← NEW catch-all
GET    /healthz                   handleHealth
```

`handleSessionProxy` resolves the key prefix once, then hands a
`httputil.ReverseProxy` over a unix-socket transport. `FlushInterval: -1` makes
SSE work; Go 1.20+ ReverseProxy handles HTTP/1.1 Upgrade, so attach-raw collapses
in too. Specific routes win in `http.ServeMux` so `attach`, `resolve`, and
`{key}` keep their bespoke handlers.

### What gets deleted from serve-side

- `handleSignal`, `handleClone`, `handleInput`, `handleAttachments`,
  `handleAttachmentByID`, `handleEvents`, `handleState`, `handleAttachRaw`.
- The `http.Client`-over-unix-socket plumbing: SSE copy loop in
  `serve_bridge.go`, hijack/relay path (`relay`, `closeWriteConn`).
- `handleClone` calls `cloneSession()` directly today (does not actually go
  through rpc.sock on the serve side). The session-side `RunCmdService.handleClone`
  in `service.go:71` already mounts `/clone` on `super.Mux()` and calls the same
  `cloneSession()` helper. So routing through the proxy hits an equivalent
  implementation — the only behavioral difference is that the work happens in the
  session process instead of the serve process.

### What survives

- `handleSessions`, `handleSession` (state + DELETE), `handleResolve`,
  `handleAttach`, `handleHealth`.
- `dialSock` (used by both proxy transport and `dialAttach`).
- `writeResolveError`, `dialAttach`, `pumpUpstreamToWS`/`pumpWSToUpstream`.

### Test rework

`kill_test.go`, `serve_bridge_test.go`, `clone_test.go` register handlers
directly on test muxes:

- `TestKillSignalReturnsBadRequestForUnknown` (kill_test.go:135) — unknown-key
  404 path. With proxy the resolve still happens before dial, so we can swap
  the registration to `handleSessionProxy` and the test still passes.
- `TestHandleAttachRawRoundTrip` (serve_bridge_test.go:57) — exercises the
  hijack/upgrade relay against a fake unix socket. After deletion, this test
  is exactly the integration test we want — keep it but register
  `handleSessionProxy` at `/sessions/{key}/{path...}` and aim it at
  `/sessions/{key}/attach` (the session-library route is `/attach`, not
  `/attach-raw`). Update the fake-session handler to accept `/attach`.
- `TestHandleAttachRawNotFound`, `TestHandleAttachRawAmbiguous` — same
  registration swap.
- `TestHandleClone` (clone_test.go:96) — currently registers serve-side
  `handleClone`. Easiest path: delete this test; coverage is already provided
  by `TestCloneSessionPreservesMetadataAndEnvOverride` (calls `cloneSession`
  directly) and `TestCmdCloneRemote` (CLI side, independent).

A new integration test (`serve_proxy_test.go`) spins up a real session-side
mux on a unix socket and exercises one verb (e.g. `/signal`) end-to-end.

## Steps

1. Pre-flight: confirm `go.mod` ≥ 1.22 (it's 1.26, fine), enumerate every
   `register(...)` line in `serve.go` and confirm the survives/deletes list.
2. Land `handleSessionProxy` + a catch-all registration alongside existing
   handlers (specific routes still win). Compile + `go test ./cmd/hoot/...`.
3. Add `serve_proxy_test.go` integration test: real session mux on a unix
   socket, one verb (signal) end-to-end.
4. Delete `handleSignal` (smallest blast radius). Update
   `TestKillSignalReturnsBadRequestForUnknown` to register
   `handleSessionProxy` instead. Run tests.
5. Delete `handleInput`, `handleAttachments`, `handleAttachmentByID`,
   `handleClone`, `handleState`. Update / delete tests as listed above. Run
   tests after each.
6. Delete `handleAttachRaw`. Update the three attach-raw tests to register
   `handleSessionProxy` and aim at `/sessions/{key}/attach` (the session-side
   path is `/attach`, since the session library does not register
   `/attach-raw`; the serve-side surface is `attach-raw` but the proxy folds
   it into the canonical `/attach`).
7. Delete `handleEvents`. Smoke the SSE path manually if convenient.
8. Prune `serve_bridge.go`: drop the unused SSE client loop, `relay`,
   `closeWriteConn`. Keep `dialSock`, `writeResolveError`, `dialAttach`,
   `handleAttach`, the WS pumps, `handleResolve`.
9. Update `cmd/hoot/serve.go` route comment block to match the new surface.
10. README check: update if it documents any of the deleted routes.
11. Final `go test ./...` + remote-CLI smoke (`hoot list --remote`,
    `hoot kill --remote`, `hoot detach --remote`, `hoot attach --remote`).

## Verification

- `go test ./...` passes after each delete.
- New `serve_proxy_test.go` exercises the catch-all over a real unix socket.
- Remote CLI smoke against a freshly-built `hoot serve`:
  - `hoot list --remote unix:/tmp/serve.sock`
  - `hoot run --remote ... && hoot detach --remote && hoot attach --remote`
  - `hoot kill --remote ...`

## Wire-shape note

`/sessions/{key}/attach-raw` (serve-side) → `/attach` (session-side). After
consolidation, the canonical CLI path becomes `/sessions/{key}/attach` —
which is *also* the path `handleAttach` (browser WS) lives on. That's a
collision: a CLI client doing the HTTP/1.1 Upgrade and a browser doing the WS
handshake hit the same URL. The session library distinguishes them by the
`Upgrade:` header (`websocket` vs `hoot-attach/1`), so keeping
serve-side `handleAttach` on `/attach` and routing CLI traffic at the same
path *would* require the bespoke `handleAttach` to either forward non-WS
upgrades to the proxy or stay on a dedicated `attach-raw` URL.

The pragmatic choice: keep the serve-side surface as `/sessions/{key}/attach`
for the WS bridge (browser) and `/sessions/{key}/attach-raw` for the CLI.
The catch-all proxy handles `/attach-raw` by rewriting the upstream path to
`/attach` so the session library sees a plain HTTP/1.1 Upgrade. That keeps
existing CLI clients (`hoot attach --remote`) unchanged.

→ `pr.Out.URL.Path` rewrite: special-case `attach-raw` → `attach`. Or, simpler:
the serve-side mux registers a one-line `/sessions/{key}/attach-raw` handler
that does the rewrite itself before delegating to `handleSessionProxy`. Keep
this in the spec; decide which spelling at implementation time.

## Open questions

None blocking — see "Wire-shape note" above for the attach-raw rewrite, which
I'll resolve at implementation time and document in `## Design notes`.

## Design notes

- 2026-05-07T08:50Z — `/sessions/{key}/input` (serve-side shorthand) → `/sessions/{key}/pty/input` (canonical, after consolidation).
  - `handleInput` was the only forwarder that did a path-rename: serve `/input` → upstream `/pty/input`. Every other verb already had a 1:1 name match.
  - Alternatives considered:
    - **Add an alias rewrite to the proxy** (special-case `path == "input"` → `pty/input`): keeps existing `/sessions/{key}/input` URLs working. Lost because it bakes a serve-side alias into the catch-all permanently — exactly the kind of tax the consolidation removes.
    - **Add a tiny wrapper handler** (like `handleAttachRawProxy` for attach-raw): same downside — yet another bespoke route.
    - **Drop the alias entirely** (picked): only one caller (`writeRemote` in `cmd/hoot/write.go`), no webui caller, no public-API contract. Updating that one URL is trivial.
  - Trade-off: an out-of-tree script hitting `/sessions/{key}/input` against an updated `hoot serve` will start getting 404s from the session library (which has only `/pty/input`). Acceptable since this surface was undocumented.
