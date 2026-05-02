---
slug: migrate-supervisor-termui-webui-ts-libs-add-supervisor-serve-wire-devportv3
---

# Goal

Move the existing TypeScript dashboard out of `dotfiles/libs/hayeah-go/supervisor/{termui,webui}` into the `~/github.com/hayeah/supervisor` repo as a pnpm workspace, port the dotfiles `ptydemo serve` API as a new `supervise serve` subcommand, and wire the whole thing together with a `devport.toml` that fronts vite + the API on a single proxy. End state: `cd repos/.../supervisor && devport up` brings up a working dashboard at one URL with live supervisor sessions; `/browser screenshot` proves it.

Out of scope: any new termui/webui features, redesigning the wire protocol, changing the supervisor library itself.

# Architecture

## Repo layout (target supervisor repo, post-migration)

```
github.com/hayeah/supervisor/
  go.mod / go.sum / go.work          # existing
  *.go                                # supervisor library + attach handler (existing)
  cmd/supervise/
    main.go run.go list.go attach.go  # existing subcommands
    serve.go                          # NEW: cmdServe, serveState, register helper
    serve_bridge.go                   # NEW: handleAttach (WS↔attachwire bridge), handleEvents (SSE proxy)
    serve_spawn.go                    # NEW: createSession + spawnSupervise + splitCmd
  internal/attachwire/                # existing — used by the bridge
  package.json                        # NEW: workspace root
  pnpm-workspace.yaml                 # NEW
  packages/
    supervisor-termui/                # NEW: ported from dotfiles termui (renamed @hayeah/supervisor-termui)
      package.json src/ tsup.config.ts tsconfig.json README.md
    supervisor-webui/                 # NEW: ported from dotfiles webui (renamed @hayeah/supervisor-webui)
      package.json src/ vite.config.ts tsconfig*.json index.html public/
  devport.toml                        # NEW: vite + supervise serve composed under one proxy
```

Package names land as `@hayeah/supervisor-termui` and `@hayeah/supervisor-webui` per section text. (Source dirs are flat `termui/` / `webui/`; we put them under `packages/` since the repo will host two packages.)

## supervise serve

New subcommand `supervise serve [--state-dir <d>] [--addr 127.0.0.1] [--port N] [--prefix /api]`. Reads from the same state-dir as `supervise run/list/attach` — it's a fan-out HTTP server that lists sessions and bridges per-session WebSocket attaches into each `rpc.sock`'s `/attach` endpoint.

Routes (with `--prefix=/api` mounted both prefixed and bare):

| Method | Path                          | Behaviour                                                                                                    |
|--------|-------------------------------|--------------------------------------------------------------------------------------------------------------|
| GET    | `/sessions`                   | `{sessions:[{...StateFile, alive}]}`                                                                          |
| POST   | `/sessions`                   | `{cmd?, argv?, key?}` → spawn `supervise __supervise --state-dir <d> --key <k> -- <argv...>`, wait for sock  |
| GET    | `/sessions/{key}`             | state.json                                                                                                    |
| DELETE | `/sessions/{key}`             | SIGTERM `state.Supervisor.PID`, 204                                                                           |
| GET    | `/sessions/{key}/state`       | state.json                                                                                                    |
| GET    | `/sessions/{key}/events`      | SSE proxy of `rpc.sock /events`                                                                              |
| GET    | `/sessions/{key}/attach`      | WebSocket — bridge to `rpc.sock /attach` (HTTP/1.1 Upgrade → `attachwire` framed protocol)                   |
| GET    | `/healthz`                    | `{ok:true,...}`                                                                                              |

### WS↔attachwire bridge

Browser side (`@hayeah/supervisor-termui`'s `WebSocketAttach`):
- binary frame, both directions = PTY bytes
- text frame client→server = `{"type":"resize","cols":N,"rows":N}`

Backend side (target supervisor `attach_handler.go` over `rpc.sock`):
- HTTP/1.1 Upgrade `supervise-attach/1`, then `attachwire` frames: `Hello` (cols/rows/replayMode JSON) → `Output` (server→client PTY bytes) / `Size` (server→client effective size JSON) / `Input` (client→server raw bytes) / `Size` (client→server new declared size JSON)

Bridge logic:

1. WS `Accept` → dial `rpc.sock` (with the long-path-relative chdir fallback used by `supervise attach`).
2. Send HTTP Upgrade + `MsgHello{cols,rows, replayMode:"full"}` using `cols/rows` from a synthetic initial 80×24 (the browser will send a resize as soon as the terminal mounts, which forwards as `MsgSize`).
3. Two pumps:
   - upstream `attachwire` reader → if `MsgOutput`, write WS binary frame; if `MsgSize`, ignore (browser doesn't render remote-size status; the size-mirror UI was a TUI-only thing).
   - WS reader → if binary, send `MsgInput{payload}`; if text + `{type:"resize"}`, send `MsgSize{cols,rows}`.
4. Any error or close on either side cancels both pumps.

Decision: bridge talks `attachwire` directly rather than going through the older `/pty/{stream,input,resize}` REST routes. That keeps the contract narrow (one supervisor endpoint instead of three) and matches what `supervise attach` already does. The `ptyclient` package from dotfiles is *not* ported.

### Spawning supervisors from POST /sessions

Reuses the `pty.Open()` + fork-self pattern from the dotfiles `serve_spawn.go`, translated for the target binary's flag layout (`supervise __supervise --state-dir <d> --key <k> -- <argv...>` instead of `ptydemo supervise ...`). Returns 201 with `{...StateFile, alive:true, socket_path}` once `rpc.sock` appears (3s timeout).

`splitCmd` is a tiny shell-ish tokenizer (whitespace + matched single/double quotes, no escapes), good enough for `bash`, `bash -l`, `sh -c "echo hi"`. Lifted verbatim.

## TS migration

Source-of-truth diff vs. dotfiles:

- `@hayeah/termui` → `@hayeah/supervisor-termui` (package.json `name` only; src untouched)
- `supervisor-webui` → `@hayeah/supervisor-webui`; dependency `"@hayeah/termui": "workspace:*"` → `"@hayeah/supervisor-termui": "workspace:*"`; one import in `webui/src/data/source.ts` and `webui/src/data/live.ts` updated to match.
- pnpm workspace at supervisor repo root: `packages: ["packages/*"]`.

No code changes inside `termui/src/` or `webui/src/` other than the rename. The webui's `data/live.ts` already targets `/api/sessions{,/:key{,/state,/events,/attach}}`, which lines up exactly with the routes above.

`vp` (vite-plus) is already the toolchain in the source webui — keep it.

## devportv3 integration

Add `devport.toml` at the supervisor repo root:

```toml
tmux_session = "supervisor-dashboard"

[[service]]
type = "proxy"

  [[service.routes]]
  name         = "api"
  path         = "/api/"
  strip_prefix = true
  type         = "exec"
  command      = ["supervise", "serve",
                  "--port", "$${PORT}",
                  "--addr", "127.0.0.1",
                  "--state-dir", ".supervise"]
    [service.routes.health]
    type = "http"; path = "/healthz"; interval = "1s"; timeout = "30s"

  [[service.routes]]
  name    = "web"
  path    = "/"
  type    = "http"
  url     = "http://127.0.0.1:$${PORT}"
  # vite dev server, allocated by the exec leaf below

  [[service.routes]]
  name    = "vite"
  type    = "exec"
  command = ["pnpm", "--filter", "@hayeah/supervisor-webui", "dev",
             "--port", "$${PORT}", "--strictPort"]
  cwd     = "."
  # ...
```

The exact final shape may need to be adjusted to whatever devportv3's current TOML actually accepts (the README snippet above shows static + exec siblings; "http url to a sibling exec leaf" is the pattern). I'll iterate against `devport up`'s error messages and look at any existing example specs in `devportv3/docs/` if needed. **No changes to devportv3 itself** — this section is consumer-side only.

# Steps

Phase 1: TS migration

1. Set up pnpm workspace at supervisor repo root (`package.json`, `pnpm-workspace.yaml`, `.gitignore` for `node_modules/`).
2. Copy `termui/` → `packages/supervisor-termui/`, rename package to `@hayeah/supervisor-termui`, `pnpm install`, `pnpm --filter ... build` to verify tsup still produces dist.
3. Copy `webui/` → `packages/supervisor-webui/`, rename package, fix the one workspace dep + two source imports, `vp build` to verify.
4. Boot vite (`pnpm --filter @hayeah/supervisor-webui dev --port <free>`) and hit `/preview` to confirm the mock data source renders end-to-end before any backend exists.

Phase 2: supervise serve

5. Vendor `github.com/coder/websocket` into `go.mod` (used by the bridge).
6. Implement `cmd/supervise/serve.go` — flag parsing, mux registration helper, `getSessions` / `handleSession` / `handleState` / `handleHealth`. Lift the JSON shapes from the dotfiles serve verbatim — webui already speaks them.
7. Implement `cmd/supervise/serve_spawn.go` — `splitCmd`, `createSession`, `spawnSupervise` (translated to `supervise __supervise` argv), `closeSession`.
8. Implement `cmd/supervise/serve_bridge.go` — `handleEvents` SSE proxy and the WS↔attachwire bridge (`handleAttach`). The bridge dials `rpc.sock`, performs the HTTP Upgrade + Hello, then runs two pumps against `attachwire.ReadFrame`/`WriteFrame`.
9. Wire `serve` into `main.go`'s switch and update `usage()`.
10. Smoke test against a live `supervise run -- bash` session: `curl /api/sessions`, `curl -XPOST /api/sessions`, `websocat ws://.../api/sessions/<key>/attach` (or browser).

Phase 3: devportv3 wiring + evidence

11. Write `devport.toml`. `devport up`. Confirm vite + supervise serve come up healthy.
12. `browser open http://localhost:<proxy-port>`. Take a screenshot of the dashboard with at least one live session attached and showing PTY output. Save under `tmp/`. Link from `## Evidence`.
13. Update `supervisor/README.md` with a paragraph on `supervise serve` + a pointer to `devport up` for the example dashboard.

# Verification

- `pnpm --filter @hayeah/supervisor-termui build` and `pnpm --filter @hayeah/supervisor-webui build` both succeed.
- `go test ./...` in the supervisor repo still passes.
- `supervise serve --port 8080 --state-dir /tmp/x` boots; `curl http://127.0.0.1:8080/sessions` returns `{"sessions":[]}`; create + attach loop works against a live `supervise run`.
- `devport up` brings up a healthy stack; `browser screenshot` shows the webui talking to the API (a session list with at least one entry, and a focused terminal with visible PTY output).

# Open questions

None right now — the dotfiles ptydemo serve gives a near-line-for-line reference, the only translation work is the WS↔attachwire bridge (since the target supervisor repo doesn't have the older `/pty/*` REST routes that `ptyclient` was built around — wait, it actually does: `pty_libghostty.go` mounts `/pty/{text,html,vt,stream,input,resize}`. So bridging via `ptyclient` is also viable — but I'm sticking with `/attach` because it's the supported public protocol and the legacy routes might be retired).

# Design notes

- 2026-05-02T10:35Z — Picked `/attach` (HTTP Upgrade + attachwire) over the older `/pty/{stream,input,resize}` REST routes for the WS bridge.
  - Both endpoints exist in the target supervisor repo (saw both in `pty_libghostty.go:410-422`). The ptydemo reference impl in dotfiles uses the `/pty/*` flavor via `ptyclient`.
  - Picked `/attach` because:
    - It's the documented protocol that `supervise attach` uses today — same machinery, different transport on the client side.
    - Single endpoint vs. three, and the resize/input/stream ordering is already serialized server-side via attachwire's framed protocol.
    - Avoids depending on `ptyclient` (which lives only in dotfiles and would otherwise have to be ported too).
  - Loser: `/pty/*` + ptyclient. Less work to type initially (the ptydemo serve.go is ~100 lines that could be lifted whole), but bigger surface area to drag along (the whole `ptyclient` package), and tied to routes that may not be the long-term shape.
  - Trade-off: I have to write the bridge by hand against `attachwire`, ~80 lines of Go. Compared to ~30 lines using `ptyclient.Client`. Worth it.

- 2026-05-02T10:35Z — Putting termui+webui under `packages/` rather than at repo root.
  - Source repo (dotfiles) had them flat at `supervisor/termui/` and `supervisor/webui/`. The target repo is a single Go module at root with many `*.go` files; mixing a `termui/` and `webui/` dir at root reads as more confusing than a `packages/` subtree.
  - Alternative considered: flat at root. Loser because the Go file listing already crowds the root namespace; an extra `termui/` at the top would visually bury `attach.go`/`store.go`/etc.

- 2026-05-02T10:35Z — Renaming `@hayeah/termui` → `@hayeah/supervisor-termui` because the section text says so.
  - Trade-off: name is a little long. But it disambiguates from any future generic `termui` package and groups the two packages by prefix.
