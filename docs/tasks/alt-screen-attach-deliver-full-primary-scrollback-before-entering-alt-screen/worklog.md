---
status: done
section: Alt-screen attach: deliver full primary scrollback before entering alt screen
slug: alt-screen-attach-deliver-full-primary-scrollback-before-entering-alt-screen
mode: worktree
spec: spec.md
created: 2026-05-05T03:47:55Z
---

> ## Alt-screen attach: deliver full primary scrollback before entering alt screen
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/supervisor`.
>
> When a client attaches while the child is on alt screen (tmux, vim, less, etc.), today only the alt-screen content is sent. The primary scrollback is preserved internally but never reaches the client — when the child later exits alt screen, the user's terminal restores its own (empty) primary screen and the recorded primary history is gone.
>
> **Goal:** on attach, deliver the full primary scrollback first, then enter alt screen and continue live. The user gets correct scrollback behavior regardless of which screen is active when they attach.
>
> **Required reading**:
> - `/Users/me/Dropbox/notes/2026-05-02/supervise-attach-scrollback-spec_claude.md` — recently-shipped scrollback work; the "Known limitation: alt-screen attach loses primary scrollback" section explicitly punts this fix.
> - `/Users/me/Dropbox/notes/2026-05-02/supervise-alt-screen-scrollback-gap_claude.md` — full analysis with Option A/B/C tradeoffs (parallel emulator, destructive toggle, format-while-alt API).
>
> **Mandate**: really try to find a fourth option before falling back to A/B/C. Specifically:
>
> - Read `go-libghostty`'s Go wrapper end-to-end. Look for any Terminal/Formatter API that takes a screen selector, even one that's plumbed but not surfaced in the doc-comments.
> - Read libghostty's Zig source (cloned at `~/github.com/ghostty-org/ghostty`). The C/Zig API may already expose a "format inactive screen" entry point that the Go wrapper just doesn't bind. The spec author's claim that the formatter has "no inactive-screen selector" was based on a doc read, not a Zig source dive.
> - Check the underlying `Screen` / `Terminal` types in `src/terminal/` for primary/alternate accessors. If the formatter takes a `*Screen` (vs `*Terminal`), wrapping the primary screen directly would be a clean route. If it only takes `*Terminal`, look at how alt-screen toggling is implemented — there may be a save/restore pair we can borrow non-destructively (e.g. via a brief read-locked formatter call against a snapshot).
> - If nothing in libghostty is exposable, evaluate whether a small upstream patch (Zig → C API → Go binding) is feasible. Personal-project — vendoring a fork of go-libghostty or libghostty is on the table.
>
> The agent should write `spec.md` covering:
> - What the libghostty/go-libghostty source actually exposes today (concrete file:line citations).
> - The recommended approach with rationale, including any new option discovered.
> - The on-wire shape: how does the client consume "primary scrollback first, then alt screen"? Two snapshots in sequence? A new attachwire frame? Spec calls this out so the implementation is unambiguous.
> - How does this interact with the live fanout? When the child later toggles alt off, the existing live stream already restores primary — verify this still works.
> - Verification plan: how do we confirm primary scrollback survives an alt-screen attach? Concrete tests, not just "looks right".
>
> - [ ] rfc: review spec.md
> - [ ] implement per spec

## Todos
- [x] check out and inspect `supervisor`, `go-libghostty`, and Ghostty source
- [x] read required notes and write `spec.md`
- [x] add primary-screen filter with unit tests — 6333014
- [x] add primary mirror terminal lifecycle to `LibghosttyPTY` — d508dc3
- [x] make attach subscribe+snapshot atomic — d508dc3
- [x] add alt-screen attach snapshot/restore tests — bd18049
- [x] update README/docs for attach scrollback behavior — 7fb6153
- [x] run verification and commit

## Agent log
- 2026-05-05T04:15Z Read AGENT_LOOP, required notes, supervisor attach/PTY code, go-libghostty formatter/terminal wrappers, and Ghostty Zig formatter/screen internals. Wrote spec.md and proceeding end-to-end per boss note (no RFC stop).
- 2026-05-05T04:24Z Added primary-screen VT filter and tests for primary passthrough, alt suppression, chunk boundaries, legacy alt modes, and multi-param DECSET/DECRST. Commit 6333014.
- 2026-05-05T04:29Z Wired primary-only libghostty terminal into PTY lifecycle, resized/closed it with the real terminal, and added SubscribeWithSnapshot so attach snapshots and live subscription registration are serialized. Commit d508dc3.
- 2026-05-05T04:35Z Added replay tests proving alt-screen snapshots prefix primary scrollback before alt content and that a live alt-exit restores a populated client primary. Commit bd18049.
- 2026-05-05T04:38Z Updated README attach docs to describe primary-first alt-screen snapshot ordering and why live alt exit restores the recorded primary. Commit 7fb6153.
- 2026-05-05T04:42Z Verification passed. Full suite `go test ./...`; targeted behavior suite covers primary filter, primary-before-alt snapshot ordering, replay into a fresh terminal, and live alt-exit restoration. Status done.

## Boss log
- 2026-05-05T03:48Z no rfc gate — solve it end-to-end. write spec.md as your design doc (so the human can read your reasoning afterward), but don't park waiting for review. once the spec.md design feels right to you, implement it, verify, and ship. the human will review the whole landed package at lgtm time.
  
  the boss.md section has been updated to a single "investigate, write spec.md, then implement and verify end-to-end" box. proceed.

## Evidence

Repo: `repos/github.com/hayeah/supervisor`

Commits:

- `6333014` Add primary screen VT filter
- `d508dc3` Mirror primary screen for attach snapshots
- `bd18049` Test alt-screen attach snapshots
- `7fb6153` Document alt-screen attach replay

Full suite:

```text
$ go test ./...
ok  	github.com/hayeah/supervisor	0.573s
ok  	github.com/hayeah/supervisor/cmd/supervise	0.263s
?   	github.com/hayeah/supervisor/internal/attachwire	[no test files]
ok  	github.com/hayeah/supervisor/internal/shortid	(cached)
```

Targeted behavior suite:

```text
$ go test -run 'TestVTPrimaryScreenFilter|TestLibghosttyPTYSnapshot_AltScreenIncludesPrimaryBeforeAlternate|TestLibghosttyPTYSubscribeWithSnapshot_LiveAltExitRestoresPrimary' -v .
=== RUN   TestLibghosttyPTYSnapshot_AltScreenIncludesPrimaryBeforeAlternate
--- PASS: TestLibghosttyPTYSnapshot_AltScreenIncludesPrimaryBeforeAlternate (0.01s)
=== RUN   TestLibghosttyPTYSubscribeWithSnapshot_LiveAltExitRestoresPrimary
--- PASS: TestLibghosttyPTYSubscribeWithSnapshot_LiveAltExitRestoresPrimary (0.01s)
=== RUN   TestVTPrimaryScreenFilter_PassesPrimaryBytes
--- PASS: TestVTPrimaryScreenFilter_PassesPrimaryBytes (0.00s)
=== RUN   TestVTPrimaryScreenFilter_StripsAlternateScreenContent
--- PASS: TestVTPrimaryScreenFilter_StripsAlternateScreenContent (0.00s)
=== RUN   TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes
=== RUN   TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes/47
=== RUN   TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes/1047
=== RUN   TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes/1049
--- PASS: TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes (0.00s)
    --- PASS: TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes/47 (0.00s)
    --- PASS: TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes/1047 (0.00s)
    --- PASS: TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes/1049 (0.00s)
=== RUN   TestVTPrimaryScreenFilter_SplitAcrossReads
--- PASS: TestVTPrimaryScreenFilter_SplitAcrossReads (0.00s)
=== RUN   TestVTPrimaryScreenFilter_MultiParamAltMode
--- PASS: TestVTPrimaryScreenFilter_MultiParamAltMode (0.00s)
=== RUN   TestVTPrimaryScreenFilter_NonAltPrivateModesPassOnPrimary
--- PASS: TestVTPrimaryScreenFilter_NonAltPrivateModesPassOnPrimary (0.00s)
PASS
ok  	github.com/hayeah/supervisor	0.205s
```

What the behavior tests prove:

- `TestLibghosttyPTYSnapshot_AltScreenIncludesPrimaryBeforeAlternate` writes 12 primary lines, enters `ESC[?1049h`, paints alt content, snapshots, verifies `primary-01` appears before `ALT-PANEL`, replays into a fresh libghostty terminal, verifies the client is on alternate screen, then feeds `ESC[?1049l` and verifies primary scrollback is present.
- `TestLibghosttyPTYSubscribeWithSnapshot_LiveAltExitRestoresPrimary` uses `SubscribeWithSnapshot`, writes a live `ESC[?1049lprimary-after`, verifies the live stream includes the alt-exit sequence, replays snapshot+live into a fresh terminal, and verifies the client is back on primary with prior scrollback plus post-alt output.

## Trouble report
- The cleanest inactive-screen formatter exists only inside Ghostty Zig (`ScreenFormatter` over `*Screen`); the public C and Go APIs expose only terminal active-screen formatting, so this task needs a local mirror unless we vendor an upstream API patch.
- In the live-exit test, post-alt primary output can overwrite the restored primary cursor row (observed `primary-after` replacing `primary-12`). The assertion checks durable scrollback rows plus the post-alt output rather than pinning the exact overwritten row.
