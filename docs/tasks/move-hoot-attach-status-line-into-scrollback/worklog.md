---
status: done
section: Move hoot attach status line into scrollback
slug: move-hoot-attach-status-line-into-scrollback
mode: worktree
spec: spec.md
created: 2026-05-08T03:35:42Z
---

> ## Move hoot attach status line into scrollback
>
> ---
> status:
>   type: open
> ---
>
> The current `prefix-?` status line during attach interferes with more complex TUIs. Drop the live overlay. Instead, print the status line once as ordinary terminal output before the screen snapshot is rendered, so it lands in scrollback — scroll up one line to see it.
>
> Attach sequence:
>
> - Replay scrollback / history (if any).
> - Print the status line as a normal line.
> - Render the screen snapshot.
>
> This removes the redraw fight with TUI apps and removes the live overlay machinery.
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] thread `hud *hudState` + local `rows` into `runSession` / `runServerLoop`; add `(*hudState).Line()` accessor
- [x] in `runServerLoop`, replace `emitConnect` + `\x1b[H\x1b[2J` with HUD-line print + `\x1b[<rows>S\x1b[H` before snapshot payload
- [x] delete `emitConnect`
- [x] drop `chordHelp` from FSM (`chordCmd`, `matchChord`, `runChordFSM`, `fsmActions.help`)
- [x] drop `help` wiring in `runStdinFSM` call site; drop `hud` arg from `runStdinFSM` if unused
- [x] delete `printChord` from `title.go`
- [x] update `cmdAttach` `-h` text and the `runStdinFSM` doc comment
- [x] update `attach_fsm_test.go`: drop `help`, drop `?` case, add `<prefix>?` silent-ignore test
- [x] update `title_test.go`: delete the two `printChord` tests
- [x] update `run_attach_test.go`: replace connect-banner assertion with HUD-scrollback-line assertion
- [x] add a focused `runServerLoop` byte-ordering test (scrollback then screen → HUD + scroll-up + payload)
- [x] update `README.md` chord table + Session HUD subsection
- [x] `go test ./...` green
- [x] smoke: `hoot run --attach -- bash`; eyeball HUD line in scrollback; `<prefix>?` is a no-op; `<prefix>.` detach; reconnect path

## Agent log
- 2026-05-08T03:47Z drop <prefix>? chord + printChord — 44e06e5
- 2026-05-08T04:00Z scrollback HUD print + golden refresh + README — 3821f28
- 2026-05-08T04:02Z all phases done; tests + smoke green; status=done

## Boss log

## Evidence

### Commits on `move-hoot-attach-status-line-into-scrollback`

- `44e06e5` Drop `<prefix>?` HUD chord and on-demand `printChord`
- `3821f28` Print attach HUD line into local scrollback instead of as live overlay

### `go test ./... -count=1` (uncached, repo root)

```
ok  	github.com/hayeah/hootty	6.944s
ok  	github.com/hayeah/hootty/cmd/hoot	6.149s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.494s
ok  	github.com/hayeah/hootty/internal/shortid	0.377s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.671s
```

### New / spec-defining tests (all passing)

- `TestRunChordFSM_QuestionMarkChordRemoved` — `<prefix>?` is now a
  silent no-op (no actions fire, no bytes forwarded), in both legacy
  byte form and CSI u form.
- `TestEmitAttachStatusByteSequence` — locks down the exact byte
  sequence: `CSI<rows>;1H` + `CSI 2K` + dim HUD + `CSI 0m` + `CSI<rows>S` + `CSI H`.
- `TestEmitAttachStatusFallback` — confirms `[connected. K @ H]`
  fallback when `hud.Line()` is empty or `hud` is nil.
- `TestEmitAttachStatusZeroRowsDefaults24` — `localRows=0` (non-tty
  edge) defaults to 24 instead of emitting `CSI 0 S`.
- `TestRunServerLoopSnapshotByteOrdering` — drives the snapshot phase
  end-to-end via in-process attachwire frames and asserts the
  ordering `scrollback bytes < HUD line < scroll-up CSI < snapshot
  payload`.

### `attachetest` golden refresh

The libghostty-backed e2e goldens are the strongest evidence for the
"scroll up one line to see HUD" invariant — they show the local
terminal's actual rendered scrollback + viewport after a real attach.

`internal/attachetest/testdata/attach-substantial-scrollback.golden`
now ends:

```
history-43
<ESC>[0m<ESC>[2m[connected. scrollback @ local]<ESC>[0m
short-visible
<ESC>[0m<ESC>[38;5;5mstyled-ready<ESC>[0m<ESC>[0m<ESC>[2;13H
```

The dim-wrapped HUD line sits exactly one row above the visible
viewport, with the remote scrollback (`history-01..history-43`)
preserved above it. Same shape across all six golden cases (empty
remote scrollback, substantial scrollback, full screen, detach,
reattach boundaries, resize).

### Live smoke transcript

`tmp/110111_651-attach-smoke-stream.bin` — captured byte stream from
running `hoot attach` against a freshly-`hoot run` bash session, with
a synthetic `\x1e.` detach injected after 1s. First ~250 bytes:

```
\x1b[>u                                          (kitty kbd push)
\x1b[24;1H\x1b[2K                                (bottom-left + clear row)
\x1b[2m🦉 ... smokefresh ~/...path... bash -lc ... \x1b[0m
\x1b[24S\x1b[H                                   (scroll viewport, home)
row1\r\nrow2\r\nrow3                             (snapshot payload)
\x1b[0m\x1b[4;1H\x1b[<u                          (cursor + kitty kbd pop)
... detach cleanup escape sequences ...
```

The dim HUD line is emitted on the bottom row, the entire viewport
is scrolled into local scrollback (`CSI 24 S`), and the snapshot
paints into the cleared viewport — matching `TestEmitAttachStatusByteSequence`.

### Manual verification of `-h`

`/tmp/hoot-smoke attach -h` no longer mentions `<prefix> ?` and
documents the new behavior:

```
The same session summary is printed once as ordinary terminal output
just before the screen snapshot is rendered, then the visible viewport
is scrolled into the local terminal's scrollback ring — so a single
scroll-up after attach reveals which session you are in, without a
live overlay that fights TUI redraws.
```

## Trouble report

- **Iteration on the scroll-up sequence.** First attempt wrapped the
  HUD line in leading + trailing CRLFs, then did `CSI <rows> S` to push
  the viewport into scrollback. That worked but added 6+ blank
  scrollback rows before the HUD line on a fresh terminal — the user
  would have had to scroll up 7+ rows to find the HUD. Reworked
  emitAttachStatus to position the cursor at the bottom row first
  (`CSI <rows>;1H`), clear that row (`CSI 2 K`), write HUD bytes, then
  scroll. Now the HUD lands as the bottom-most scrollback entry on
  every attach, regardless of where the cursor was. Captured in
  `spec.md`'s Design notes.
- **`hoot` build artifact in repo root.** Running `go build ./cmd/hoot`
  in the worktree drops a `hoot` binary in the repo root that
  `git status` flags as untracked. Removed before each commit;
  `.gitignore` could be tightened in a follow-up if anyone cares.
  `#friction`
