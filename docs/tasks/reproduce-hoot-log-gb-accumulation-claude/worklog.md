---
status: done
section: Reproduce hoot log GB accumulation (claude)
slug: reproduce-hoot-log-gb-accumulation-claude
mode: worktree
spec: spec.md
created: 2026-05-09T09:32:31Z
---

> ## Reproduce hoot log GB accumulation (claude)
>
> ---
> status:
>   type: open
> ---
>
> A new GB-scale `pty.hootty.log` accumulation showed up after `d708de0` (self-session guard for `hoot log <current-session>` from inside its own session). That fix should block the previous feedback path, so this occurrence must have a different cause — log bytes still leaking back into the recorder somehow.
>
> Parallel investigation: a sibling codex agent is racing on the same problem in a separate workspace (`reproduce-hoot-log-gb-accumulation-codex`). Don't coordinate — find your own deterministic repro.
>
> Investigate:
> - Reproduce a runaway log against current master (post-`eb51a17`).
> - Trace the actual write path. Production recorder writes are `LibghosttyPTY.readLoop → master.Read → Recorder.Write` + resize. Anything else getting onto the master?
> - Candidate hot zones: nested attach with `unset HOOT_SESSION` bypass, the new `hoot install-remote` / `hoot @host` auto-bootstrap from `eb51a17`, attach-replay paths if any feed master, the spacer/auto-split branches in the binary recorder.
> - Don't implement the fix — research-only. The human compares findings from both agents before deciding direction.
>
> Output: `spec.md` with deterministic repro recipe, traced write path, root-cause verdict, fix-shape sketch.
>
> - [ ] rfc: review findings

## Todos

- [x] Trace every PTY-master writer in current master (readLoop, libghostty query auto-replies, attach client `p.Write`, restorer paths)
- [x] Audit `hoot install-remote` / `hoot @host` auto-bootstrap from `eb51a17` for in-session feedback
- [x] Audit nested-attach with `unset HOOT_SESSION` bypass
- [x] Audit recorder spacer / auto-split branches (`bridgeIdleLocked`, `flushPendingLocked`)
- [x] Build deterministic Go-test repro
- [x] Write spec.md with verdict, repro recipe, traced write path, fix-shape sketch
- [x] Audit recorder.go for unsigned-subtraction footguns
- [x] Fix bridgeIdleLocked + Write invariant
- [x] Convert repro tests to passing regressions
- [x] go test ./... clean

## Agent log

- 2026-05-09T16:33Z — Worktree created at `repos/github.com/hayeah/hootty` from master `eb51a17`.
- 2026-05-09T16:38Z — Read all PTY-master writers; only `readLoop` feeds the recorder. install-remote / @host bootstrap writes to os.Stdout/Stderr only and is guarded against in-session run via `errIfNestedHootSession`.
- 2026-05-09T16:42Z — Identified unsigned-subtraction underflow in `recorder.go:bridgeIdleLocked` reachable when `pendingBucketMs < lastEmittedMs`. That ordering is reachable post-`RecordResize` whenever the next Write lands inside the same 33ms bucket as the resize.
- 2026-05-09T16:45Z — Added `recorder_underflow_test.go` with two repros (synthetic-state + public-API). Both balloon the recorder file by MB in 2 s. See `tmp/164114_3N-recorder-runaway.log`.
- 2026-05-09T16:55Z — Spec.md drafted with verdict, traced write path, three fix-shape options. Status → done.
- 2026-05-09T17:10Z — RFC ticked. Boss greenlit shipping the fix. Audited recorder.go for unsigned-subtraction footguns: only `bridgeIdleLocked` (the GB driver) and `Write`'s pendingBucketMs install actually violate the lastEmittedMs invariant. `flushPendingLocked`'s and `RecordResize`'s `delta` computations are safe by construction once bucket and now respect the invariant.
- 2026-05-09T17:14Z — Fix landed (df60c1a). `Write` floors bucket to >= lastEmittedMs; `bridgeIdleLocked` early-returns on regressed nowMs as defense-in-depth. Invariant documented on the `lastEmittedMs` and `pendingBucketMs` fields.
- 2026-05-09T17:16Z — Converted `recorder_underflow_test.go` to passing regressions (200 ms flush budget, 4 KiB log-size budget). Full `go test ./...` clean. See `tmp/171530_<ms>-go-test-all.log`.

## Boss log
- 2026-05-09T09:50Z ticked: rfc
- 2026-05-09T09:50Z rfc green-lit by the human. new top-level box added: "implement per findings".
  
  your investigation converged with a parallel codex agent on the same root cause (codex's repro: nested self-attach with `unset HOOT_SESSION` produced 7.08M spacer frames all `advance=4294967295` — exact underflow signature you predicted). codex's section is now closed.
  
  go ahead and ship the fix per spec.md:
  - guard `bridgeIdleLocked` against the underflow. signed arithmetic, or an explicit `if pendingBucketMs <= lastEmittedMs return` early-out. pick whichever fits the surrounding code best.
  - audit `flushPendingLocked` and any other recorder paths for similar unsigned-subtraction footguns.
  - convert `recorder_underflow_test.go`'s two intentionally-failing tests into passing assertions: bound the flush time (e.g. < 50ms) and bound the resulting log size. these are the regression tests.
  - consider whether RecordResize at non-bucket-aligned ms is the only trigger, or whether other code paths can produce nowMs < lastEmittedMs. fix the underlying invariant, not just the resize path.
  - run `go test ./...` clean. no new diagnostic noise.
  - update README / docs only if the recorder's documented invariants change.
  
  flip status to working when you start, done when ready for review.

## Evidence

### Investigation phase

`tmp/164114_3N-recorder-runaway.log` — full `go test` transcript.

```
=== RUN   TestRecorderUnderflowAfterResize
    recorder_underflow_test.go:35: before flush: lastEmittedMs=1000 pendingBucketMs=990
    recorder_underflow_test.go:53: flushPendingLocked did not return in 2s; log grew to 8746370 bytes (runaway spacer loop confirmed)
--- FAIL: TestRecorderUnderflowAfterResize (2.00s)
=== RUN   TestRecorderResizeWriteFlushRunaway
    recorder_underflow_test.go:104: Write did not return in 2s via public API; log grew to 5037331 bytes — runaway confirmed
--- FAIL: TestRecorderResizeWriteFlushRunaway (2.00s)
FAIL
FAIL    github.com/hayeah/hootty        4.233s
```

Both tests deliberately fail (research-only; the runaway is the finding).
Public-API test produced 5.04 MB of spacer frames in 2 s with no caller
in the loop — extrapolates to ~9 GB/hour per stuck recorder.

The runaway is in `recorder.go:bridgeIdleLocked` (lines 212–236):

```go
for nowMs - r.lastEmittedMs > 65535 {           // unsigned underflow when nowMs < lastEmittedMs
    advance = ^uint32(0)
    write 9 bytes
    r.lastEmittedMs += uint64(advance)          // never crosses back below nowMs
}
```

Trigger: any `RecordResize` at non-bucket-aligned ms (essentially always)
followed by a `Write` within the same 33 ms bucket — i.e. ordinary
`hoot run --attach` after the first attach-side `MsgSize` lands. SIGWINCH-
driven re-resizes produce additional triggers across the session lifetime.

Precondition matrix and fix-shape sketch in `spec.md`. Bug pre-dates
`d708de0` (landed with `dcfb7e4`); the previous self-feedback loop masked it.

### Fix phase

Commit `df60c1a` on branch `reproduce-hoot-log-gb-accumulation-claude`.

Two narrow changes in `recorder.go`:

```go
// Write: floor bucket to lastEmittedMs to keep the monotonic invariant.
if bucket < r.lastEmittedMs {
    bucket = r.lastEmittedMs
}

// bridgeIdleLocked: defense-in-depth early return.
if nowMs <= r.lastEmittedMs {
    return nil
}
```

Plus invariant documented on the `lastEmittedMs` / `pendingBucketMs` fields.

Regression tests at `recorder_underflow_test.go` (passing, 0.0s):

```
=== RUN   TestRecorderUnderflowAfterResize
--- PASS: TestRecorderUnderflowAfterResize (0.00s)
=== RUN   TestRecorderResizeWriteFlushRunaway
--- PASS: TestRecorderResizeWriteFlushRunaway (0.00s)
PASS
```

Tests bound flush time to 200 ms and resulting log size to 4 KiB. Pre-fix
both budgets failed by orders of magnitude (5+ MB in 2 s).

Full suite (`tmp/165352_3N-go-test-all.log`):

```
ok  	github.com/hayeah/hootty	6.963s
ok  	github.com/hayeah/hootty/cmd/hoot	11.488s
ok  	github.com/hayeah/hootty/internal/bootstrap	1.230s
ok  	github.com/hayeah/hootty/internal/sessionpick	0.645s
ok  	github.com/hayeah/hootty/internal/shortid	0.336s
ok  	github.com/hayeah/hootty/internal/sshtransport	1.004s
```

## Trouble report

- LSP warns the worktree path is outside its workspace module — harmless,
  the worktree is its own go module with its own `go.mod` (the test runs
  fine via `go test`).
- LSP also surfaced six "undefined: StateFile" diagnostics on `recorder.go`
  during the fix edit; those are pre-existing scoping noise (the type is
  defined elsewhere in the package and `go build ./...` is clean).
- Considered whether the third spec-listed fix shape — flooring
  `lastEmittedMs` to a bucket boundary inside `RecordResize` — was worth
  doing alongside. Decided against: it loses delta fidelity on the resize
  → next-write transition for no additional safety, since `Write`'s
  bucket-floor already enforces the invariant.

