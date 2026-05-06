# hootty

A small Go library + reference CLI that manages a single child process.
The child is spawned on a libghostty-backed PTY; its output is recorded as an
asciicast v2 JSONL file for replay; and the current screen state is exposed through an
embeddable `http.ServeMux` (plain text / HTML / raw VT).

Project home: <https://hootty.dev>.

This is the slimmer cousin of `~/github.com/hayeah/dotfiles/libs/hayeah-go/session`,
extracted so it can stand on its own. The major shift from the dotfiles
version: tmux is no longer a spawn backend. The session opens its own
PTY and runs Ghostty's VT parser against the byte stream
([mitchellh/go-libghostty](https://github.com/mitchellh/go-libghostty)).

## Status

In progress — see `docs/tasks/extract-hootty-library-with-pty-libghostty-drop-tmux-spawn/spec.md`.

## Build

`libghostty-vt` is a Zig library reachable via cgo + pkg-config. Build it
once locally (clone, `make build`), then point this repo at it via
`PKG_CONFIG_PATH`. The `Makefile` defaults to
`$LIBGHOSTTY/build/_deps/ghostty-src/zig-out/share/pkgconfig` where
`$LIBGHOSTTY` is `~/github.com/mitchellh/go-libghostty` by default.

```sh
make build      # → bin/hoot
make test
```

To build a Linux `amd64` binary from macOS, see
[`docs/cross-compile.md`](docs/cross-compile.md).

Prereqs (one-time):

- Zig 0.15.2 (`mise use -g zig@0.15.2` — Zig 0.16 does NOT work)
- CMake
- pkg-config

## Library shape

```go
import session "github.com/hayeah/hootty"

ptyImpl, _ := session.NewLibghosttyPTY(master, cols, rows,
    session.WithRecorder(recorderFile))

run := session.New(session.SessionConfig{
    StateDir: ".state",
    Key:      "demo",
    Service:  myService,   // implements session.Service
    PTY:      ptyImpl,
})
err := run.Run(ctx)        // blocks; serves rpc.sock
```

The `Runner.Mux()` http.ServeMux is the public surface. Mount it standalone
(as `hoot` does over a unix socket) or embed it into a larger Go HTTP
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

## `hoot` CLI

```sh
hoot run     [--remote <url>] [--state-dir <dir>] [--key <key>] [--attach]
                  [--no-reconnect] [--no-ascii-cinema-playback]
                  [--ascii-cinema-playback-window <duration>]
                  [--ascii-cinema-playback-speed <float>]
                  [--prefix-key <key>] -- <cmd> [args...]
hoot list    [--remote <url>] [--state-dir <dir>]
hoot resolve [--remote <url>] [--state-dir <dir>] <id-or-prefix>
hoot clone   [--remote <url>] [--state-dir <dir>] [--key <new-key>]
                  [--env NAME[=VALUE]] [--env-file <path>]
                  [--attach|--detach] <id-or-prefix>
hoot attach  [--remote <url>] [--state-dir <dir>] [--no-reconnect]
                  [--no-ascii-cinema-playback]
                  [--ascii-cinema-playback-window <duration>]
                  [--ascii-cinema-playback-speed <float>]
                  [--prefix-key <key>] <id-or-prefix>
hoot serve   [--state-dir <dir>] --bind <host:port|unix:/path.sock> [--prefix /api]
```

`--state-dir` defaults to `~/.hoot` for every subcommand.

`--remote` targets another host for session-facing subcommands:

- `host:port`, `http://host:port`, `https://host:port` talk to a
  running `hoot serve`.
- `ssh://host`, `ssh://user@host`, `ssh://user@host:2222` shell out to
  OpenSSH, start a per-invocation remote `hoot serve --bind unix:<sock>`,
  forward a local Unix socket to it, and then use the same HTTP API.

The ssh transport inherits the user's OpenSSH config, agent,
ProxyJump, hardware-key, and ControlMaster behavior. `hoot` must be in
the remote login shell's `PATH`; no persistent remote server is required.

`hoot list` emits JSONL — one line per session, each line a
serialized `session.StateFile` (the same shape `<dir>/<key>/state.json`
holds on disk). Pipe through `jq -s` if you want an array.

`run` opens a PTY pair, forks an internal session process (with the
master on fd 3 and stdio = slave), and serves the mux on
`<dir>/<key>/rpc.sock`. The session holds a flock on `<dir>/<key>/`
for the lifetime of the managed child. New state files include
`session.argv` and `session.cwd`; remove old state dirs when upgrading
across this schema change.

Each session directory also contains `pty.cast`, an asciicast v2 JSONL
recording with a header line followed by output (`"o"`) and resize
(`"r"`) events. It is valid input for `asciinema play`, `agg`, and
`asciinema-player`. Input (`"i"`) events are not recorded.

If `--key` is omitted, `hoot` generates a random short id (3–8
chars drawn from `0-9a-z` minus `l` and `o`, collision-checked against
existing sessions). The chosen key is printed on stdout.

Pass `--attach` to `run` to attach the current terminal to the new
session immediately after spawn readiness. Detaching with
`<prefix>.` leaves the session running in the background, the same as
running `hoot run -- ...` followed by `hoot attach <key>`. With
`--remote`, the session is spawned through the remote `hoot serve`
transport and the attach phase uses that same transport. The attach
phase accepts the same playback, prefix-key, and reconnect flags as
`hoot attach`; `--no-reconnect` only affects remote attach.

Anywhere a session key is accepted (including `resolve`), you can pass
either the full id or any unique prefix (minimum 3 characters,
case-insensitive). Ambiguous prefixes fail with an error listing the
matching ids; this is what makes the random ids ergonomic to use.

### `hoot clone`

`clone` spawns a fresh sibling session in the same state dir using the
source session's recorded `session.argv` and `session.cwd`. It does not
fork the running child and does not copy a live PTY; it repeats the
original spawn now.

```sh
hoot clone abc
hoot clone --key work2 abc
hoot clone --attach abc
hoot clone --env FEATURE=on --env-file .env.clone abc
hoot clone --remote ssh://devbox abc
```

The cloned process inherits from the new `hoot __session` environment
at clone time and then goes through the usual child env cleanup
(`TMUX`/`TMUX_PANE` stripped, `TERM=xterm-256color`). `--env NAME=VALUE`
sets a one-shot override for the clone; `--env NAME` copies `NAME` from
the cloning CLI process; `--env-file` loads simple `NAME=VALUE` lines.
Overrides are not persisted to state and are not replayed by
clone-of-clone unless passed again.

Bare `hoot clone` is detached by default and prints the new key on
stdout, matching `hoot run`. `--attach` switches into the cloned
session after creation.

### `hoot attach`

`attach` connects the local terminal to a running session as a
"dumb pipe with two side-channels": stdin → PTY master, master →
stdout, plus a SIGWINCH → resize side-channel. The local termios is
put into raw mode (no echo, no line buffering, no `Ctrl-C` →
local SIGINT) so signal generation happens on the *remote* slave
ldisc, not locally — the user's `Ctrl-C` reaches the managed
child as expected.

Multiple attaches can drive the same session simultaneously. The
PTY winsize is negotiated as `min(cols)`, `min(rows)` across all
connected attaches; the negotiated size is broadcast back to every
attach as a `Size` frame whenever it changes.

Initial replay is phased. The server sends two libghostty-formatted
snapshot frames: `MsgSnapshotScrollback` for history, then
`MsgSnapshotScreen` for the visible viewport plus cursor position,
active SGR style, and non-default modes. The client writes a plain
`[connected. <session> @ <host>]` banner, prints the scrollback into
local history, clears only the local viewport with `ESC[H ESC[2J`,
then paints the visible screen and switches to live `MsgOutput`
frames. This keeps pre-attach local scrollback intact while preventing
stale local viewport rows from bleeding through short remote screens.

If the child is currently on an alternate screen (tmux, vim, less,
etc.), attach currently preserves the legacy full-snapshot fallback in
the visible-screen phase. That builds both screen buffers in the
attaching terminal, so the later live alternate-screen exit restores
the recorded primary scrollback rather than the attach client's
previously empty local primary. The snapshot subscribe pair is
race-free — both happen on the dispatcher goroutine, so live chunks
delivered after subscribe carry no overlap with the snapshot.

On detach, the client locally leaves alt screen defensively, resets
SGR, shows the cursor, clears only the viewport, and prints
`[disconnected. <session> @ <host>]` before exiting. None of these
cleanup bytes are written to the remote PTY or other attached clients.

Asciicast playback is enabled by default on the first attach and is
inserted after the visible-screen phase and before live output. The
default window is the last 5 minutes of output, replayed at 8x with
long idle gaps capped so attach reaches live promptly. Override with
`--ascii-cinema-playback-window <duration>` (`0` = full cast) and
`--ascii-cinema-playback-speed <float>`, or skip it entirely with
`--no-ascii-cinema-playback`. Remote reconnects do not replay the cast
again; they repaint with the phased snapshot and go straight to live.

The previous `--no-full-replay` flag and the alternative `pty.log`
replay arm were removed. Replaying raw `pty.log` re-issued every
terminal-query escape the child ever sent (`DA`, `DSR`, `OSC 11 ?`,
…) which the user's real terminal would dutifully answer back into
the child's stdin — the same root cause as the live "junk chars
after tmux detach" symptom, just spread across the whole session.
The recorder now writes `pty.cast` for offline playback; attach playback
filters its output through the same terminal-query stripper used by live
fanout before bytes reach the user's real terminal.

Live fanout strips terminal-query escape sequences before sending
to the user's real terminal (the libghostty emulator on the
session already auto-replies, so a second answer from the user's
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
| `<prefix> Ctrl-Z`     | suspend `hoot attach` (SIGTSTP; `fg` resumes) |
| `<prefix> c`          | clone current session and attach to the clone     |
| `<prefix> ?`          | print one-line help on stderr, stay attached      |

Exit codes: `0` clean detach, `1` protocol/dial error, `2` argument
error (or `404`/`409` from a remote `attach-raw`), `130` terminated by
SIGINT/SIGTERM/SIGHUP we trapped.

#### Remote sessions (`--remote`)

With `--remote http://host:port`, `list`, `resolve`, `run`, and
`attach` talk to a running `hoot serve` instead of the local state dir.
Bare `host:port` is accepted as sugar for `http://host:port`.

With `--remote ssh://host`, the local CLI starts a short-lived remote
`hoot serve` over OpenSSH and forwards a local Unix socket to it. The
remote only needs `hoot` in `PATH`; the user does not start `hoot serve`
by hand.

```sh
hoot list --remote ssh://devbox
hoot run --remote ssh://devbox -- bash
hoot resolve --remote ssh://devbox abc
hoot attach --remote ssh://devbox abc

hoot attach --remote m4mini:20000 abc          # bare host:port
hoot attach --remote http://m4mini:20000 abc   # explicit scheme
hoot attach --remote https://m4mini:443  abc   # TLS
```

Auto-reconnect (default on `--remote` attach, opt out with
`--no-reconnect`):

- Backoff: 1s, 2s, 4s, 8s, 16s, 30s — capped at 30s, retries forever.
- A status line on stderr counts down each tier in place: `[hoot:
  reconnecting in 5s — press any key to retry now]`. Pressing any
  non-prefix key wakes the backoff and retries immediately.
- Termios stays raw across drops. On reconnect the client sends a fresh
  `Hello` with the current cols/rows; the server snapshot repaints the
  screen so the disconnect line is overwritten naturally.
- `<prefix>.` detaches cleanly even mid-disconnect.
- A 404 (no session matched) or 409 (ambiguous prefix) on the upgrade
  response surfaces as exit 2 with the server's message.

### `hoot serve`

`serve` is a fan-out HTTP server over the same `--state-dir`. It
exposes a session-keyed REST + WebSocket surface that an external
UI can drive without dialing each session's `rpc.sock` directly
(the browser cannot speak unix-socket).

```sh
hoot serve --bind 127.0.0.1:8080 --state-dir ~/.hoot [--prefix /api]
hoot serve --bind unix:$HOME/.hoot/api.sock --state-dir ~/.hoot
```

Routes (mounted at the bare path and — if `--prefix` is set — at
`<prefix><path>` too, so the same binary works behind a
`strip_prefix=false` proxy and on direct probing):

| Method | Path                        | Behaviour                                                        |
|--------|-----------------------------|------------------------------------------------------------------|
| GET    | `/sessions`                 | `{sessions: [{...StateFile, alive}]}`                            |
| POST   | `/sessions`                 | `{cmd \| argv, key?}` → forks `hoot __session`            |
| GET    | `/sessions/{key}`           | state.json (alias of `/state`)                                  |
| POST   | `/sessions/{key}/clone`     | `{key?, env?}` → forks a sibling from `session.argv`/`cwd`       |
| GET    | `/sessions/{key}/resolve`   | resolve full id or unique prefix                                |
| DELETE | `/sessions/{key}`           | SIGTERM the session by pid (204)                              |
| GET    | `/sessions/{key}/state`     | state.json                                                       |
| GET    | `/sessions/{key}/events`    | SSE proxy of upstream `/events`                                  |
| GET    | `/sessions/{key}/attach`    | WebSocket bridge to upstream `/attach` (browser)                 |
| GET    | `/sessions/{key}/attach-raw`| HTTP/1.1 Upgrade pass-through to upstream `/attach` (CLI)        |
| GET    | `/healthz`                  | liveness                                                         |

The WebSocket bridge (`/attach`) speaks browser-friendly framing —
binary frames carry PTY bytes both directions, and a text frame
`{"type":"resize","cols":N,"rows":N}` is translated into an
`attachwire.MsgSize` upstream — and terminates the
`hoot-attach/1` HTTP-Upgrade protocol on the rpc.sock side.

The Upgrade pass-through (`/attach-raw`) is for the CLI: it resolves
`{key}` (full id or unique prefix) server-side, hijacks the client
connection, and `io.Copy`s bytes both ways with no transcoding. The
client speaks the same `hoot-attach/1` framing it uses against a
local `rpc.sock`. 404 on no-match, 409 on ambiguous prefix.

### Example dashboard

`packages/hootty-webui/` is an example React app that renders
the session list and a live terminal pane against `hoot
serve`. To run it locally with vite + the API behind a single
proxy port, point [`devportv3`](https://github.com/hayeah/devportv3)
at the included `devport.toml`:

```sh
make build           # → bin/hoot
pnpm install
devport up           # foreground; or `--daemon` for background
# proxy URL is printed in the up summary
```

The `@hayeah/hootty-termui` package extracts the terminal
widget (ghostty-web canvas + AttachStream byte-pipe contract) so
it can be reused in other dashboards.

## Future work (out of scope here)

- A `--render` mode for `attach` that embeds libghostty client-side
  and composites the child's virtual screen into a sub-region of
  the local terminal (so per-attach winsize can diverge from the
  negotiated min).
