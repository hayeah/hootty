---
status: done
section: hoot clone <key>: spawn a sibling session with the same spawn parameters
slug: hoot-clone-key-spawn-a-sibling-session-with-the-same-spawn-parameters
mode: worktree
spec: spec.md
created: 2026-05-05T16:56:54Z
---

> ## hoot clone <key>: spawn a sibling session with the same spawn parameters
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Vision: from an attached session, spawn a new fresh independent supervised session running the same command in the same env / cwd / tags. Like tmux's `<prefix>c` for a new window — but the new "window" is a fresh, independently supervised hoot session.
>
> **Mental model**: not `fork(2)` of the running child. The supervisor does what it did to spawn this session, again, right now. So we need the original spawn parameters preserved in `StateFile`:
>
> - argv
> - cwd
> - env (or a curated subset — full env is too big and leaky; spec the policy)
> - tags (the existing session-tag concept)
>
> Then:
>
> - **CLI**: `hoot clone <key>` — clone locally (same state-dir).
> - **HTTP**: `POST /sessions/{key}/clone` on the serve bridge.
> - **Attach-side UX**: a new prefix-key binding (`<prefix>c` is the natural choice — mirrors tmux) that issues a clone of the *currently attached* session and attaches to the new one.
>
> Open design questions for the spec:
>
> - **Env capture policy**: full snapshot at spawn time? An allowlist (PATH, HOME, USER, SHELL, TERM, LANG, LC_*, ...)? A user-tunable allowlist via flag/config? Spec a default that's safe and useful — too narrow breaks the cloned process, too wide leaks secrets.
> - **StateFile schema bump**: adding `argv/cwd/env/tags` to the persistent state. Forward-compat: old state files don't have these fields → `clone` returns "no clone metadata" error. Don't break existing sessions.
> - **Optional flags on `clone`**: `--key <new-key>` (override generated id), `--detach` (don't attach after clone — useful for scripting), `--remote` (clone on a different host? probably out of scope; flag and defer).
> - **`--attach` on clone**: default ON or OFF? `<prefix>c` from inside an attach implies attach-after-clone; bare `hoot clone` from a shell — what's the default? Spec it.
> - **Clone of a clone**: clone metadata propagates so the new session is also clone-able.
> - **Prefix-key binding mechanics**: how does the local attach client trigger the clone? Sending an out-of-band signal to the server (new attachwire frame? HTTP call to the serve bridge?) — depends on which transport is in use. Spec both local-sock and http transports.
>
> The agent should write `spec.md` covering:
> - Env-capture policy with rationale
> - StateFile schema additions and backward-compat behavior
> - The CLI / HTTP surface
> - The prefix-key binding wire path (one mechanism that works for both local and remote)
> - Verification plan including: clone preserves cwd, clone preserves env subset, clone-of-clone works, `<prefix>c` from inside an attach spawns + attaches the new session
>
> This is design-heavy. Park at the rfc gate.
>
> - [ ] rfc: review spec.md
> - [ ] implement per spec

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Check out `~/github.com/hayeah/hootty` into `repos/`.
- [x] Inspect current spawn, state, serve, clone-adjacent, and attach-prefix paths.
- [x] Draft `spec.md` covering env inheritance/overrides, state schema, CLI/HTTP surface, prefix-key wire path, and verification.
- [x] RFC review gate: wait for boss/human review of `spec.md`.
- [x] After RFC approval, add `argv`/`cwd` schema and metadata plumbing through `SessionConfig`.
- [x] Implement clone-time env override parsing with focused tests.
- [x] Factor shared spawn/clone helpers.
- [x] Add `hoot clone` local and remote CLI behavior.
- [x] Add `POST /sessions/{key}/clone` and local per-session `POST /clone`.
- [x] Add `<prefix>c` attach behavior and target switching.
- [x] Update README/usage.
- [x] Run verification plan and collect evidence.

## Agent log

- 2026-05-05T16:59:13Z Checked out `github.com/hayeah/hootty` via `boss checkout` and inspected `state.go`, `session.go`, `cmd/hoot/run.go`, `cmd/hoot/session.go`, `cmd/hoot/service.go`, `cmd/hoot/serve*.go`, `cmd/hoot/attach.go`, `internal/attachwire/wire.go`, and README/API docs.
- 2026-05-05T16:59:13Z Drafted `spec.md` and parked at the planned `rfc: review spec.md` gate. Key choices: `session.spawn` schema, safe allowlisted env snapshot, detached-by-default CLI clone, and HTTP control-plane prefix clone for both local and remote attaches.
- 2026-05-05T16:59:13Z Tightened the RFC after a sanity pass: local `/clone` is owned by `RunCmdService` with explicit `StateDir`/`Key`, and env capture includes a v1 `--clone-env` / `clone_env` opt-in allowlist extension while keeping the sensitive-name denylist.
- 2026-05-06T03:25:02Z Updated RFC from human review: removed persisted env snapshots and `env_policy`; clone now inherits env through the fresh `__session` spawn chain, with clone-time `--env` / `--env-file` and HTTP `env` overrides.
- 2026-05-06T03:31:36Z Verified there is no existing session tag field or `--tag` surface in `hootty`; removed tags from the active spec schema and verification plan.
- 2026-05-06T03:33:29Z Updated RFC from human review: removed old-state backward compatibility requirements; users can remove old state dirs on upgrade, and missing argv/cwd is corrupt state rather than a special compatibility case.
- 2026-05-06T03:37Z RFC gate ticked by boss; moving into implementation.
- 2026-05-06T03:40Z Landed argv/cwd state plumbing and shared session spawn helper in `746b288`; `go test ./...` passed.
- 2026-05-06T03:42Z Landed clone env override parsing/tests in `67b5f9c`; `go test ./...` passed. Read boss verification note and will cover structured clone behavior with production-path Go tests; will evaluate golden harness for `<prefix>c`.
- 2026-05-06T03:45Z Landed clone primitive, `hoot clone`, remote clone, serve `POST /sessions/{key}/clone`, and per-session `POST /clone` in `f165fcb`; `go test ./...` passed.
- 2026-05-06T03:48Z Landed attach `<prefix>c` clone switching in `98d6e62`; removed unsupported clone branch per human review because local and remote attach targets should both support clone. `go test ./...` passed.
- 2026-05-06T03:50Z Documented `hoot clone`, env overrides, `<prefix>c`, state schema note, and HTTP clone route in `d74a5cd`; `go test ./...` passed.
- 2026-05-06T03:53Z Final verification passed: `go test ./...`, `make build`, real `hoot run`/`clone`/clone-of-clone/attach smoke. Worktree clean.
- 2026-05-06T03:51Z Boss reported lgtm rebase conflict against master in `cmd/hoot/run.go`; reopening work to preserve master `hoot run --attach` while keeping shared spawn helper.
- 2026-05-06T04:07Z Resolved the master rebase by preserving `hoot run --attach` local/remote handoff and re-applying the shared clone spawn helper plus attach target switching. Landed reconciliation in `4e39975`; `go test ./...` passed; worktree clean.

## Boss log
- 2026-05-06T03:37Z ticked: rfc
- 2026-05-06T03:40Z before you start coding the verification, think about lifting the smoke tests into the existing golden-fixture harness.
  
  we have a convention from the attach-detach work (sealed at 590ca00):
  - `internal/attachetest/harness.go` — libghostty-based remote+local terminals you can drive end-to-end.
  - `cmd/hoot/attach_golden_test.go` — six golden scenarios using `attachetest.NewRemote` / `NewLocalTerminal` / `CompareGolden`. Goldens checked in under `internal/attachetest/testdata/`. `UPDATE_GOLDENS=1 go test ./...` regenerates.
  
  clone is a natural fit:
  - spawn parent session with a deterministic argv/cwd/env subset, attach via the harness, snapshot.
  - `<prefix>c` to clone, attach to the new session, snapshot.
  - assert: same argv visible (`ps`-like fixture cmd, or remote process echoes its argv on startup), cwd matches, env subset matches, parent and clone have distinct keys, both running.
  - detach scenario: `hoot clone --detach <key>` from a shell — golden the attach into the clone afterward shows the right state.
  - clone-of-clone: snapshot after second clone, prove metadata propagated.
  
  think through how to make these scenarios deterministic enough for goldens (timestamps, pids, generated keys all need redaction or pinning). the existing harness probably already has redaction conventions — read it.
  
  if golden-snapshotting clone behavior is genuinely a poor fit (e.g. the asserts are about structured state, not terminal output), say so and just write focused Go tests that exercise the production code paths — the bar is "would survive a refactor", not "must use goldens".
  
  flag in the agent log if you want to push back on any of this. either way: cover the verification list with real go tests, not throwaway shell smokes, where it's reasonable.
- 2026-05-06T03:51Z rebase conflict on master during lgtm. aborted — worktree is clean.
  
  textual conflict in: cmd/hoot/run.go (UU)
  auto-merged (no conflict): cmd/hoot/serve_spawn.go, cmd/hoot/service.go, cmd/hoot/session.go, cmd/hoot/spawn.go, session.go, session_test.go, state.go
  
  commit that landed on master since you branched:
  
  - a4f3c2e (merge) / abb3c45 "Add run attach mode" — touched README.md, cmd/hoot/attach.go, cmd/hoot/main.go, cmd/hoot/run.go, cmd/hoot/run_attach_test.go (NEW)
  
  what landed structurally: `hoot run --attach` flag — after spawn, the same process flips into attach mode against the new session. The implementation reuses the existing `runAttach` core (which takes a dialer parameter), so cmd/hoot/run.go gained an `--attach` codepath that calls into the attach machinery. The remote (`--remote http://...`) and local cases are both supported.
  
  your branch's run.go also touches the spawn flow (clone-related shared spawn helper). The two are independent in intent but overlap in cmd/hoot/run.go's flag plumbing and post-spawn handoff.
  
  resolution requirements (paste verbatim):
  
  > Resolve by PRESERVING features from master, not by taking your side blindly. For each conflicted file, read master's version end-to-end first and understand what features the new lines implement before overwriting. If master's changes are a superset of what you did, adopt master's version and re-apply your delta on top. Re-run tests after resolution.
  
  specifically for cmd/hoot/run.go: master added the `--attach` flag and the post-spawn attach handoff (calling into runAttach with the appropriate dialer for local vs remote). Your branch added shared-spawn-helper plumbing. Keep BOTH — adopt master's --attach handoff, and re-apply your shared spawn helper on top so `hoot run --attach` works through your refactor.
  
  after resolution: confirm `go test ./...` is green, append a note to ## Agent log, flip status back to done. i'll re-run boss lgtm.

## Evidence

RFC artifact:

- `spec.md` at workspace root covers the revised env inheritance/override policy, StateFile schema, CLI/HTTP surface, prefix-key binding path, and verification plan. Per human review, old state dirs are not supported and should be removed on upgrade.

Commits:

- `746b288` Persist session argv and cwd
- `67b5f9c` Parse clone environment overrides
- `f165fcb` Add clone command and HTTP route
- `98d6e62` Clone from attach prefix
- `d74a5cd` Document hoot clone
- `4e39975` Reconcile run attach with clone targets

Automated tests:

```sh
$ go test ./...
ok  	github.com/hayeah/hootty	(cached)
ok  	github.com/hayeah/hootty/cmd/hoot	(cached)
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	(cached)
ok  	github.com/hayeah/hootty/internal/sshtransport	(cached)
```

Coverage added:

- `session_test.go`: new sessions write `session.argv` and `session.cwd`.
- `cmd/hoot/env_override_test.go`: `--env`, env lookup, `--env-file`, malformed input, and override precedence.
- `cmd/hoot/clone_test.go`: clone preserves argv/cwd, clone-time env reaches child state without being persisted, corrupt source state errors, HTTP clone route, remote CLI clone request.
- `cmd/hoot/attach_clone_test.go`: `<prefix>c` clones and switches the attach loop to the new target.
- `cmd/hoot/run_attach_test.go`: master `hoot run --attach` behavior remains covered after the clone spawn refactor.

Real CLI smoke:

```sh
$ make build
go build -o bin/hoot ./cmd/hoot

$ (cd "$CWD" && bin/hoot run --state-dir "$STATE" --key src -- \
    bash -lc 'printf "cwd=%s clone_var=%s\n" "$PWD" "${CLONE_VAR:-}"; sleep 45')
src
hoot: session "src" started (...)

$ bin/hoot clone --state-dir "$STATE" --key dst --env CLONE_VAR=visible src
dst
$ bin/hoot clone --state-dir "$STATE" --key dst2 dst
dst2

$ jq -c '.session | {key, argv, cwd, has_env: has("env")}' "$STATE"/{src,dst,dst2}/state.json
{"key":"src","argv":["bash","-lc","printf \"cwd=%s clone_var=%s\\n\" \"$PWD\" \"${CLONE_VAR:-}\"; sleep 45"],"cwd":".../tmp/e2e-cwd","has_env":false}
{"key":"dst","argv":["bash","-lc","printf \"cwd=%s clone_var=%s\\n\" \"$PWD\" \"${CLONE_VAR:-}\"; sleep 45"],"cwd":".../tmp/e2e-cwd","has_env":false}
{"key":"dst2","argv":["bash","-lc","printf \"cwd=%s clone_var=%s\\n\" \"$PWD\" \"${CLONE_VAR:-}\"; sleep 45"],"cwd":".../tmp/e2e-cwd","has_env":false}
```

Attach transcript checks:

- `dst` attach output showed `cwd=.../tmp/e2e-cwd clone_var=visible`.
- `dst2` attach output showed the same cwd and `clone_var=` empty, proving clone-of-clone propagates argv/cwd but does not replay one-shot env overrides.

## Trouble report

- I did not add a golden snapshot file for clone. The durable assertions are structured state and target switching, so I used focused production-path Go tests instead. The attach-side behavior is covered by `cmd/hoot/attach_clone_test.go`, which drives the real attach loop and prefix FSM with deterministic in-process attach targets; this avoids generated keys, pids, timestamps, and terminal snapshot redaction churn.
- During boss LGTM, master had added `hoot run --attach`, causing a rebase conflict in the same spawn handoff path. Resolution kept master's attach-after-run behavior for both local and remote targets, then routed it through this branch's shared spawn and attach-target abstractions.
