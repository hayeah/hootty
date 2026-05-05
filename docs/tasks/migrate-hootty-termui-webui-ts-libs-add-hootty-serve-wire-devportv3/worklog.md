---
status: done
section: Migrate hootty termui+webui TS libs; add hootty serve; wire devportv3
slug: migrate-hootty-termui-webui-ts-libs-add-hootty-serve-wire-devportv3
mode: worktree
spec: spec.md
created: 2026-05-02T10:28:13Z
---

> ## Migrate hootty termui+webui TS libs; add hootty serve; wire devportv3
>
> ---
> status:
>   type: open
> ---
>
> **Pending the smoke-test-agent-loop section above** — do not start until that lgtm's.
>
> Migrate the existing TS libs out of dotfiles into the hootty repo, add a server that powers the example app, and stitch everything together with devportv3.
>
> Sources to migrate from:
> - `/Users/me/github.com/hayeah/dotfiles/libs/hayeah-go/hootty/termui` — reusable terminal component
> - `/Users/me/github.com/hayeah/dotfiles/libs/hayeah-go/hootty/webui` — example app consuming termui
>
> Targets (in `~/github.com/hayeah/hootty`):
> - `@hayeah/hootty-termui` — reusable component package
> - `@hayeah/hootty-webui` — example app package
>
> Tooling and scaffolding:
> - Use the `/webui` skill for the package layout and dev workflow.
> - Use `vp` (vite-plus) as the toolchain.
>
> New backend:
> - `hoot serve [--state-dir]` — HTTP API that routes to the individual `rpc.sock` of each process in the state-dir. This is what powers the example webui app. Reuse the per-session socket protocol; the server is a fan-out / multiplexer.
>
> Integration:
> - Pull everything together with `hayeah/devportv3`:
>   - proxy to vite live dev server (webui)
>   - proxy to `hoot serve` API
> - Evidence: use the `/browser` skill to take screenshots of the running webui talking to live hootty sessions.
>
> - [ ] migrate termui and webui packages, scaffold with vp + /webui conventions
> - [ ] implement `hoot serve` and verify the webui talks to it
> - [ ] wire devportv3 and capture browser screenshots as evidence

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### Phase 1 — TS migration (boss-doc box: migrate termui and webui packages)

- [x] pnpm workspace at hootty repo root (package.json, pnpm-workspace.yaml, .gitignore) — 29a222b
- [x] copy termui → packages/hootty-termui, rename to @hayeah/hootty-termui, build green — 3f6ca29
- [x] copy webui → packages/hootty-webui, rename, fix workspace dep + 3 imports, build green — e38da7d
- [x] vite dev /preview smoke (mock data source) renders before any backend — tmp/preview-smoke.png

### Phase 2 — hoot serve (boss-doc box: implement hoot serve and verify)

- [x] go.mod: add github.com/coder/websocket — c646793
- [x] cmd/hoot/serve.go — flags, mux, GET /sessions, GET/DELETE /sessions/{key}, /healthz — 21bf44b
- [x] cmd/hoot/serve_spawn.go — POST /sessions (splitCmd + spawnHoot), closeSession — 2a93db2
- [x] cmd/hoot/serve_bridge.go — handleAttach (WS↔attachwire), handleEvents (SSE proxy) — 47b9ec5
- [x] wire serve subcommand into main.go switch + usage() — 47b9ec5
- [x] curl/websocat smoke against a live hoot run — tmp/serve-smoke-transcript.txt

### Phase 3 — devportv3 wiring + evidence (boss-doc box: wire devportv3 + screenshots)

- [x] devport.toml composing vite + hoot serve under one proxy — 3a0e72b
- [x] devport up → healthy stack
- [x] browser screenshot of dashboard with live session + PTY output, saved under tmp/ — tmp/dashboard-attached.png
- [x] update hootty/README.md with serve + devport pointer — 3a0e72b

## Agent log
- 2026-05-02T10:28Z blocked: section text says 'Pending the smoke-test-agent-loop section above — do not start until that lgtm's.' Standing down until that prerequisite section is merged. Re-nudge me when it's cleared.
- 2026-05-02T10:33Z unblocked. wrote spec.md, seeded 13 todos across 3 phases. hootty worktree leased at slot 000. starting phase 1 (TS migration).
- 2026-05-02T10:33Z phase 1.1 done — pnpm workspace scaffold landed (29a222b)
- 2026-05-02T10:38Z phase 1 complete — termui (3f6ca29) + webui (e38da7d) ported, /preview screenshot captured (tmp/preview-smoke.png) showing sessions sidebar + terminal pane against MockDataSource. starting phase 2 (hoot serve).
- 2026-05-02T10:44Z phase 2 complete — hoot serve landed across 4 commits (c646793 21bf44b 2a93db2 47b9ec5); GET/POST/DELETE /sessions and WS /attach all proved against a live hoot run; transcript at tmp/serve-smoke-transcript.txt. starting phase 3 (devport.toml + browser screenshot).
- 2026-05-02T10:52Z phase 3 done — devport.toml + README pointer landed (3a0e72b); browser screenshot tmp/dashboard-attached.png shows the dashboard at the proxy URL with live demo1 bash session attached over WS, and ticker exited as expected. status: done.

## Boss log
- 2026-05-02T10:28Z prereq cleared — the smoke test section already lgtm'd at sha 8f86475. that "pending" line was a queueing note for the boss loop, not for you. proceed with the migration. flip status back to working.

## Evidence

### Phase 1 — TS migration

Both packages build clean from the new pnpm workspace:

```
$ pnpm --filter @hayeah/hootty-termui build
ESM dist/index.js 5.32 KB
DTS dist/index.d.ts 1.92 KB

$ pnpm --filter @hayeah/hootty-webui build
vite v8.0.10 building client environment for production...
✓ 51 modules transformed.
dist/index.html                     0.45 kB
dist/assets/index-DAS1QtYZ.css     20.16 kB
dist/assets/index-fH3_Kubi.js     271.72 kB
dist/assets/ghostty-web-…js     1,171.32 kB   (existing — pre-migration)
✓ built in 143ms
```

`/preview` against the MockDataSource renders the sessions sidebar (bash / vim / htop / build-00) and the terminal pane (live `ls -la` mock output) with no console errors:

![preview](tmp/preview-smoke.png)

This proves the import rewrite (`@hayeah/termui` → `@hayeah/hootty-termui` in 3 webui files) didn't break the workspace dep. The webui's `LiveDataSource` already targets `/api/sessions{,/:key{,/state,/events,/attach}}`, which is exactly the route shape phase 2's `hoot serve` will expose.

Commits: 29a222b (workspace) · 3f6ca29 (termui) · e38da7d (webui).

### Phase 2 — hoot serve

`go build ./cmd/hoot` and `go test ./...` both clean.

End-to-end smoke transcript at [`tmp/serve-smoke-transcript.txt`](tmp/serve-smoke-transcript.txt). Highlights:

- `GET /api/healthz` → 200 `{"ok":true,...}`
- `GET /api/sessions` returns the live `sm1` session plus the just-deleted `api1` (with `alive:false`, `state:"exited"`) — proving list reflects flock state.
- `POST /api/sessions {cmd:"sh -c \"echo from-api && sleep 30\"", key:"api2"}` → 201 with full StateFile + `socket_path` — session survives, `state:"running"`, real pid.
- `DELETE /api/sessions/api2` → 204; subsequent list shows it gone (the hootty exited cleanly).
- `WS /api/sessions/sm1/attach` (Python `websockets` smoke client) — after sending the resize text frame and `b"ls /tmp\n"` binary input, the bridge replays `hello world` (the bash one-shot's previous stdout, delivered via upstream `MsgOutput` → WS binary frame) and echoes the typed `ls /tmp` (input flowed through WS binary → upstream `MsgInput` → PTY echo back). Both pumps verified.

Commits: c646793 (deps) · 21bf44b (serve.go) · 2a93db2 (serve_spawn.go) · 47b9ec5 (serve_bridge.go + main.go wiring).

### Phase 3 — devportv3 wiring + browser screenshot

`devport up --daemon` against the included `devport.toml` brings up the full stack on a single proxy port:

```
2026/05/02 17:49:09 INFO wrote env file path=…/devport.env lines=4
devport up: stack is healthy
  _proxy0               http://127.0.0.1:20000
  API                   http://127.0.0.1:20001
  VITE                  http://127.0.0.1:20002
```

Hitting the proxy:

- `GET http://127.0.0.1:20000/api/healthz` → `{"ok":true,...}` (proxy strip_prefix=true → `/healthz` on the api leaf)
- `GET http://127.0.0.1:20000/` → vite-served `index.html` with `<script type="module" src="/@vite/client">` (the live dev server, proxied by devport)
- `GET http://127.0.0.1:20000/api/sessions` → `{"sessions":[]}` initially, then populates after `POST`s

After creating two sessions via the API (`demo1` running an interactive bash, `ticker` running a 5-tick loop + sleep), the browser dashboard shows:

![dashboard with live session attached](tmp/dashboard-attached.png)

What this proves end-to-end through the proxy:

- **REST polling**: sidebar lists both sessions with their live state (demo1 `RUNNING`, ticker `EXITED` after its loop finished). The webui polls `/api/sessions` once a second; status transitions are reflected without a refresh.
- **WS attach bridge**: clicking demo1 mounts a ghostty-web canvas backed by `WebSocketAttach` against `ws://…/api/sessions/demo1/attach`. The terminal renders the bash session's full replay (welcome banner + zsh hint + `bash-3.2$ ` prompt) — bytes flowed `attachwire.MsgOutput` (rpc.sock) → bridge → WS binary frame → ghostty-web. The cursor is live.

The unattached state and the `/preview` mock-data render are also captured as `tmp/dashboard-live.png` and `tmp/preview-smoke.png` for completeness.

Smoke-test artifact for direct API access (curl + Python websockets, no browser): `tmp/serve-smoke-transcript.txt`.

Commits: 3a0e72b (devport.toml + README).

## Trouble report

### Phase 3 — devport.toml iteration

Two failing-then-fixed snags worth recording for the next time someone wires a vite-plus app behind devportv3:

- **`/api/healthz` 404 with strip_prefix=false.** First draft set `strip_prefix=false` and pointed health at `/api/healthz`, then mounted serve without `--prefix`. The devport health probe goes *directly* to the api leaf at its own port, not through the proxy — so it asked for `/api/healthz` on a server that only knew about bare `/healthz`. Fix: flipped to `strip_prefix=true` (cleaner for the leaf anyway) and pointed health at `/healthz`. Lesson: in devport, leaf health is leaf-local; proxy paths are a routing concern, not a probe concern.

- **`pnpm exec vite` exits with `Command "vite" not found`.** The webui's `vite` is npm-aliased to `@voidzero-dev/vite-plus-core`, so the bare `vite` binary isn't on the path. Fix: `pnpm exec vp dev` (the vite-plus wrapper, which the package's own `dev` script also uses). This is /webui-skill convention but I forgot it on the first pass.

### Phase 2 — chdir indirection over-engineered, ripped out

In the first draft of `serve_bridge.go` I wrote ~7 layers of chdir indirection (`chdir → chdirRaw → osChdir → systemChdir → osChdirReal → syscallChdir → realOsChdir → goOsChdir`) — a "test seam" with nothing to seam. CLAUDE.md is explicit about this: "Don't add features, refactor, or introduce abstractions beyond what the task requires." Replaced with a 5-line `dialSock` that just calls `os.Chdir` / `os.Getwd` under a single `chdirMu` lock. Caught before commit.
