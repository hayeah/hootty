# Switch recorder to asciinema JSONL and attach playback

## Goal

Replace the raw PTY byte log with an asciicast v2 JSONL recording that external asciinema tools can consume, and use that recording during attach to replay recent history after the libghostty snapshot and before live bytes. Also remove the vestigial recorder byte-offset API (`Recorder.Size` and the `SubscribeAtRecord` offset return) because snapshot attach no longer uses it. Out of scope: recording user input as `"i"` events, changing the libghostty snapshot semantics, or replacing the existing attach transport.

## Architecture

- `recorder.go`
  - Change `Recorder` from raw byte append to asciicast v2 writer.
  - Open `<state-dir>/<key>/pty.cast` and immediately write a header JSON object with `version`, `width`, `height`, and `timestamp`.
  - Add cheap optional metadata hooks for `command` and `title` through recorder options.
  - `Write([]byte)` writes `[seconds_since_start, "o", chunk_string]`.
  - Add `RecordResize(cols, rows uint16)` writing `[t, "r", "COLSxROWS"]`.
  - Do not record input events for now; the attach client only sends input to the PTY master, and recording user input would need a privacy decision plus different placement than the output tee.
  - Add a small reader for playback events that returns bounded output events from the cast file.
- `cmd/supervise/supervise.go`
  - Create `pty.cast` instead of `pty.log`.
  - Pass initial cols/rows and the supervised command string into the recorder header.
- `pty_libghostty.go`
  - Drop `Recorder.Size`.
  - Change `SubscribeAtRecord()` to return `(<-chan []byte, func())`.
  - Use master’s `SubscribeWithSnapshotParts()` phased attach path.
  - Call `RecordResize` from `Resize` after the kernel/libghostty resize succeeds.
- `internal/attachwire/wire.go`
  - Extend `Hello` with optional JSON fields:
    - `ascii_cinema_playback`: nullable bool; omitted means server default enabled.
    - `ascii_cinema_playback_window_seconds`: float; omitted/invalid means server default.
    - `ascii_cinema_playback_speed`: float; omitted/invalid means server default.
  - No new frame type; playback bytes are ordinary `MsgOutput` frames.
- `attach_handler.go`
  - After `SubscribeWithSnapshotParts`, send `MsgSnapshotScrollback` and `MsgSnapshotScreen` first.
  - If enabled, read bounded output events from `pty.cast`, filter them through a fresh `vtQueryStripper`, and stream them as `MsgOutput` at accelerated timing.
  - Live chunks are already buffered by the subscription. After playback ends, drain and forward `liveCh`; this preserves the existing byte-clean cutover property.
  - Ignore cast `"r"` resize events during attach playback. Attach size is controlled by the current attach set; historical resizes are for asciinema playback tools, not for the live terminal.
- `cmd/supervise/attach.go`
  - Add `--no-ascii-cinema-playback`.
  - Add override flags `--ascii-cinema-playback-window <duration>` and `--ascii-cinema-playback-speed <float>`.
  - Default playback is sent only on the first connection. Remote reconnect sends `ascii_cinema_playback=false` to avoid noisy replay on every reconnect.
- `README.md`
  - Document `pty.cast`, playback flags, defaults, and the fact that input is not recorded.

## Decisions

- Playback bound: default to the last 5 minutes of output events. This makes attach useful for long-lived shells without replaying hours of old output.
- Playback speed: default to 8x with per-gap sleeps capped at 250ms. This preserves recognizable order and rhythm for recent bursts while preventing long idle gaps from blocking live handoff.
- Playback override: `--ascii-cinema-playback-window 0` means full cast; positive durations bound to the last N seconds. `--ascii-cinema-playback-speed` must be positive.
- Wire shape: extend `Hello`. It is already the attach negotiation message, JSON tolerates additive fields, and playback itself can reuse `MsgOutput`.
- Reconnect: playback is first-connect only. Reconnects repaint with the phased libghostty snapshot and go live immediately.
- Query stripping: playback output uses the same `vtQueryStripper` class as live fanout, with independent state for the replay stream.

## Steps

- Remove `Recorder.Size` and the `SubscribeAtRecord` offset return; update tests and comments.
- Implement asciicast v2 recorder output and resize events.
- Switch internal supervisor state file from `pty.log` to `pty.cast`.
- Add cast playback reader and attach-handler playback streaming.
- Add attach CLI flags and Hello fields, with first-connect-only reconnect behavior.
- Update README and focused tests.
- Verify `go test ./...`.
- Verify a real `pty.cast` with `asciinema play`.
- Verify attach playback, no-playback attach, and tmux reattach/query-strip behavior end to end.

## Verification

- `make test` or `go test ./...` passes.
- A real session produces `<state-dir>/<key>/pty.cast` whose first line is an asciicast v2 header and whose output/resize events are JSONL.
- `asciinema play <state-dir>/<key>/pty.cast` accepts the recording and replays it.
- `supervise attach <key>` shows the snapshot, replays recent history, and then forwards a new live marker typed after playback.
- `supervise attach --no-ascii-cinema-playback <key>` skips playback and still forwards live output.
- Reattach to a tmux session after a query-heavy startup; playback bytes and live bytes are stripped of terminal-query probes and live cutover remains clean.

## Open questions

None. The section leaves several design choices open; this spec picks concrete defaults and exposes conservative overrides.

## Design notes

- 2026-05-05T04:42Z — Picked additive `Hello` fields instead of a new attachwire frame.
  - A new frame would give an explicit server acknowledgement surface, but the client only needs to request behavior and the server can stream playback as normal output.
  - Extending `Hello` keeps the protocol compatible with the WebSocket bridge and remote raw bridge because both already forward the same attachwire stream. Older clients that omit the fields get default playback.
  - The nullable bool is intentional: omitted means "server default on", while explicit `false` supports `--no-ascii-cinema-playback` and reconnect suppression.

- 2026-05-05T04:42Z — Picked last-5-minutes at 8x with a 250ms sleep cap for default attach playback.
  - Full-session replay loses badly for long-lived shells: a 12-hour session at original speed is unusable, and even 8x can still stall for long idle periods.
  - As-fast-as-possible burst would preserve byte order but not the "how we got here" feel called out in the task.
  - The chosen default keeps burst ordering visible and bounds idle waits. Users can request full history with `--ascii-cinema-playback-window 0` when they explicitly want it.

- 2026-05-05T04:42Z — Input recording stays out of scope.
  - The current recorder tee is on child output after the PTY master read. User input is written in the opposite direction via attach `MsgInput` and may include passwords or tokens.
  - Asciicast v2 supports `"i"` events, but adding them safely needs a policy and probably a separate opt-in flag. This change records output and resize only.

- 2026-05-05T04:54Z — The tmux e2e replay exposed `CSI ? 996 n`, a private DSR-style query that the existing query stripper did not recognize.
  - Symptom: `pty.cast` contained tmux's normal startup probe burst, playback filtered the known `CSI c`, `CSI > c`, `CSI > q`, OSC color queries, and size queries, but the attach capture still included `ESC[?996n`.
  - Picked fix: extend `paramsAreDSR` to strip any private numeric `CSI ? Pn n` shape, not just `?5` and `?6`. These are query-shaped DEC/private DSR requests; set/reset operations use different finals (`h`/`l`), so the broader match is still conservative.
  - Follow-on: this benefits both asciicast playback and live fanout because both use `vtQueryStripper`.

- 2026-05-05T05:11Z — Rebasing onto master changed attach from one snapshot frame to phased snapshot frames.
  - Master feature preserved: the server now sends `MsgSnapshotScrollback` and `MsgSnapshotScreen`; the client prints the connect banner, writes scrollback, clears the viewport, paints the visible screen, then accepts live output. This avoids stale viewport rows and gives the new golden harness a stable surface.
  - Playback insertion moved from "after the monolithic snapshot" to "after `MsgSnapshotScreen` and before live `MsgOutput`." This matches the boss note and keeps the user-visible order as current screen first, recent history replay second, live cutover third.
  - Alternative considered: stream playback between scrollback and visible-screen. Rejected because playback can contain clears, cursor moves, and alt-screen transitions; putting it before the visible phase would make the visible screen repaint after history and obscure the "how we got here" effect. After visible-screen is explicit and testable.
  - Added a raw attach golden (`attach-asciicast-playback-raw`) because the final terminal screen alone can legitimately end at the same `screen-now` state whether playback happened or not. The raw transcript golden captures the phase order and playback bytes.
