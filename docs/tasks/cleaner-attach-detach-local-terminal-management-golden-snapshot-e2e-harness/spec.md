# Cleaner Attach/Detach Local Terminal Management

## Goal

Make `hoot attach` paint local terminals in clear phases so stale local viewport content cannot bleed through remote snapshots. The server will split libghostty snapshots into scrollback and visible-screen protocol frames; the client will bracket attach/detach with plain banners and local-only cleanup. This section also ships a reusable libghostty-backed golden snapshot harness for the attach UX. Alt-screen behavior is not expanded here beyond preserving the existing full-snapshot fallback.

## Architecture

- `pty_libghostty.go`
  - Add `SnapshotParts() (scrollback, screen []byte, err error)`.
  - Add `SubscribeWithSnapshotParts()` mirroring `SubscribeWithSnapshot()` atomicity.
  - Main-screen split: format the active terminal as VT, use `Terminal.ScrollbackRows()` as the number of leading formatted rows that belong to history, and split on formatted row delimiters. The remaining bytes are visible-screen replay, including cursor/style/mode restore.
  - Keep `Snapshot()` and `SubscribeWithSnapshot()` for compatibility with existing tests and alt-screen behavior.
- `internal/attachwire/wire.go`
  - Add `MsgSnapshotScrollback` and `MsgSnapshotScreen`.
  - Update protocol comments and frame validation expectations.
- `attach_handler.go`
  - After initial `Size`, call `SubscribeWithSnapshotParts()`.
  - Always send `MsgSnapshotScrollback`, then `MsgSnapshotScreen`, even when a part is empty. Empty scrollback still tells the client the attach paint sequence has begun.
  - Continue live PTY bytes as `MsgOutput`.
- `cmd/hoot/attach.go`
  - Thread an attach label through the loop: local uses resolved session key and `local`; remote uses the user-provided session argument and host flag.
  - On snapshot scrollback: emit `[connected. <session> @ <host>]`, then the scrollback bytes.
  - On snapshot screen: emit `ESC[H ESC[2J`, then visible-screen bytes.
  - On process exit: emit `ESC[?1049l ESC[0m ESC[?25h ESC[H ESC[2J`, then `[disconnected. <session> @ <host>]`.
  - Existing transient reconnect status lines remain stderr-only and do not produce disconnect banners unless the attach process exits.
- `internal/attachetest/`
  - Reusable package for attach UX tests.
  - Use libghostty for both fixture terminals and the local client terminal.
  - Drive an in-process attach server over a real PTY pair, feed remote fixture bytes through `LibghosttyPTY`, pipe client output into a local libghostty terminal, and capture textual snapshots.
  - Golden files live under `internal/attachetest/testdata/`; `UPDATE_GOLDENS=1` rewrites them.

## Steps

- Investigate existing attach snapshot/protocol/client paths and write this spec.
- Implement snapshot splitting and atomic snapshot-parts subscription.
- Add attachwire frame types and server-side snapshot-parts transmission.
- Implement phased client attach paint and local detach cleanup banners.
- Build `internal/attachetest` helpers for libghostty local-terminal golden snapshots.
- Add golden scenarios:
  - empty remote scrollback with fresh visible screen
  - substantial remote scrollback with short visible screen
  - full visible screen plus scrollback
  - detach clears viewport while preserving scrollback and disconnect banner
  - detach then reattach has scannable session boundaries
  - resize between detach and reattach snapshots at the negotiated size
- Refresh checked-in goldens with `UPDATE_GOLDENS=1`.
- Update README attach protocol/UX docs.
- Verify `UPDATE_GOLDENS=1 go test ./...` and bare `go test ./...`.
- Capture a manual smoke transcript demonstrating the attach/detach UX.

## Verification

- `UPDATE_GOLDENS=1 go test ./...` regenerates golden files cleanly.
- `go test ./...` passes against checked-in goldens.
- Golden files under `internal/attachetest/testdata/` cover at least the six named scenarios.
- Manual smoke transcript saved under workspace `tmp/` and summarized in `worklog.md`.

## Open Questions

None for the boss before implementation.

Defaults picked from the upstream spec:

- Connect banner appears before scrollback. This makes the boundary between pre-attach local history and remote attach history visible when scrolling back.
- Detach emits `ESC[?1049l` defensively before reset/clear. The current client already had an alt-screen escape in its termios cleanup path; preserving that behavior prevents a bad remote state from trapping the user in alt screen, and the sequence is harmless on main screen.

## Design Notes

- 2026-05-05T04:48Z - `FormatterFormatVT` does not expose separate history/screen outputs, but it does emit formatted terminal rows in order and `Terminal.ScrollbackRows()` reports the history row count.
  - Probe used a 20x5 libghostty terminal with 10 styled lines and a cursor move. `ScrollbackRows()` returned 6 and `FormatVT` emitted leading CRLF-delimited rows for history followed by the nonblank visible rows and final cursor restore.
  - Chosen split: count `\r\n` row delimiters through the reported scrollback row count and split the VT byte stream there. The screen part keeps the formatter's final cursor/style/mode restoration.
  - Alternative considered: replay full snapshot into a no-scrollback terminal and re-format to get screen-only bytes. This gives a robust screen snapshot but still does not produce scrollback-only bytes, so it cannot satisfy the phased attach protocol by itself.
  - Alternative considered: use `GridRef` row/cell APIs to synthesize VT manually. That would make row boundaries explicit, but it would reimplement formatting for style, wide cells, graphemes, cursor style, and modes. Reusing libghostty's formatter keeps the high-risk terminal rendering logic in the terminal library.
