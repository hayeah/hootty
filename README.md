# supervisor

A small Go library + reference CLI that supervises a single child process.
The child is spawned on a libghostty-backed PTY; its raw output is teed to a
file for replay; and the current screen state is exposed through an
embeddable `http.ServeMux` (plain text / HTML / raw VT).

This is the slimmer cousin of `~/github.com/hayeah/dotfiles/libs/hayeah-go/supervisor`,
extracted so it can stand on its own. The major shift from the dotfiles
version: tmux is no longer a spawn backend. The supervisor opens its own
PTY and runs Ghostty's VT parser against the byte stream
([mitchellh/go-libghostty](https://github.com/mitchellh/go-libghostty)).

## Status

In progress — see `docs/tasks/extract-supervisor-library-with-pty-libghostty-drop-tmux-spawn/spec.md`.

## Build

`libghostty-vt` is a Zig library reachable via cgo + pkg-config. Build it
once locally (clone, `make build`), then point this repo at it via
`PKG_CONFIG_PATH`. The `Makefile` defaults to
`$LIBGHOSTTY/build/_deps/ghostty-src/zig-out/share/pkgconfig` where
`$LIBGHOSTTY` is `~/github.com/mitchellh/go-libghostty` by default.

```sh
make build      # → bin/supervise
make test
```

Prereqs (one-time):

- Zig 0.15.2 (`mise use -g zig@0.15.2` — Zig 0.16 does NOT work)
- CMake
- pkg-config

## Library shape

```go
import "github.com/hayeah/supervisor"

ptyImpl, _ := supervisor.NewLibghosttyPTY(master, cols, rows,
    supervisor.WithRecorder(recorderFile))

run := supervisor.New(supervisor.SupervisorConfig{
    StateDir: ".state",
    Key:      "demo",
    Service:  myService,   // implements supervisor.Service
    PTY:      ptyImpl,
})
err := run.Run(ctx)        // blocks; serves rpc.sock
```

The `Runner.Mux()` http.ServeMux is the public surface. Mount it standalone
(as `supervise` does over a unix socket) or embed it into a larger Go HTTP
server.

### HTTP endpoints contributed by `LibghosttyPTY`

| Path              | Method | Description                                                   |
| ----------------- | ------ | ------------------------------------------------------------- |
| `/pty/text`       | GET    | Plain-text formatter dump. `?scrollback=1` to include history.|
| `/pty/html`       | GET    | HTML fragment dump (libghostty `FormatterFormatHTML`).        |
| `/pty/vt`         | GET    | Self-contained VT replay of the current screen.               |
| `/pty/stream`     | GET    | Chunked binary: VT snapshot + live PTY bytes.                 |
| `/pty/input`      | POST   | Body bytes → PTY master (raw passthrough).                    |
| `/pty/resize`     | POST   | `{cols, rows}` JSON → resize.                                 |
| `/attach`         | GET    | HTTP/1.1 Upgrade → binary attach protocol (see below).        |

Plus the always-present library routes:

| Path              | Description                                          |
| ----------------- | ---------------------------------------------------- |
| `/state`          | JSON snapshot of `state.json`.                       |
| `/events`         | SSE stream of state-change events.                   |

## `supervise` CLI

```sh
supervise run     [--state-dir <dir>] [--key <key>] -- <cmd> [args...]
supervise list    [--state-dir <dir>]
supervise resolve [--state-dir <dir>] <id-or-prefix>
supervise attach  [--host <addr>] [--state-dir <dir>] [--no-reconnect]
                  [--prefix-key <key>] <id-or-prefix>
supervise serve   [--state-dir <dir>] --bind <host:port> [--prefix /api]
```

`--state-dir` defaults to `~/.supervise` for every subcommand.

`supervise list` emits JSONL — one line per session, each line a
serialized `supervisor.StateFile` (the same shape `<dir>/<key>/state.json`
holds on disk). Pipe through `jq -s` if you want an array.

`run` opens a PTY pair, forks an internal supervisor process (with the
master on fd 3 and stdio = slave), and serves the mux on
`<dir>/<key>/rpc.sock`. The supervisor holds a flock on `<dir>/<key>/`
for the lifetime of the supervised child.

If `--key` is omitted, `supervise` generates a random short id (3–8
chars drawn from `0-9a-z` minus `l` and `o`, collision-checked against
existing sessions). The chosen key is printed on stdout.

Anywhere a session key is accepted (including `resolve`), you can pass
either the full id or any unique prefix (minimum 3 characters,
case-insensitive). Ambiguous prefixes fail with an error listing the
matching ids; this is what makes the random ids ergonomic to use.

### `supervise attach`

`attach` connects the local terminal to a running supervisor as a
"dumb pipe with two side-channels": stdin → PTY master, master →
stdout, plus a SIGWINCH → resize side-channel. The local termios is
put into raw mode (no echo, no line buffering, no `Ctrl-C` →
local SIGINT) so signal generation happens on the *remote* slave
ldisc, not locally — the user's `Ctrl-C` reaches the supervised
child as expected.

Multiple attaches can drive the same session simultaneously. The
PTY winsize is negotiated as `min(cols)`, `min(rows)` across all
connected attaches; the negotiated size is broadcast back to every
attach as a `Size` frame whenever it changes.

Initial replay: a libghostty-formatted VT snapshot is sent as the
first output frame, then the connection switches to live. On the
primary screen this is the active screen's viewport + scrollback up to
`max_scrollback`, plus cursor position, active SGR style, and
non-default modes. If the child is currently on an alternate screen
(tmux, vim, less, etc.), the snapshot is ordered as primary scrollback
first, then alternate-screen entry and the active alternate contents.
That builds both screen buffers in the attaching terminal, so the
later live alternate-screen exit restores the recorded primary
scrollback rather than the attach client's previously empty local
primary. The snapshot subscribe pair is race-free — both happen on the
dispatcher goroutine, so live chunks delivered after subscribe carry
no overlap with the snapshot.

The previous `--no-full-replay` flag and the alternative `pty.log`
replay arm were removed. Replaying raw `pty.log` re-issued every
terminal-query escape the child ever sent (`DA`, `DSR`, `OSC 11 ?`,
…) which the user's real terminal would dutifully answer back into
the child's stdin — the same root cause as the live "junk chars
after tmux detach" symptom, just spread across the whole session.
The recorder still writes `pty.log` for offline analysis; it's just
not the source for attach replay.

Live fanout strips terminal-query escape sequences before sending
to the user's real terminal (the libghostty emulator on the
supervisor already auto-replies, so a second answer from the user's
terminal would inject duplicate bytes into the child's stdin). The
recorder + emulator branch sees raw bytes; only the user-facing
fanout is filtered. See `vtquery_stripper.go`.

Inside an attach, a mosh-style prefix key introduces commands. The
default is `C-^` (Ctrl-^, 0x1e — same as mosh; chosen so it does not
collide with tmux's `C-b`). Override via `--prefix-key C-a`,
`--prefix-key 0x1c`, …; printable bytes are rejected at flag-parse time.

| sequence              | action                                            |
| --------------------- | ------------------------------------------------- |
| `<prefix> .`          | detach (clean exit 0)                             |
| `<prefix> ^`          | send literal prefix byte to remote                |
| `<prefix> Ctrl-Z`     | suspend `supervise attach` (SIGTSTP; `fg` resumes) |
| `<prefix> ?`          | print one-line help on stderr, stay attached      |

Exit codes: `0` clean detach, `1` protocol/dial error, `2` argument
error (or `404`/`409` from a remote `attach-raw`), `130` terminated by
SIGINT/SIGTERM/SIGHUP we trapped.

#### Remote attach (`--host`)

`attach --host` connects to a `supervise serve` host instead of dialing
a local `rpc.sock`. The server resolves the short id and bridges the
HTTP/1.1 `Upgrade: supervise-attach/1` straight through to its session's
`rpc.sock` — same `attachwire` protocol, just transported over TCP (or
TLS).

```sh
supervise attach --host m4mini:20000 abc          # bare host:port
supervise attach --host http://m4mini:20000 abc   # explicit scheme
supervise attach --host https://m4mini:443  abc   # TLS
```

`--host` accepts:

- `host:port` (defaults to `http://`)
- `http://host:port` / `https://host[:port]`

Auto-reconnect (default on `--host`, opt out with `--no-reconnect`):

- Backoff: 1s, 2s, 4s, 8s, 16s, 30s — capped at 30s, retries forever.
- A status line on stderr counts down each tier in place: `[supervise:
  reconnecting in 5s — press any key to retry now]`. Pressing any
  non-prefix key wakes the backoff and retries immediately.
- Termios stays raw across drops. On reconnect the client sends a fresh
  `Hello` with the current cols/rows; the server snapshot repaints the
  screen so the disconnect line is overwritten naturally.
- `<prefix>.` detaches cleanly even mid-disconnect.
- A 404 (no session matched) or 409 (ambiguous prefix) on the upgrade
  response surfaces as exit 2 with the server's message.

### `supervise serve`

`serve` is a fan-out HTTP server over the same `--state-dir`. It
exposes a session-keyed REST + WebSocket surface that an external
UI can drive without dialing each session's `rpc.sock` directly
(the browser cannot speak unix-socket).

```sh
supervise serve --bind 127.0.0.1:8080 --state-dir ~/.supervise [--prefix /api]
```

Routes (mounted at the bare path and — if `--prefix` is set — at
`<prefix><path>` too, so the same binary works behind a
`strip_prefix=false` proxy and on direct probing):

| Method | Path                        | Behaviour                                                        |
|--------|-----------------------------|------------------------------------------------------------------|
| GET    | `/sessions`                 | `{sessions: [{...StateFile, alive}]}`                            |
| POST   | `/sessions`                 | `{cmd \| argv, key?}` → forks `supervise __supervise`            |
| GET    | `/sessions/{key}`           | state.json (alias of `/state`)                                  |
| DELETE | `/sessions/{key}`           | SIGTERM the supervisor by pid (204)                              |
| GET    | `/sessions/{key}/state`     | state.json                                                       |
| GET    | `/sessions/{key}/events`    | SSE proxy of upstream `/events`                                  |
| GET    | `/sessions/{key}/attach`    | WebSocket bridge to upstream `/attach` (browser)                 |
| GET    | `/sessions/{key}/attach-raw`| HTTP/1.1 Upgrade pass-through to upstream `/attach` (CLI)        |
| GET    | `/healthz`                  | liveness                                                         |

The WebSocket bridge (`/attach`) speaks browser-friendly framing —
binary frames carry PTY bytes both directions, and a text frame
`{"type":"resize","cols":N,"rows":N}` is translated into an
`attachwire.MsgSize` upstream — and terminates the
`supervise-attach/1` HTTP-Upgrade protocol on the rpc.sock side.

The Upgrade pass-through (`/attach-raw`) is for the CLI: it resolves
`{key}` (full id or unique prefix) server-side, hijacks the client
connection, and `io.Copy`s bytes both ways with no transcoding. The
client speaks the same `supervise-attach/1` framing it uses against a
local `rpc.sock`. 404 on no-match, 409 on ambiguous prefix.

### Example dashboard

`packages/supervisor-webui/` is an example React app that renders
the session list and a live terminal pane against `supervise
serve`. To run it locally with vite + the API behind a single
proxy port, point [`devportv3`](https://github.com/hayeah/devportv3)
at the included `devport.toml`:

```sh
make build           # → bin/supervise
pnpm install
devport up           # foreground; or `--daemon` for background
# proxy URL is printed in the up summary
```

The `@hayeah/supervisor-termui` package extracts the terminal
widget (ghostty-web canvas + AttachStream byte-pipe contract) so
it can be reused in other dashboards.

## Future work (out of scope here)

- A `--render` mode for `attach` that embeds libghostty client-side
  and composites the child's virtual screen into a sub-region of
  the local terminal (so per-attach winsize can diverge from the
  negotiated min).
- Reconnect / resume.
- Remote HTTP access.
