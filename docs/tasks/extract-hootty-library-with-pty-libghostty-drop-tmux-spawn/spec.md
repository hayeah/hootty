# spec.md — extract hootty library; pty+libghostty (drop tmux spawn)

## Goal

Lift the hootty code at `~/github.com/hayeah/dotfiles/libs/hayeah-go/hootty` into a new standalone repo `github.com/hayeah/hootty`. Trim it to the bits the task names: a process hootty that spawns a child on a libghostty-backed PTY, tees the raw output for replay, and exposes terminal state through an embeddable `http.ServeMux` (text / HTML / raw VT). Ship a small `hoot` example CLI that serves the mux on a unix socket.

In scope:
- New repo `hayeah/hootty` (module `github.com/hayeah/hootty`).
- Library: PTY spawn + libghostty VT + tee recorder + HTTP mux.
- Switch VT binding from `code.selman.me/hauntty/libghostty` (WASM) to `github.com/mitchellh/go-libghostty` (cgo). The task brief points at the mitchellh binding's guide as reference.
- Example `hoot` CLI: launches a process with a target dir, serves the mux on `<dir>/rpc.sock`.
- Drop the tmux spawn backend (`pty_tmux.go`, `tmux.go`, the TmuxSpawn config). Drop unused webui/termui/ptyclient subpackages — those are not the hoot example shape the task describes.

Explicitly out of scope (per task brief):
- Multi-attachment window-size negotiation.
- Remote HTTP access (everything stays on a unix socket).
- Agent-driven shell automation.
- An `attach` CLI that replays into tmux. Future task; the tee recording is the seam for it.

## Architecture

Repo layout:

```
hayeah/hootty/
  go.mod                          # module github.com/hayeah/hootty
  README.md
  Makefile                        # libghostty-vt build/PKG_CONFIG glue
  hootty.go                   # Runner, Mux(), Run(ctx)
  hootty_iface.go             # Hootty interface
  service.go                      # Service interface
  pty.go                          # PTY interface
  pty_libghostty.go               # libghostty-backed PTY
  pty_libghostty_routes.go        # mux registration: /pty/text|html|vt|stream|input|resize|send-keys
  pty_libghostty_keys.go          # tmux-style key name → libghostty Key+Mods
  recorder.go                     # tee writer: raw PTY bytes → file
  store.go store_test.go          # external readers (StateDir scan, IsAlive, Resolve)
  state.go atomic.go flock.go ... # plumbing kept as-is
  signals.go eventbus.go poll.go tailfile.go
  cmd/hoot/
    main.go run.go hoot.go service.go common.go
```

Key flow (matches existing dotfiles design — emulator pattern):
1. `hoot <args> -- <cmd>` opens a PTY pair via `creack/pty`, sets winsize, forks itself with `__hoot` subcommand: stdio = slave, ExtraFiles[0] = master, Setsid (no controlling tty in parent).
2. `__hoot` picks master off fd 3, builds `LibghosttyPTY`, builds the `Runner`, calls `runner.Run(ctx)`.
3. `Runner.Run` flocks `<dir>/`, writes initial `state.json`, registers default routes (`/state`, `/events`) plus PTY routes, listens on `<dir>/rpc.sock`.
4. The default `RunCmdService` `exec.Cmd`s the user command with stdio = inherited (slave), `Setctty=true Ctty=0`, publishes state transitions, waits.
5. `LibghosttyPTY.readLoop` reads master → tees raw bytes to `<dir>/pty.log` + feeds the libghostty Terminal + fans out to live subscribers.

VT binding swap (this is the load-bearing part):

| Existing (hauntty/libghostty)                    | New (mitchellh/go-libghostty)                                         |
| ------------------------------------------------ | --------------------------------------------------------------------- |
| `rt.NewTerminal(cols, rows, scrollback)`         | `libghostty.NewTerminal(WithSize, WithMaxScrollback, WithWritePty)`   |
| `term.Feed(bytes)`                               | `term.VTWrite(bytes)`                                                 |
| `term.DumpScreen(DumpPlain/VTSafe/VTFull)`       | `Formatter` with `FormatterFormatPlain` / `FormatterFormatVT`         |
| (no HTML)                                        | `Formatter` with `FormatterFormatHTML`                                |
| `term.EncodeKey(keyCode, mods)`                  | `KeyEncoder` + `KeyEvent` (set Key, Mods, Action, optional UTF8)      |
| `term.Resize(cols, rows)`                        | `term.Resize(cols, rows, cellW, cellH)`                               |
| Single dispatcher goroutine (WASM not threadsafe)| Same model required (libghostty is single-threaded per terminal)      |

The dispatcher pattern stays. We register `WithWritePty` to forward terminal replies (DA/DECRPM/cursor-pos queries) back to the master — without it, queries silently disappear and apps like vim break on first VT probe.

Recorder: a `Recorder` that wraps a `*os.File` opened at `<dir>/pty.log`, with a `Write(b []byte) (int, error)` and a buffered fsync on close. The dispatcher writes a copy to the recorder before fanning out to live subscribers; tee placement is in `readLoop`'s post-`VTWrite` hook so subscribers and the recorder see byte-identical streams.

HTTP routes (the new shape; supersedes the existing /pty/snapshot+scrollback):
- `GET /pty/text`     — `FormatterFormatPlain`, `?scrollback=1` to include scrollback. `WithFormatterTrim(true)`.
- `GET /pty/html`     — `FormatterFormatHTML`, `?scrollback=1`. Returns just the formatter fragment.
- `GET /pty/vt`       — `FormatterFormatVT` of the current screen (snapshot you can replay into a tty).
- `GET /pty/stream`   — chunked binary: first chunk = VT snapshot, subsequent chunks = live bytes from readLoop fanout. The seam an `attach` CLI consumes.
- `POST /pty/input`   — body = raw bytes → master.Write.
- `POST /pty/resize`  — `{cols, rows}` JSON → `pty.Setsize` + `term.Resize`.
- `POST /pty/send-keys` — `{keys: [...]}` JSON → name lookup + `KeyEncoder` → master.Write.

Build: `mitchellh/go-libghostty` is cgo + pkg-config; ship a `Makefile` that exports `PKG_CONFIG_PATH=$HOME/github.com/mitchellh/go-libghostty/build/_deps/ghostty-src/zig-out/share/pkgconfig` (configurable via env). README documents both static-default and the prereqs (zig 0.15.2, cmake, pkg-config).

## Steps

Phase 1 — repo skeleton
- Init `~/github.com/hayeah/hootty` as a git repo, master branch, baseline empty commit.
- `boss checkout` it into the workspace.
- Create `go.mod` for `github.com/hayeah/hootty`, add deps (`creack/pty`, `mitchellh/go-libghostty`, `golang.org/x/term`).
- Add Makefile + README skeleton.

Phase 2 — port library bits (no PTY yet)
- Copy `atomic.go state.go store.go flock.go poll.go tailfile.go signals.go eventbus.go service.go hootty_iface.go hootty.go` (and their tests) into the new repo. Strip TmuxSpawn references.
- Update import paths to `github.com/hayeah/hootty`.
- Drop the `Plugin`/`KillOnExit`/`Spawn` shape from old `HoottyConfig`. Keep the four-field shape that the dotfiles version already converged on (StateDir, Key, Service, PTY).
- `go vet ./...` + run the tests that don't need PTY.

Phase 3 — pty + libghostty (the meat)
- Write `pty.go` (interface; same shape as dotfiles).
- Write `pty_libghostty.go` against `mitchellh/go-libghostty`. Single dispatcher goroutine; `WithWritePty` forwarding to master.
- Write `pty_libghostty_keys.go` — tmux-style key names → `libghostty.Key*` + `Mod*` constants + KeyEvent setup.
- Write `recorder.go` — file-backed tee writer.
- Wire recorder into `readLoop` — tee before fanout.

Phase 4 — http routes
- `pty_libghostty_routes.go` registering the seven endpoints listed above against a passed-in `*http.ServeMux`.
- `RegisterRoutes(mux)` is what `Runner.Run` calls when `cfg.PTY` implements the optional interface — same lift as dotfiles.

Phase 5 — `hoot` CLI
- `cmd/hoot/{main,run,hoot,service,common}.go`. Two subcommands: `hoot run -- <cmd>` (parent) and `hoot __hoot --key K --state-dir D -- <cmd>` (child fork). Shape mirrors dotfiles' ptydemo, just renamed and trimmed.

Phase 6 — verification
- Unit tests for: store, atomic write, flock, recorder roundtrip, libghostty PTY (feed bytes, dump screen, format html/plain/vt, send-keys, resize). Pattern-match dotfiles' `pty_libghostty_test.go` but rewritten for mitchellh API.
- E2E smoke: `hoot run -- bash -c 'echo hello; sleep 1'` in detached mode (no attach), then curl `/pty/text`, `/pty/html`, `/pty/vt`, `/pty/stream` over the unix socket, expect "hello" in each.

Phase 7 — README + finalize
- README with: motivation, library API, build setup (libghostty-vt prereqs), `hoot` CLI usage, future-work pointers (attach CLI / recorded log replay).

## Verification

- `go test ./...` from the repo root passes (with `PKG_CONFIG_PATH` exported via Makefile target `make test`).
- `make build` produces `bin/hoot`.
- E2E transcript captured under `tmp/`:
  - Start: `hoot run --state-dir <tmp> --key demo -- bash -lc 'echo hello-world; printf "\\x1b[1mbold\\x1b[0m\\n"; sleep 30' &`
  - Wait for `<tmp>/demo/rpc.sock`.
  - `curl --unix-socket <sock> http://x/pty/text` → contains `hello-world` and `bold` (no escapes).
  - `curl --unix-socket <sock> http://x/pty/html` → contains `font-weight: bold` and `hello-world`.
  - `curl --unix-socket <sock> http://x/pty/vt | xxd | head` → contains `\x1b[1m` SGR.
  - `curl --unix-socket <sock> http://x/state` → JSON with state=running, pid > 0.
  - Send keys: `curl -X POST --unix-socket <sock> -d '{"keys":["C-c"]}' http://x/pty/send-keys` → child exits, `/state` shows state=exited.
  - Recorded log: `xxd <tmp>/demo/pty.log` — contains the full byte stream replayed by the child.

## Open questions

None — task brief is concrete enough to go straight to implementation. Will append `## Design notes` entries as decisions surface.

## Design notes

- 2026-05-02T06:40Z — Switched VT-binding from `code.selman.me/hauntty/libghostty` (Go-over-WASM, transpiled from Zig) to `github.com/mitchellh/go-libghostty` (cgo against libghostty-vt) per task brief.
  - The task explicitly references `/Users/me/Dropbox/notes/2026-04-30/go-libghostty-guide_claude.md` and the mitchellh repo, so this isn't really a choice — it's the brief.
  - Practical consequences:
    - Build now needs `pkg-config` + `libghostty-vt` (`PKG_CONFIG_PATH`), and prereqs zig 0.15.2 + cmake. Captured in the `Makefile` (sets `PKG_CONFIG_PATH=$LIBGHOSTTY/build/_deps/ghostty-src/zig-out/share/pkgconfig` from `LIBGHOSTTY=$HOME/github.com/mitchellh/go-libghostty`) and the README prereq section.
    - API differences: `Feed`→`VTWrite`, `DumpScreen(format)`→`Formatter`, `EncodeKey(code,mods)` (1-shot) → `KeyEncoder` + `KeyEvent` (per-keystroke struct + `SetOptFromTerminal`), `Resize(c,r)`→`Resize(c,r,cellW,cellH)` (cellW/H are 0 for headless; only used by Kitty graphics).
    - libghostty's `MaxScrollback` defaults to **0** (no scrollback). Set explicitly to 10_000 in `NewLibghosttyPTY`. Otherwise `/pty/text` and friends would silently drop the moment the screen scrolls.
    - Effects: registered `WithWritePty` to forward terminal replies (DA/DECRPM/cursor-pos) back to the master. Without it, vim and other apps probing on startup go silent because their queries vanish into a void. Same as the dotfiles version — just expressed differently.
  - Module pinning: the binding has no semver tag, so `go.mod` has a pseudo-version and a non-committed `go.work` pins the local checkout (`/Users/me/github.com/mitchellh/go-libghostty`). `go.work` is gitignored — once the upstream tags, drop it.

- 2026-05-02T06:40Z — Endpoint shape: `/pty/{text,html,vt}` instead of the dotfiles' `/pty/{snapshot,scrollback}` + `?escapes=0|1`.
  - The task brief asks for "plain text (ghostty formatter), HTML (ghostty formatter), raw VT". One verb per format reads cleaner than one verb with a query toggle.
  - Dropped the `?lines=N` parameter the dotfiles version had stubbed (it was a TODO, never implemented — libghostty's formatter has no row-range API, so honoring it would mean walking the grid by hand). Same reason it was a no-op there. Document in the README if anyone asks.
  - HTML output is just the formatter's fragment (single styled `<div>` with `font-family: monospace; white-space: pre`). Consumers wrap and supply palette CSS — see the libghostty guide for the recipe.

- 2026-05-02T06:40Z — Worker stderr leaks into the captured PTY stream because the worker process inherits stdio = PTY slave; the hootty lib's default `slog.Default()` writes to stderr → the slave → the master → libghostty Terminal → `/pty/text` shows the hootty's own log lines.
  - Caught at e2e smoke: `acquired flock hootty=demo` appeared in the first `/pty/text` capture.
  - Fix: in `cmd/hoot/hoot.go` (the `__hoot` entry), redirect `slog.Default()` to `<state>/<key>/hootty.log` before constructing the Runner. Falls back to `io.Discard` if the file can't be opened.
  - This is an example-CLI concern, not a library concern. Library users embedding the Runner in their own server keep their own `slog.Default()`. Documented behavior: when running a worker process whose stdio is a PTY, redirect logging to a file or `io.Discard`.

- 2026-05-02T06:40Z — Test polling vs. round-trip-flush.
  - The dispatcher pattern means master.Read → readLoop → `p.do(feed)` is async with respect to test code calling `p.FormatText()`. A single round-trip call can race the readLoop's feed action onto the dispatcher's queue.
  - Tried: round-trip twice (does not help — same race shifted). Tried: ad-hoc Sleep (flaky on slow CI). Settled on: poll FormatText with a 2-second deadline, breaking when the expected token shows up. Cheap, deterministic, ~10ms typical resolution.
  - If we ever want a synchronous "flush" primitive, the lift is small: add a `Sync()` method that does a do(no-op) AND signals when the readLoop's most recent in-flight `do()` has completed. Not worth it for now — polling is fine.
