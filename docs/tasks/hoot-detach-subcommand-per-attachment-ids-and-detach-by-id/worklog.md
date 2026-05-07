---
status: done
section: Hoot detach subcommand — per-attachment IDs and detach by id
slug: hoot-detach-subcommand-per-attachment-ids-and-detach-by-id
mode: worktree
spec: spec.md
created: 2026-05-06T09:41:31Z
---

> ## Hoot detach subcommand — per-attachment IDs and detach by id
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Add a `hoot detach <id>` subcommand. Today there's no way to remotely kick a single attachment off a session; `hoot kill` signals the monitored process, not the attach plumbing.
>
> User direction (paraphrased — agent should produce a real spec):
> - Generate a short attachment ID per attachment, globally unique across the namespace that already contains session IDs (i.e. the same shortid pool that `hoot run --key` and the auto-generated session keys live in). The same `id` argument to `hoot detach <id>` should be unambiguous: if it's a session id, detach every attachment on that session; if it's an attachment id, detach only that one.
> - Store attachments in `state.json` for the session they're attached to. Add when the attachment hello completes; remove when the attachment ends (graceful close or forced detach).
> - The Hello message could optionally carry origin info (host, terminal name/version, `TERM`, the user's local cols×rows). Stash that in the attachment record so `hoot list` can render it.
>
> Open design questions for `spec.md`:
>
> - **Process model.** Where does an attachment "live" today? Walk through the attach handler (`attach_handler.go`) and the dispatcher's subscriber map (`pty_libghostty.go`). What does it mean to "detach" — close the WS/socket, deregister the subscriber, both? Is there one goroutine per attachment? File:line.
> - **ID allocation.** The shortid package (`internal/shortid`) is the obvious source — confirm it can mint IDs that don't collide with existing session keys, or document the rule (e.g. "session keys + attachment IDs share the same pool, allocator checks against state.json union"). Where does the allocator run — in `__session` (which knows its own attachments) or somewhere that knows ALL sessions' attachments? Implications for collisions across sessions.
> - **state.json schema.** Add `attachments: [{id, started_at, client_size, origin?, …}]` to the session state. Who owns updates — the supervisor process? Concurrency/locking story. What happens when `__session` exits — attachment records cleaned automatically (state.json goes with the dir) or do we need explicit teardown.
> - **Hello extension.** Backwards compat: old clients without the new fields should still work; their attachment records carry whatever they did send (or empty origin). Field shape: `{client_host, client_term, client_term_program?, client_term_program_version?, client_cols, client_rows}`. Anything else worth including (TZ? username?) — keep minimal, can extend later. Wire field naming.
> - **Detach mechanics.** How does the supervisor force-close a single attachment from outside? Does the rpc.sock mux already have a per-attachment handle, or do we need to keep a `map[id]chan struct{}` of cancellation channels? The signal path from `hoot detach` CLI → serve.go → rpc.sock → individual attachment goroutine.
> - **CLI shape.** `hoot detach <id>` (positional). What about `hoot detach <session-id> <attachment-id>` for disambiguation if the same shortid alphabet is used? Probably not needed if IDs are globally unique. Exit codes: 0 success, non-zero for "no such id". What does the detached client see — clean EOF, or a specific reason ("detached by hoot detach")?
> - **Local + remote.** Local path: CLI dials the local rpc.sock for the resolved session. Remote: through the existing `hoot serve` proxy. Mirror what `hoot kill` did.
> - **Auth/safety.** Same as `hoot kill` — the existing attach auth covers this (whoever can attach can detach), nothing new. Confirm.
> - **Surface.** CLI shape, exit codes, error cases.
>
> Pre-plant the rfc gate so the human reviews before implementation.
>
> - [ ] rfc: review spec.md (process model + attachment registry + detach mechanics)
> - [ ] implement per spec — include tests for at least the local path

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### phase 1 — spec
- [x] explore attach_handler.go / pty_libghostty.go / state.go / shortid / serve.go
- [x] write spec.md (process model, registry, schema, Hello, detach mechanics, list, CLI)
- [ ] park at rfc gate; wait for human review

### phase 2 — implement (rfc ticked)
- [x] schema in `state.go`: PTYSize / Origin / AttachmentRecord; SessionState.Size; Attachments[]
- [x] effective-size hook into AttachSet sizeApplier → Writer.Update (registry.SetEffectiveSize)
- [x] reshape `attachwire.Hello` to {Size: PTYSize, Origin: Origin}; drop AsciiCinemaPlayback* and handler-side plumbing
- [x] populate Hello.Origin from cmdAttach (host/term/term_program/term_program_version/user)
- [x] `attachRegistry` in session library: mint id via shortid.Generate over session-local set; Add/Remove; persist via Writer.Update
- [x] supervisor mux routes: DELETE /attachments, DELETE /attachments/{id} (registered by Runner.registerAttachRoutes)
- [x] inject "[detached by hoot detach]" final-frame on supervisor close (synchronous via writeMu so it actually lands)
- [x] hoot serve proxy: DELETE /sessions/{key}/attachments[/{id}]
- [x] `cmd/hoot/detach.go` local + remote; parse `<sess>/<att>`; two-stage Resolve
- [x] tests: registry+state.json roundtrip, detach by attachment id, detach by session id, arg-parse table, scoped-resolve no cross-session bleed
- [x] README + usage() for `hoot detach`; show both `hoot detach foo` and `hoot detach foo/bar`

## Agent log
- 2026-05-06T10:30Z spawned, checked out hootty (worktree 000), read attach_handler.go / pty_libghostty.go subscribe path / AttachSet / state.go / shortid / serve.go / cmd/hoot routing.
- 2026-05-06T10:32Z noted that `hoot kill` referenced in section text doesn't actually exist; modeled CLI shape on `hoot attach` / `hoot resolve` instead. Documented in spec.
- 2026-05-06T10:34Z drafted spec.md covering process model (one goroutine per attachment + writer + live-forwarder, all reaped by serveOne defers), per-session registry with CLI-side union resolution for cross-session collisions, state.json schema bump (Cols/Rows + Attachments[]+Origin), Hello extension (5 Client* fields, all omitempty), detach via DELETE /attachments[/{id}] on rpc.sock + proxy through hoot serve, hoot list --table rendering. Set status: blocked at the rfc gate.
- 2026-05-06T09:47Z spec.md drafted; parked at rfc gate. covers: per-session attachment registry, state.json schema bump (Cols/Rows + attachments[]+origin), Hello extension (5 Client* fields), DELETE /attachments[/{id}] on rpc.sock + hoot serve proxy, hoot list --table rendering with stale tagging. status: blocked.
- 2026-05-06T09:56Z synced workspace — rebased worktree onto master (now has hoot kill at 15e2a05). updated spec.md to reference cmd/hoot/kill.go as the authoritative model: same CLI flag layout, exit-code table (0/1/2/3/5), dialUnixSock pattern, /sessions/{key}/<verb> routing. corrected earlier note that said hoot kill didn't exist.
- 2026-05-06T10:25Z ID design pivot: switched to long-hex (crypto/rand 8 bytes = 16 chars) for both session keys and attachment IDs; shortid.Resolve handles prefix display at CLI boundary. Drops the global-allocator + per-session AmbiguousIDError debate entirely — uniqueness is statistical (2^64), no coordination needed. Convention captured at notes/2026-05-06/shortid-convention-long-internal-prefix-resolve_claude.md. Existing sessions are nuked on upgrade per user direction (no on-disk migration). Hello also reshapes to {Size: PTYSize, Origin: Origin} matching the persisted record shape, and drops AsciiCinemaPlayback* fields. Spec updated, parked at rfc.
- 2026-05-06T10:31Z ID alphabet finalized: base34 (shortid.IDAlphabet, l/o excluded), 13 chars per ID — covers a uint64 of entropy with no bits lost. Spec + mdnote updated to match.
- 2026-05-07T04:15Z rebased onto master (now at `93c5699`, the cinema-excision merge). 5 conflicts, all small and resolved per boss guidance:
  - `internal/attachwire/wire.go`: master kept `{Cols, Rows}` on Hello; ours reshaped to `{Size, Origin}`. Took ours (post-cinema baseline already lacks the AsciiCinemaPlayback fields, so this is purely the reshape).
  - `cmd/hoot/run.go` + `cmd/hoot/attach.go`: cosmetic `attachOptionsFromFlags` call-site formatting (multi-line vs single-line). Took master's compact form.
  - `cmd/hoot/run_attach_test.go`: master asserted `hello.Cols/Rows`; ours uses `hello.Size`. Took ours (matches the new Hello shape).
  - `attach_handler.go`: comment-wording differences in serveOne and the live-forward goroutine. Took master's wording (slightly clearer "forwards live chunks straight to the client").
  Re-amended commit to drop a stray `hoot` binary that `git add -A` accidentally staged. Final commit: `accfb53`. `go test ./...` clean. `status: done` again. PTYSize/Origin live in attachwire (re-exported as session.PTYSize/Origin); Hello reshaped, asciicast playback fields and handler removed (CLI flags also dropped — they were no-ops without the wire fields). attachRegistry minted ids via shortid.Generate over the session's own attachments slice and persisted on every Add/Remove. Supervisor /attach registration moved out of LibghosttyPTY.RegisterRoutes into Runner.registerAttachRoutes so the registry/Writer can be threaded in; pty.RegisterAttachRoute kept as a registry-less entry point for the attachetest harness. Force-detach writes the yellow notice synchronously under a writeMu shared with the writer goroutine — the first try lost the notice on a race; the mutex guarantees it lands before conn.Close. Smoke (run/attach/detach/list) confirms attachments[] populates with id+size+origin and clears after detach; the detached client now prints "[detached by hoot detach]" in yellow before the disconnect banner. `go test ./...` clean.

## Boss log
- 2026-05-06T09:43Z scope expansion: fold the `hoot list` improvements into this section instead of a separate one. since the attachment registry + Hello-origin extension is the foundational change, list rendering belongs in the same spec/implementation pass.
  
  include in spec.md (in addition to what's already there):
  
  - `hoot list` (and the underlying state.json read) should surface:
    - the session's own window size (cols × rows of the supervised PTY)
    - per-attachment: short ID, client-reported size (cols × rows), origin info if present (host, terminal name, TERM_PROGRAM, etc.)
    - the "effective" / lower-denominator window size — the min(cols), min(rows) across live attachments — which is what hoot already negotiates today; expose explicitly so users can see why the session sized to what it did
  - where does size negotiation actually happen? trace: each attachment Hello reports rows/cols; dispatcher resizes the live emulator to the min across attachments. file:line. confirm what "effective" means precisely (min, mode, something else?).
  - what's the JSON shape of `hoot list` today? does it emit JSON or plain text? if JSON, extend; if text, decide table layout. add `--json` flag if not already there.
  - per-attachment origin fields render terse: `host=foo term=ghostty cols=120 rows=40`. don't paginate.
  - effective vs session size: if 3 attachments are (120,40) (100,30) (140,50), session sizes to (100,30). show all four in `list` (per-attachment + effective + session).
  - stale attachments: if one died ungracefully and didn't get pruned from state.json, what does list show? "stale" or just whatever's there with a heads-up. tie into the cleanup story.
  - backwards compat: old state.json without attachment list — render as "0 attachments" or omit column. don't crash.
  - concrete table layout (TTY) AND JSON schema in the spec.
  
  rename the section header in your worklog frontmatter is fine to leave alone — slug stays. just expand the spec and add list-rendering to the implementation surface.
  
  re-park at rfc when revised. human will review the merged spec.
- 2026-05-06T10:41Z ticked: rfc
- 2026-05-07T03:57Z rebase conflict on master during `boss lgtm`. aborted — worktree is clean. status flip back to `working` and resolve, please.
  
  # what landed on master since you branched
  
  base = `15e2a05` (which is `hoot kill` — you already rebased onto this earlier).
  
  four commits landed since (the `hoot-attach-replay-shows-tui-animation-artifacts` branch was merged). these directly overlap with files you also touched:
  
  - `0dbdc94` "Excise asciicast cinema playback from attach"
    - touches: `attach_handler.go`, `attach_handler_test.go`, `cmd/hoot/attach.go`, `cmd/hoot/attach_clone_test.go`, `cmd/hoot/attach_golden_test.go`, `cmd/hoot/main.go`, `cmd/hoot/run.go`, `cmd/hoot/run_attach_test.go`, `internal/attachwire/wire.go`
    - removed `sendAsciiCinemaPlayback` / `streamAsciiCinemaPlayback` + the buffering goroutine in `serveOne`
    - dropped `AsciiCinemaPlayback*` fields from `attachwire.Hello`
    - dropped `--no-ascii-cinema-playback` / `--ascii-cinema-playback-window` / `--ascii-cinema-playback-speed` flags from `hoot attach` + `hoot run`
    - dropped `attachPlaybackConfig` from `cmd/hoot/attach.go`; updated `runAttachLoop` / `runConnectLoop` / `runSession` signatures
  - `6ceb071` "Forward snapshot frames through the WS bridge"
    - touches: `cmd/hoot/serve_bridge.go` (M), `cmd/hoot/serve_bridge_pump_test.go` (A)
    - `pumpUpstreamToWS` now forwards `MsgSnapshotScrollback` (0x04) and `MsgSnapshotScreen` (0x05) as binary WS payloads alongside `MsgOutput`. Silently drops `MsgPing` / `MsgPong` (WS has own keepalive).
  - `ba836c1` "README: drop ascii-cinema-playback flags, explain snapshot-only attach"
    - touches: `README.md`
  - `8385560` "docs/cross-compile: drop --no-ascii-cinema-playback from smoke-test snippet"
    - touches: `docs/cross-compile.md`
  
  # resolution guidance (paste verbatim — important)
  
  resolve by **preserving features from master, not by taking your side blindly**. for each conflicted file, read master's version end-to-end first and understand what features the new lines implement before overwriting. if master's changes are a superset of what you did, adopt master's version and re-apply your delta on top. re-run tests after resolution.
  
  specific guidance:
  
  - the cinema-excision overlap is mostly **agreement** between you and master — both removed the same things. the conflict is over the surrounding context (your attach-registry / Hello-reshape / detach-route changes vs master's simplified post-cinema versions of the same files). prefer master's post-cinema baseline, then re-apply your attachment registry / `{Size, Origin}` Hello / `/attachments[/{id}]` mux routes / `runAttachLoop` Origin plumbing / `cmd/hoot/detach.go` / `hoot list --table` on top.
  - `cmd/hoot/serve_bridge.go`: master added snapshot-frame forwarding to `pumpUpstreamToWS` and a new `serve_bridge_pump_test.go`. if your branch also modified `serve_bridge.go` (probably to add `DELETE /sessions/{key}/attachments[/{id}]` proxy routes), keep master's `pumpUpstreamToWS` change AND your new DELETE routes — they're independent edits.
  - `cmd/hoot/attach.go`: master removed `attachPlaybackConfig` and changed `runAttachLoop` / `runConnectLoop` / `runSession` signatures. your branch added Origin plumbing through the same call sites. take master's signature shape, then re-thread your Origin parameters through.
  - `cmd/hoot/run.go`: master dropped cinema flags from `hoot run`; your branch dropped them too. take master's version then make sure your `generateKey` and any other run.go edits are present.
  - `internal/attachwire/wire.go`: master dropped `Hello.AsciiCinemaPlayback*`. you also dropped them and reshaped `Hello` to `{Size: PTYSize, Origin: Origin}` plus added the new origin fields. take master's post-excision Hello as the base, then apply your reshape on top. make sure no orphaned playback fields slip back in.
  - `cmd/hoot/main.go`: master removed cinema flag entries from `usage()`. your branch added the `hoot detach` line. trivial merge — keep both deltas.
  - README.md / docs/cross-compile.md: master rewrote the cinema-flag sections. prefer master's prose; if you added detach/list documentation, append after master's prose, don't merge into the conflicted hunks.
  - attach_handler.go / attach_handler_test.go / attach_clone_test.go / attach_golden_test.go / run_attach_test.go: high overlap. master's post-excision baseline is simpler; re-apply your registry + `{Size, Origin}` + detach-route additions on top.
  
  # expected next actions
  
  - flip `status: working` in your worklog frontmatter (drop the `done` it currently has).
  - resolve, update commit (or new commit, your call), add a brief note to `## Agent log` summarizing the resolution.
  - run `go test ./...` clean.
  - flip `status: done`.
  - i'll re-run `boss lgtm`.
  
  if rebase still conflicts after a clean attempt, append the new context to `## Agent log` with what you saw — don't keep retrying blindly.

## Evidence

Commit (post-rebase): `accfb53` on branch `hoot-detach-subcommand-per-attachment-ids-and-detach-by-id` — "Add hoot detach subcommand with per-attachment IDs", rebased onto master `93c5699`.

`go test ./...`:
```
ok  	github.com/hayeah/hootty	6.595s
ok  	github.com/hayeah/hootty/cmd/hoot	2.675s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	(cached)
ok  	github.com/hayeah/hootty/internal/sshtransport	(cached)
```

CLI smoke (`hoot run smoke; hoot attach smoke &; hoot detach smoke`):
- `hoot list` after attach shows the attachment with id `d43`, size `80×24`, and origin `{host: m4mini.local, term: tmux-256color, term_program: tmux, term_program_version: 3.6a, user: me}`.
- `hoot detach smoke` prints `hoot: detached all attachments from session "smoke"`; subsequent `hoot list` shows `attachments` cleared.
- The detached client receives the yellow `\x1b[33m[detached by hoot detach]\x1b[0m` notice before the standard disconnect banner.

New tests:
- `TestSplitDetachArg` — table-driven `<sess>[/<att>]` parser (cmd/hoot/detach_test.go).
- `TestAttachRegistry{AddRemoveStateJSON,EmptyOriginIsNil,CloseAll,IDsAreSessionScoped}` — registry contract + state.json roundtrip + per-session id scoping (attach_registry_test.go).
- `TestDetachBy{AttachmentID,SessionID}`, `TestDetachUnknownAttachment404` — end-to-end via Runner + rpc.sock (detach_e2e_test.go).

## Trouble report
