---
slug: migrate-hootty-termui-webui-ts-libs-add-session-serve-wire-devportv3
---

# Goal

Move the existing TypeScript dashboard out of `dotfiles/libs/hayeah-go/session/{termui,webui}` into the `~/github.com/hayeah/hootty` repo as a pnpm workspace, port the dotfiles `ptydemo serve` API as a new `hoot serve` subcommand, and wire the whole thing together with a `devport.toml` that fronts vite + the API on a single proxy. End state: `cd repos/.../session && devport up` brings up a working dashboard at one URL with live session sessions; `/browser screenshot` proves it.

Out of scope: any new termui/webui features, redesigning the wire protocol, changing the session library itself.

# Architecture

## Repo layout (target session repo, post-migration)

```
github.com/hayeah/hootty/
  go.mod / go.sum / go.work          # existing
  *.go                                # session library + attach handler (existing)
  cmd/hoot/
    main.go run.go list.go attach.go  # existing subcommands
    serve.go                          # NEW: cmdServe, serveState, register helper
    serve_bridge.go                   # NEW: handleAttach (WS↔attachwire bridge), handleEvents (SSE proxy)
    serve_spawn.go                    # NEW: createSession + spawnSession + splitCmd
  internal/attachwire/                # existing — used by the bridge
  package.json                        # NEW: workspace root
  pnpm-workspace.yaml                 # NEW
  packages/
    hootty-termui/                # NEW: ported from dotfiles termui (renamed @hayeah/hootty-termui)
      package.json src/ tsup.config.ts tsconfig.json README.md
    hootty-webui/                 # NEW: ported from dotfiles webui (renamed @hayeah/hootty-webui)
      package.json src/ vite.config.ts tsconfig*.json index.html public/
  devport.toml                        # NEW: vite + hoot serve composed under one proxy
```

Package names land as `@hayeah/hootty-termui` and `@hayeah/hootty-webui` per section text. (Source dirs are flat `termui/` / `webui/`; we put them under `packages/` since the repo will host two packages.)

## hoot serve

New subcommand `hoot serve [--state-dir <d>] [--addr 127.0.0.1] [--port N] [--prefix /api]`. Reads from the same state-dir as `hoot run/list/attach` — it's a fan-out HTTP server that lists sessions and bridges per-session WebSocket attaches into each `rpc.sock`'s `/attach` endpoint.

Routes (with `--prefix=/api` mounted both prefixed and bare):

| Method | Path                          | Behaviour                                                                                                    |
|--------|-------------------------------|--------------------------------------------------------------------------------------------------------------|
| GET    | `/sessions`                   | `{sessions:[{...StateFile, alive}]}`                                                                          |
| POST   | `/sessions`                   | `{cmd?, argv?, key?}` → spawn `hoot __session --state-dir <d> --key <k> -- <argv...>`, wait for sock  |
| GET    | `/sessions/{key}`             | state.json                                                                                                    |
| DELETE | `/sessions/{key}`             | SIGTERM `state.Session.PID`, 204                                                                           |
| GET    | `/sessions/{key}/state`       | state.json                                                                                                    |
| GET    | `/sessions/{key}/events`      | SSE proxy of `rpc.sock /events`                                                                              |
| GET    | `/sessions/{key}/attach`      | WebSocket — bridge to `rpc.sock /attach` (HTTP/1.1 Upgrade → `attachwire` framed protocol)                   |
| GET    | `/healthz`                    | `{ok:true,...}`                                                                                              |

### WS↔attachwire bridge

Browser side (`@hayeah/hootty-termui`'s `WebSocketAttach`):
- binary frame, both directions = PTY bytes
- text frame client→server = `{"type":"resize","cols":N,"rows":N}`

Backend side (target session `attach_handler.go` over `rpc.sock`):
- HTTP/1.1 Upgrade `hoot-attach/1`, then `attachwire` frames: `Hello` (cols/rows/replayMode JSON) → `Output` (server→client PTY bytes) / `Size` (server→client effective size JSON) / `Input` (client→server raw bytes) / `Size` (client→server new declared size JSON)

Bridge logic:

1. WS `Accept` → dial `rpc.sock` (with the long-path-relative chdir fallback used by `hoot attach`).
2. Send HTTP Upgrade + `MsgHello{cols,rows, replayMode:"full"}` using `cols/rows` from a synthetic initial 80×24 (the browser will send a resize as soon as the terminal mounts, which forwards as `MsgSize`).
3. Two pumps:
   - upstream `attachwire` reader → if `MsgOutput`, write WS binary frame; if `MsgSize`, ignore (browser doesn't render remote-size status; the size-mirror UI was a TUI-only thing).
   - WS reader → if binary, send `MsgInput{payload}`; if text + `{type:"resize"}`, send `MsgSize{cols,rows}`.
4. Any error or close on either side cancels both pumps.

Decision: bridge talks `attachwire` directly rather than going through the older `/pty/{stream,input,resize}` REST routes. That keeps the contract narrow (one session endpoint instead of three) and matches what `hoot attach` already does. The `ptyclient` package from dotfiles is *not* ported.

### Spawning hoottys from POST /sessions

Reuses the `pty.Open()` + fork-self pattern from the dotfiles `serve_spawn.go`, translated for the target binary's flag layout (`hoot __session --state-dir <d> --key <k> -- <argv...>` instead of `ptydemo hoot ...`). Returns 201 with `{...StateFile, alive:true, socket_path}` once `rpc.sock` appears (3s timeout).

`splitCmd` is a tiny shell-ish tokenizer (whitespace + matched single/double quotes, no escapes), good enough for `bash`, `bash -l`, `sh -c "echo hi"`. Lifted verbatim.

## TS migration

Source-of-truth diff vs. dotfiles:

- `@hayeah/termui` → `@hayeah/hootty-termui` (package.json `name` only; src untouched)
- `hootty-webui` → `@hayeah/hootty-webui`; dependency `"@hayeah/termui": "workspace:*"` → `"@hayeah/hootty-termui": "workspace:*"`; one import in `webui/src/data/source.ts` and `webui/src/data/live.ts` updated to match.
- pnpm workspace at session repo root: `packages: ["packages/*"]`.

No code changes inside `termui/src/` or `webui/src/` other than the rename. The webui's `data/live.ts` already targets `/api/sessions{,/:key{,/state,/events,/attach}}`, which lines up exactly with the routes above.

`vp` (vite-plus) is already the toolchain in the source webui — keep it.

## devportv3 integration

Add `devport.toml` at the session repo root:

```toml
tmux__session = "hootty-dashboard"

[[service]]
type = "proxy"

  [[service.routes]]
  name         = "api"
  path         = "/api/"
  strip_prefix = true
  type         = "exec"
  command      = ["hoot", "serve",
                  "--port", "$${PORT}",
                  "--addr", "127.0.0.1",
                  "--state-dir", ".hoot"]
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
  command = ["pnpm", "--filter", "@hayeah/hootty-webui", "dev",
             "--port", "$${PORT}", "--strictPort"]
  cwd     = "."
  # ...
```

The exact final shape may need to be adjusted to whatever devportv3's current TOML actually accepts (the README snippet above shows static + exec siblings; "http url to a sibling exec leaf" is the pattern). I'll iterate against `devport up`'s error messages and look at any existing example specs in `devportv3/docs/` if needed. **No changes to devportv3 itself** — this section is consumer-side only.

# Steps

Phase 1: TS migration

1. Set up pnpm workspace at session repo root (`package.json`, `pnpm-workspace.yaml`, `.gitignore` for `node_modules/`).
2. Copy `termui/` → `packages/hootty-termui/`, rename package to `@hayeah/hootty-termui`, `pnpm install`, `pnpm --filter ... build` to verify tsup still produces dist.
3. Copy `webui/` → `packages/hootty-webui/`, rename package, fix the one workspace dep + two source imports, `vp build` to verify.
4. Boot vite (`pnpm --filter @hayeah/hootty-webui dev --port <free>`) and hit `/preview` to confirm the mock data source renders end-to-end before any backend exists.

Phase 2: hoot serve

5. Vendor `github.com/coder/websocket` into `go.mod` (used by the bridge).
6. Implement `cmd/hoot/serve.go` — flag parsing, mux registration helper, `getSessions` / `handleSession` / `handleState` / `handleHealth`. Lift the JSON shapes from the dotfiles serve verbatim — webui already speaks them.
7. Implement `cmd/hoot/serve_spawn.go` — `splitCmd`, `createSession`, `spawnSession` (translated to `hoot __session` argv), `closeSession`.
8. Implement `cmd/hoot/serve_bridge.go` — `handleEvents` SSE proxy and the WS↔attachwire bridge (`handleAttach`). The bridge dials `rpc.sock`, performs the HTTP Upgrade + Hello, then runs two pumps against `attachwire.ReadFrame`/`WriteFrame`.
9. Wire `serve` into `main.go`'s switch and update `usage()`.
10. Smoke test against a live `hoot run -- bash` session: `curl /api/sessions`, `curl -XPOST /api/sessions`, `websocat ws://.../api/sessions/<key>/attach` (or browser).

Phase 3: devportv3 wiring + evidence

11. Write `devport.toml`. `devport up`. Confirm vite + hoot serve come up healthy.
12. `browser open http://localhost:<proxy-port>`. Take a screenshot of the dashboard with at least one live session attached and showing PTY output. Save under `tmp/`. Link from `## Evidence`.
13. Update `session/README.md` with a paragraph on `hoot serve` + a pointer to `devport up` for the example dashboard.

# Verification

- `pnpm --filter @hayeah/hootty-termui build` and `pnpm --filter @hayeah/hootty-webui build` both succeed.
- `go test ./...` in the session repo still passes.
- `hoot serve --port 8080 --state-dir /tmp/x` boots; `curl http://127.0.0.1:8080/sessions` returns `{"sessions":[]}`; create + attach loop works against a live `hoot run`.
- `devport up` brings up a healthy stack; `browser screenshot` shows the webui talking to the API (a session list with at least one entry, and a focused terminal with visible PTY output).

# Open questions

None right now — the dotfiles ptydemo serve gives a near-line-for-line reference, the only translation work is the WS↔attachwire bridge (since the target session repo doesn't have the older `/pty/*` REST routes that `ptyclient` was built around — wait, it actually does: `pty_libghostty.go` mounts `/pty/{text,html,vt,stream,input,resize}`. So bridging via `ptyclient` is also viable — but I'm sticking with `/attach` because it's the supported public protocol and the legacy routes might be retired).

# Design notes

- 2026-05-02T10:35Z — Picked `/attach` (HTTP Upgrade + attachwire) over the older `/pty/{stream,input,resize}` REST routes for the WS bridge.
  - Both endpoints exist in the target session repo (saw both in `pty_libghostty.go:410-422`). The ptydemo reference impl in dotfiles uses the `/pty/*` flavor via `ptyclient`.
  - Picked `/attach` because:
    - It's the documented protocol that `hoot attach` uses today — same machinery, different transport on the client side.
    - Single endpoint vs. three, and the resize/input/stream ordering is already serialized server-side via attachwire's framed protocol.
    - Avoids depending on `ptyclient` (which lives only in dotfiles and would otherwise have to be ported too).
  - Loser: `/pty/*` + ptyclient. Less work to type initially (the ptydemo serve.go is ~100 lines that could be lifted whole), but bigger surface area to drag along (the whole `ptyclient` package), and tied to routes that may not be the long-term shape.
  - Trade-off: I have to write the bridge by hand against `attachwire`, ~80 lines of Go. Compared to ~30 lines using `ptyclient.Client`. Worth it.

- 2026-05-02T10:35Z — Putting termui+webui under `packages/` rather than at repo root.
  - Source repo (dotfiles) had them flat at `session/termui/` and `session/webui/`. The target repo is a single Go module at root with many `*.go` files; mixing a `termui/` and `webui/` dir at root reads as more confusing than a `packages/` subtree.
  - Alternative considered: flat at root. Loser because the Go file listing already crowds the root namespace; an extra `termui/` at the top would visually bury `attach.go`/`store.go`/etc.

- 2026-05-02T10:35Z — Renaming `@hayeah/termui` → `@hayeah/hootty-termui` because the section text says so.
  - Trade-off: name is a little long. But it disambiguates from any future generic `termui` package and groups the two packages by prefix.
