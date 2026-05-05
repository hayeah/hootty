# Alt-screen attach primary scrollback spec

## Goal

When a client attaches while the child is on an alternate screen, hootty must first populate the client's primary screen with the recorded primary scrollback, then switch the client into the alternate screen and paint the live alternate contents. The fix should preserve the existing live fanout behavior: when the child later exits alternate screen, the client terminal should restore the already-populated primary screen instead of an empty local primary.

Out of scope: changing Ghostty upstream in this task. The source dive below identifies a small upstream API that would be cleaner long-term, but the implementation here should ship in `github.com/hayeah/hootty` without vendoring a libghostty fork.

## Source findings

### go-libghostty exposes only active-screen formatting

- `formatter.go:173-187` in `github.com/mitchellh/go-libghostty` exposes `NewFormatter(t *Terminal, ...)` and calls `ghostty_formatter_terminal_new`; there is no screen selector in the Go options.
- `formatter.go:69-158` exposes formatter extras for terminal/screen state, but none selects primary vs alternate.
- `terminal.go:353-363` defines `ScreenPrimary` and `ScreenAlternate`, and `terminal_data.go:139-146` exposes `ActiveScreen()`, but this is read-only.
- `terminal.go:394-410` exposes `GridRef(point)` against the terminal. `point.go:21-22` says `PointTagScreen` includes scrollback, but this still addresses the active screen through `ghostty_terminal_grid_ref`; using it for rendering would mean reimplementing the formatter.

### libghostty C API also exposes only active-screen formatting

- `include/ghostty/vt/formatter.h:116-135` defines `GhosttyFormatterTerminalOptions`; fields are `emit`, `unwrap`, `trim`, `extra`, and `selection`. No screen selector.
- `include/ghostty/vt/formatter.h:137-155` documents and exports `ghostty_formatter_terminal_new` for a terminal's active screen.
- `include/ghostty/vt/terminal.h:218-232` has `GhosttyTerminalScreen` identifiers, but the public terminal data API only reports active screen; it does not provide a "format this screen" entry point.

### Zig internals reveal the clean upstream API shape

- `src/terminal/formatter.zig:127-136` explicitly says `TerminalFormatter` formats the active screen, and callers that need a specific screen should switch first or use explicit `ScreenFormatter` calls.
- `src/terminal/formatter.zig:337-341` constructs `ScreenFormatter` from `terminal.screens.active`.
- `src/terminal/formatter.zig:423-427` defines `ScreenFormatter` over an arbitrary `*const Screen`.
- `src/terminal/ScreenSet.zig:57-74` has `get` and `getInit` for primary/alternate screens.
- `src/terminal/Terminal.zig:2971-3029` exposes an internal `switchScreen` method, and `src/terminal/Terminal.zig:3041-3108` handles destructive VT mode toggling. Borrowing those semantics through VT writes would mutate the live terminal, so it is not a safe attach snapshot primitive.

Conclusion: the fourth option exists as an upstreamable API, not as a public API today. A small upstream patch could add a screen selector to `GhosttyFormatterTerminalOptions`, plumb it through `src/terminal/c/formatter.zig`, and have `TerminalFormatter` use `terminal.screens.get(screen)` or expose a C `formatter_screen_new`. The task can ship sooner and with less dependency churn by maintaining a primary-only mirror in hootty.

## Architecture

Implement a primary-screen mirror in `pty_libghostty.go`:

- Add `primaryTerm *libghostty.Terminal` to `LibghosttyPTY`.
- Add a streaming `vtPrimaryScreenFilter` that passes bytes while the child is on the primary screen, strips alternate-screen enter/exit sequences, and drops all bytes while the child is on the alternate screen except the exit sequence needed to resume primary passthrough.
- Feed `primaryTerm` from `readLoop` with the filter output after feeding the real terminal.
- Resize and close both terminals together.
- Do not configure `WithWritePty` on `primaryTerm`; the real terminal remains the only query responder.

Replace attach snapshotting with an atomic subscribe+snapshot operation:

- Add `SubscribeWithSnapshot() (<-chan []byte, []byte, func(), error)`.
- The dispatcher action registers the live subscriber and formats the snapshot in the same serialized action, so no live chunk can be both included in the snapshot and later replayed from the subscriber channel.
- Keep `SubscribeAtRecord()` for any existing callers that need the recorder offset.

Snapshot logic:

- If the real terminal active screen is primary, format the real terminal as today.
- If the real terminal active screen is alternate, format `primaryTerm` first, then format the real terminal alternate screen and concatenate:

```text
<primary VT snapshot><alternate VT snapshot>
```

The alternate VT snapshot already includes the active alternate-screen mode when `WithFormatterExtraModes(true)` is used, so no explicit extra `ESC[?1049h` should be inserted. Inserting one manually risks a duplicate 1049h, which can overwrite the client's saved primary cursor.

On-wire shape:

- Keep `attachwire.MsgOutput`.
- Send the combined snapshot as the first output payload after the initial `MsgSize`.
- No new attachwire frame is needed; the terminal byte stream itself has the correct ordering.

Live fanout interaction:

- Existing live fanout already forwards the child's later `ESC[?1049l` / `ESC[?1047l` / `ESC[?47l` exit sequence to attached clients because `vtQueryStripper` preserves non-query mode switches.
- Since the new attach snapshot has already built the client's primary and entered alt, that live exit restores the correct primary scrollback.

## Steps

- Write `spec.md` with source findings and implementation plan.
- Add `vtPrimaryScreenFilter` with focused unit tests for primary passthrough, alt suppression, chunk boundaries, and multi-parameter DECSET/DECRST.
- Add `primaryTerm` lifecycle to `LibghosttyPTY`.
- Add atomic subscribe+snapshot and use it in `attach_handler.go`.
- Add snapshot tests proving alt-screen attach emits primary scrollback before alt content and that replaying the snapshot plus later alt-exit restores primary content.
- Update README comments/docs for the attach scrollback guarantee.
- Run targeted and full Go tests.

## Verification

Commands:

- `go test ./...`
- Targeted tests:
  - `TestVTPrimaryScreenFilter_*`
  - `TestLibghosttyPTYSnapshot_AltScreenIncludesPrimaryBeforeAlternate`
  - `TestAttachHandlerAltScreenSnapshotRestoresPrimaryAfterExit`

Expected behavioral proof:

- Build a hootty terminal with more primary lines than the viewport.
- Enter alternate screen and paint an alt marker.
- Attach snapshot contains primary line markers before the alt marker.
- Feed the attach snapshot into a fresh libghostty terminal and verify the fresh terminal is on alternate screen with alt marker visible.
- Feed `ESC[?1049l` into the fresh terminal and verify primary scrollback lines are visible/restorable there.

## Open questions

None. The hootty-side mirror is the least dependency-heavy shippable route. The upstream `ScreenFormatter` selector remains a cleanup opportunity after this behavior is covered by tests.

## Design notes

- 2026-05-05T04:15Z - Picked hootty-side primary mirror over a libghostty fork for this pass.
  - New option discovered: Ghostty Zig internals already have `ScreenFormatter` for arbitrary `*Screen`, and `ScreenSet.get(.primary)` can access inactive primary state. This is cleaner than a mirror but is not exported through `include/ghostty/vt/formatter.h` or bound by go-libghostty.
  - Upstream patch shape is small but crosses three repositories/layers: Zig formatter options, generated/handwritten C header ABI, and Go binding. That is feasible but higher integration risk than a local mirror.
  - Destructive toggling lost because `Terminal.switchScreenMode(.@"1049", ...)` intentionally saves/restores cursor and clears alternate state; using it during attach would mutate the live emulator.
  - Manual `GridRef` rendering lost because it would recreate the VT formatter poorly.
- 2026-05-05T04:40Z - Implemented the mirror as a second libghostty terminal fed by a narrow alternate-screen filter.
  - The filter strips DECSET/DECRST for modes 47, 1047, and 1049, including split chunks and multi-parameter sequences. It does not try to parse all VT because the mirror only needs to know when primary output pauses and resumes.
  - The mirror terminal has no `WithWritePty` callback. The real terminal remains the only query responder; the mirror only tracks render state for snapshot formatting.
  - `SubscribeWithSnapshot` registers the subscriber and formats the snapshot inside the dispatcher. This replaced the prior subscribe-then-`Snapshot()` sequence so a live chunk cannot slip between the two operations.
  - The live-exit test showed that the first post-alt primary output can overwrite the restored primary cursor row. That is normal terminal behavior; the guarantee is that historical primary scrollback exists before the live exit, not that the current cursor row is immutable.
