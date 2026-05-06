# SSH connection reuse for remote calls

## Goal

Confirm and document whether `hoot --remote ssh://...` already reuses an existing OpenSSH master connection when a long-running remote `attach` is active. The desired unit of reuse is the OpenSSH ControlMaster connection identified by a stable `ControlPath`, not a custom in-process tunnel shared between unrelated `hoot` CLI processes. One-shot commands must continue to work when no attach is running.

## Architecture

- `internal/sshtransport/transport.go`
  - Verify `ControlPath(stateDir, user, host, port)` is stable across independent invocations for the same normalized target.
  - Verify every SSH entry point (`remoteHome`, tunnel open, cleanup) uses `baseArgs(cfg, plan.ControlPath)` so OpenSSH sees the same `ControlPath`.
  - Add focused regression coverage if the current tests do not pin the cleanup/probe path to the same control socket.
- `cmd/hoot/remote.go`
  - Confirm `newSSHRemote` derives the same `StateDir` for `attach`, `list`, `resolve`, and `run`, because this is what makes the `ControlPath` convention cross-command.
- `README.md`
  - Tighten the `ssh://` remote documentation to describe the actual lifecycle: each CLI invocation may start a short-lived local mux client and remote `hoot serve`, while OpenSSH reuses the master connection through the stable hashed `ControlPath` when one already exists.

## Steps

- Check out `github.com/hayeah/hootty` and inspect the remote/SSH implementation.
- Write this workspace spec and seed the worklog todos.
- Build a local `hoot` binary from the worktree.
- Run existing focused tests for `internal/sshtransport` and `cmd/hoot` remote parsing/remote command behavior.
- Verify current SSH behavior against `ssh://devbox` with a cold call, then with a long-running `attach` active, capturing timings/process evidence in `tmp/`.
- If the control socket is already reused, add regression tests/docs only. If it is not reused, make the smallest code change that stabilizes the shared `ControlPath`/state-dir convention.
- Run the relevant Go tests and update README if behavior or documentation changes.
- Commit the repo changes and fill the worklog evidence.

## Verification

- `go test ./internal/sshtransport ./cmd/hoot`
- Manual smoke transcript saved under `tmp/`:
  - no attach active: `hoot list --remote ssh://devbox` succeeds standalone
  - attach active: a second `hoot list --remote ssh://devbox` completes materially faster than a cold first call or shows OpenSSH mux reuse via `-vv`/process/control-socket evidence
  - post-attach: one-shots still work or fail only for ordinary SSH/remote availability reasons, not because they depend on an attach process

## Open questions

None blocking. If `devbox` is unavailable in this environment, I will still land local regression tests/docs and mark the manual SSH timing as blocked evidence rather than inventing it.

## Design notes

- 2026-05-06T04:20Z - Interpreted "existing connection" as the OpenSSH ControlMaster connection, not the long-running `hoot attach` process itself.
  - The current implementation shells out to `ssh(1)` for each remote tunnel. OpenSSH multiplexing means later invocations still create a small ssh mux client process and a new channel, but they should not redo the expensive SSH handshake/authentication when `ControlPath` is stable.
  - A custom cross-process tunnel registry would be larger and more fragile: it would need discovery, ownership, cleanup, and failure semantics. The existing ControlMaster socket already solves those with OpenSSH's lifecycle rules.
  - The main risk to verify is consistency: `remoteHome`, tunnel setup, and cleanup must all pass the same `ControlPath` derived from the same per-command state directory.

- 2026-05-06T04:31Z - The first manual smoke used the workspace path as `--state-dir` and failed before auth because OpenSSH rejected generated Unix socket paths as too long.
  - Symptom: `ControlPath too long ('/Users/me/Dropbox/boss/tasks/.../tmp/ssh-smoke-state/.tunnels/cm-2ff8ddd6' >= 104 bytes)`.
  - Second symptom after the first fix: `Bad local forwarding specification '<long workspace path>/fwd-...sock:127.0.0.1:<port>'`, because the local `-L` Unix socket path has the same OpenSSH/path budget problem.
  - Picked fix: keep sockets under the requested state dir while those paths are short enough; otherwise move the ControlMaster socket and local forward socket to short user-cache directories. The remote PID file and remote `hoot serve` lifecycle stay unchanged.
  - Alternatives considered:
    - Always store sockets under the requested state dir: preserves locality, but it is exactly what breaks long workspaces and makes SSH remotes fail before they can benefit from reuse.
    - Always store SSH sockets under a global cache dir: simpler and maximizes reuse across different `--state-dir` values, but it changes existing path behavior even for normal short state dirs. I kept short state dirs unchanged to minimize blast radius.
  - Follow-on discovery: `Tunnel.Close` could block indefinitely after OpenSSH forked a persistent mux master; cleanup now runs before the final wait and the wait is bounded after SIGKILL.
