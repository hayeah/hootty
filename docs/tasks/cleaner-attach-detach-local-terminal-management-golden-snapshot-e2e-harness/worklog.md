---
status: done
section: Cleaner attach/detach local terminal management + golden-snapshot e2e harness
slug: cleaner-attach-detach-local-terminal-management-golden-snapshot-e2e-harness
mode: worktree
spec: spec.md
created: 2026-05-05T04:47:01Z
---

> ## Cleaner attach/detach local terminal management + golden-snapshot e2e harness
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/supervisor`.
>
> Implement the spec at `/Users/me/Dropbox/notes/2026-05-05/attach-detach-local-terminal-management_claude.md`. Read it end-to-end first.
>
> The spec covers:
> - Splitting `Snapshot()` into `SnapshotParts() (scrollback, screen []byte, err error)` so the client can paint scrollback into history first, then clear viewport, then paint visible screen — preventing local viewport bleed-through.
> - Connect/disconnect banners (`[connected. <session> @ <host>]` / `[disconnected. ...]`).
> - Phased attach paint: banner → scrollback → `ESC[H ESC[2J` → visible-screen + cursor restore → live frames.
> - Phased detach: `ESC[0m ESC[?25h` → clear viewport → disconnect banner → exit.
> - Wire-protocol option A (recommended): two new frame types `MsgSnapshotScrollback` + `MsgSnapshotScreen`.
> - Two open questions in the spec — pick reasonable defaults and justify them in spec.md before implementing.
>
> ### Testing convention — design and ship it as part of this section
>
> This UX dance is subtle; the testing harness is part of the deliverable. **Use ghostty itself (libghostty) to emulate both the remote child's terminal and the local client's terminal**, then golden-snapshot the local terminal state at key moments.
>
> Sketch:
>
> - A small Go test harness that spins up:
>   - a libghostty terminal acting as the **remote child** (write fixture bytes into it: scrollback + visible content + cursor position + SGR state)
>   - a `LibghosttyPTY` wrapping a real PTY pair, plumbed through the supervisor server
>   - a **local libghostty** that consumes attach client output, simulating the user's real terminal
> - Drive an end-to-end attach → some live activity → detach cycle through the supervisor + attach client, then snapshot the local libghostty's state (scrollback ring + visible screen + cursor + style) at well-defined points: after attach paint, after some live updates, after detach clear.
> - Compare snapshots against checked-in golden files. Env flag (e.g. `UPDATE_GOLDENS=1`) regenerates them; CI / normal `go test` reads them.
> - Goldens are textual: dump `FormatVT` of the local terminal at the snapshot point, plus a small header (cols×rows, cursor row/col, active SGR, active screen). Git-diffable.
>
> Concrete scenarios to cover (each its own golden):
> - Attach with empty remote scrollback, fresh visible screen.
> - Attach with substantial remote scrollback (~50 lines), short visible screen.
> - Attach with full visible screen (rows × cols of remote content) and scrollback.
> - Detach — verify viewport cleared, scrollback intact, disconnect banner present.
> - Reattach after detach — verify boundaries between sessions are scannable in scrollback.
> - Resize between detach and reattach (rows shrink / grow): visible-portion paints against the negotiated size.
>
> Land the harness in a sub-package (e.g. `internal/attachetest/`) so it's reusable for future attach-protocol changes.
>
> - [ ] investigate, write spec.md (design decisions + harness sketch), then implement and verify end-to-end
>   - evidence: golden files committed under `internal/attachetest/testdata/` (or wherever you land it)
>   - evidence: `UPDATE_GOLDENS=1 go test ./...` regenerates cleanly; bare `go test ./...` passes against checked-in goldens
>   - evidence: at least the six scenarios above covered
>   - evidence: a manual smoke transcript demonstrating the actual attach/detach UX matches what the goldens encode

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Check out `hayeah/supervisor`, read the upstream attach/detach spec, and write workspace `spec.md`
- [x] Implement `SnapshotParts()` / atomic snapshot-parts subscription in `LibghosttyPTY`
- [x] Add attachwire snapshot-part frame types and server-side transmission
- [x] Implement phased client attach paint plus detach cleanup banners
- [x] Build reusable `internal/attachetest` libghostty golden harness
- [x] Add and check in goldens for the six required attach/detach scenarios
- [x] Update README attach protocol/UX documentation
- [x] Run `UPDATE_GOLDENS=1 go test ./...` and bare `go test ./...`
- [x] Capture a manual attach/detach smoke transcript

## Agent log
- 2026-05-05T04:48Z Checked out `github.com/hayeah/supervisor` into worktree slot 001, read the upstream spec, probed libghostty formatter row output, and wrote `spec.md`.
- 2026-05-05T04:52Z core attach snapshot phases landed in supervisor commit 338c132; go test ./... passes
- 2026-05-05T04:59Z golden attach harness, README docs, update-goldens test, bare test, and smoke transcript landed in supervisor commit 32c1daf

## Boss log

## Evidence

Commits on `github.com/hayeah/supervisor` branch `cleaner-attach-detach-local-terminal-management-golden-snapshot-e2e-harness`:

- `338c132` Split attach snapshots into paint phases
- `32c1daf` Add attach golden snapshot harness

Checked-in goldens:

- `internal/attachetest/testdata/attach-empty-scrollback.golden`
- `internal/attachetest/testdata/attach-substantial-scrollback.golden`
- `internal/attachetest/testdata/attach-full-visible-screen.golden`
- `internal/attachetest/testdata/detach-clears-viewport.golden`
- `internal/attachetest/testdata/reattach-boundaries.golden`
- `internal/attachetest/testdata/resize-reattach.golden`

Verification commands:

```text
$ UPDATE_GOLDENS=1 go test ./...
ok  	github.com/hayeah/supervisor	(cached)
ok  	github.com/hayeah/supervisor/cmd/supervise	0.422s
?   	github.com/hayeah/supervisor/internal/attachetest	[no test files]
?   	github.com/hayeah/supervisor/internal/attachwire	[no test files]
ok  	github.com/hayeah/supervisor/internal/shortid	(cached)

$ go test ./...
ok  	github.com/hayeah/supervisor	(cached)
ok  	github.com/hayeah/supervisor/cmd/supervise	0.409s
?   	github.com/hayeah/supervisor/internal/attachetest	[no test files]
?   	github.com/hayeah/supervisor/internal/attachwire	[no test files]
ok  	github.com/hayeah/supervisor/internal/shortid	(cached)
```

Manual smoke transcript:

- `tmp/attach-smoke.txt`
- Shows actual `supervise run` + `supervise attach` CLI flow with connect banner, remote scrollback, viewport clear before visible screen, detach cleanup, and disconnect banner.

## Trouble report

- `FormatterFormatVT` has no first-class split API; the implementation will split its row-ordered VT output using `ScrollbackRows()`. This is documented in `spec.md` design notes.
- The connect banner is intentionally moved into local scrollback by the later viewport clear, so the e2e tests synchronize on the production client's attached flag rather than looking for the banner in visible plain text.
