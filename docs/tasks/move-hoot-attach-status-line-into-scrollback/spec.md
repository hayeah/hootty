# spec — move hoot attach status line into scrollback

## Goal

Drop the on-demand `<prefix>?` HUD chord and replace the connect banner
with a single status line that prints into the local terminal's
scrollback at attach time, between the scrollback replay and the screen
snapshot. This removes a chord that fights with TUIs that bind `?` (or
that interleave their own redraws), and gives every attach a stable
"who am I attached to?" marker the user can find by scrolling up one
line.

Out of scope: changing the wire protocol, changing what
`MsgSnapshotScrollback` / `MsgSnapshotScreen` carry, touching the
terminal-title push/set/pop (separate channel, not a screen overlay),
the reconnect-countdown line in `waitBackoff` (only shown after a drop,
not an attach overlay).

## Architecture

Files to touch (all under `cmd/hoot/`):

- `attach.go`
  - `runAttachLoop` — pass `*hudState` through to `runSession` /
    `runServerLoop` (it already does, just reuse the existing arg).
  - `runStdinFSM` / `runChordFSM` call site — drop the `help` action
    wiring; drop the `hud` parameter from `runStdinFSM` once it is
    only used for the help chord.
  - `runServerLoop` — accept `hud *hudState` and `rows uint16`. Replace
    the `emitConnect` connect banner with a one-shot HUD-scrollback
    print sequenced as: scrollback replay → HUD line → screen snapshot.
    Replace `\x1b[H\x1b[2J` with `\x1b[<rows>S\x1b[H` so the visible
    viewport (now containing scrollback tail + the freshly-emitted HUD
    line) scrolls up into the local terminal's scrollback ring before
    the snapshot paints. This is the load-bearing change that makes
    the HUD line "land in scrollback."
  - Delete `emitConnect`.
  - `-h` (`fs.Usage`) — drop the `<prefix>?` row from the chord
    documentation; mention the new attach-time scrollback line.

- `attach_fsm.go`
  - Remove `chordHelp` from the `chordCmd` enum.
  - Remove the `?` (legacy) and `cp=63` (CSI u) cases from
    `matchChord`.
  - Remove `help` from `fsmActions`.
  - Remove the `chordHelp` case in `runChordFSM`.

- `title.go`
  - Remove `printChord` (no caller).
  - Keep `hudState`, `onAttach`, `onDetach`, the title push/set/pop,
    and `hudTitleSetBytes` / `sanitizeTitle` — terminal-title is a
    separate concern, untouched by this section. Add a thin
    `hudState.line` accessor (or an exported helper) so
    `runServerLoop` can read the HUD text.

- `attach_fsm_test.go`
  - Drop the `help` field from the `fsmActions` test recorder.
  - Drop the `help via '?'` case from
    `TestRunChordFSM_OtherChordsCSIu`.
  - Add: `<prefix> ?` is now an unknown chord — bytes are silently
    consumed (no detach, no help, no forward).

- `title_test.go`
  - Remove `TestHUDStatePrintChord` and `TestHUDStatePrintChordEmpty`.

- `run_attach_test.go`
  - Replace the "connect banner" assertion (`[connected. abc123 @ ...]`)
    with one that the HUD scrollback line is emitted with the right
    shape (HUD text + the scroll-up CSI sequence) before the snapshot
    payload.

- `README.md`
  - Drop the `<prefix> ?` row from the chord table.
  - Rewrite the "Session HUD" subsection: terminal title still set on
    attach; the status line now prints once at attach time as a normal
    scrollback line (not a chord).

## Steps

1. Decide and document the scroll-up sequence (`\x1b[<rows>S` + `\x1b[H`)
   in `runServerLoop`'s comment; pass `rows` from `runSession`.
2. Plumb the HUD line into `runServerLoop`: thread `hud *hudState`
   through `runSession`. Add `func (h *hudState) Line() string`
   accessor on `hudState`.
3. Replace `emitConnect` with HUD scrollback emit. Order inside
   `runServerLoop`:
   - First `MsgSnapshotScrollback` arrives → write payload (bytes
     scroll naturally; cursor walks down).
   - First `MsgSnapshotScreen` arrives → emit HUD line (`\r\n` + dim
     SGR + line + `\x1b[0m\r\n`) → emit `\x1b[<rows>S\x1b[H` (push the
     visible viewport into scrollback, home cursor) → write payload.
   - Subsequent `MsgSnapshotScreen` (reconnect) — same shape.
   - The HUD line is omitted when stdout is not a tty and when the
     hud line is empty (best-effort `state.json` miss).
4. Drop `<prefix>?` chord wiring.
   - `attach_fsm.go`: enum + `matchChord` + `runChordFSM` switch +
     `fsmActions.help`.
   - `attach.go`: drop the `help` field passed into `fsmActions`; drop
     `hud` from `runStdinFSM`'s signature if no other use remains.
   - Delete `printChord` in `title.go`.
5. Update `-h` text (`fs.Usage` in `cmdAttach`) and the chord-vocabulary
   block at the top of `runStdinFSM`.
6. Update `README.md` chord table + Session HUD subsection.
7. Update tests:
   - `attach_fsm_test.go`: drop `help` from `fsmRecord`; drop the
     `?` case from `TestRunChordFSM_OtherChordsCSIu`; add a
     "`<prefix> ?` is silent ignore" test (no actions, no forwarded
     bytes).
   - `title_test.go`: delete the two `printChord` tests.
   - `run_attach_test.go`: replace the connect-banner assertion with
     a HUD-scrollback-line assertion that exercises both the HUD bytes
     and the scroll-up CSI.
   - Add a unit-level `runServerLoop` test (or extend the existing
     clone test) that drives in `MsgSnapshotScrollback` then
     `MsgSnapshotScreen` and asserts the byte ordering on stdout.
8. `go test ./...` green.
9. Smoke: `hoot run --attach -- bash`, confirm visually that the HUD
   line is one row above the snapshot in scrollback after attach;
   detach with `<prefix>.`; reattach to confirm reconnect path.

## Verification

- `go test ./cmd/hoot/...` and `go test ./...` green.
- `hoot attach <id>` shows the HUD line one row above the snapshot
  (visible by scrolling up one in the local terminal).
- `<prefix>?` is a no-op (bytes consumed, nothing prints).
- Title push/set/pop unchanged — confirm by attaching, eyeballing the
  window title, detaching, eyeballing the title restore.

## Open questions

None. Section is unambiguous; I'm using the existing `hudState`
machinery rather than re-inventing the line constructor.

## Design notes

- 2026-05-08T03:55Z — Picked `\x1b[<rows>S\x1b[H` over the existing
  `\x1b[H\x1b[2J` for the screen-snapshot prelude.
  - Alternatives considered:
    - **Keep `\x1b[H\x1b[2J`** and just print the HUD line before it.
      `\x1b[2J` does not push visible content into scrollback in
      modern terminals (xterm, iTerm2, Ghostty, kitty) — it only
      clears the visible viewport. So the freshly-printed HUD line
      would be wiped from the terminal entirely, defeating the
      "scroll up one line to see it" requirement. Loser.
    - **Print enough trailing newlines after the HUD line to scroll
      it past the top of the viewport before the clear.** Works only
      when the cursor happens to be at the bottom; on a fresh
      terminal where `hoot attach` ran near the top of the viewport,
      the newlines just walk the cursor down without scrolling, and
      `\x1b[2J` still wipes the HUD. Position-dependent — fragile.
    - **`\x1b[<rows>S\x1b[H`** (picked). `CSI Pn S` (Scroll Up by Pn)
      shifts the entire scrolling region up by Pn lines, and on
      xterm-compatible terminals the lines that scroll off the top
      are appended to the local scrollback. Using `<rows>` =
      local terminal rows guarantees the entire visible viewport
      (including the HUD line we just printed) is pushed into local
      scrollback. Cursor position doesn't matter. The follow-up
      `\x1b[H` homes the cursor for the snapshot paint. The snapshot
      payload itself is a full-viewport repaint via
      `formatTerminalSnapshot` so no `\x1b[2J` is needed afterward.
    - Trade-off: `<rows>` here is the *local* terminal rows, not the
      remote PTY rows. We pull it from the same `localOrDefaultSize()`
      that already drives the SIGWINCH watcher, so it is correct
      even when the remote PTY is min-wins-resized below the local
      terminal.
  - Follow-on: if a future terminal is encountered where `CSI S`
    doesn't push to scrollback, we'd see the HUD wipe symptom and
    can fall back to a tmux-style alt-screen approach. Today's
    target terminals (xterm, iTerm2, Ghostty, kitty, Wezterm,
    Alacritty) all implement `CSI S` correctly.

- 2026-05-08T03:55Z — Kept the terminal title push/set/pop machinery
  in `title.go` even though the section says "removes the live
  overlay machinery."
  - Reason: the title is set on a separate logical channel (the
    window's title bar, OSC 2), it is not a "live overlay" on the
    terminal viewport, and it does not interfere with TUI redraws.
    The section text consistently references `<prefix> ?` and "the
    redraw fight with TUI apps" — both about viewport overlays. The
    title is unaffected.
  - If the human disagrees on review, the title machinery can be
    ripped out in a follow-up: delete `hudTitlePush`/`Set`/`Pop`,
    `onAttach`, `onDetach`, and the title-stack tests; the HUD-line
    constructor (`sessionpick.FormatHUD`) stays because the
    scrollback line still needs it.
