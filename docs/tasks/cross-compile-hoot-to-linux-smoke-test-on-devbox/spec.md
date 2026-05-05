# Cross-compile hoot to Linux

## Goal

Produce a Linux `amd64` `hoot` binary from this macOS host, copy it to `devbox`, and prove the CLI, supervisor, state-dir, HTTP, and remote attach paths work there. The deliverable is a reproducible local recipe documented in `hootty`; CI is explicitly out of scope.

## Architecture

- Repo under test: `repos/github.com/hayeah/hootty`.
- Native dependency: `github.com/mitchellh/go-libghostty`, consumed by `hootty` through cgo + `pkg-config`.
  - `go-libghostty` defaults to static cgo linking via `cgo_static.go`: `pkg-config --static libghostty-vt-static` plus `GHOSTTY_STATIC`.
  - `hootty/Makefile` currently assumes a host-built `go-libghostty` checkout at `~/github.com/mitchellh/go-libghostty/build/_deps/ghostty-src/zig-out/share/pkgconfig`.
- Upstream native library source: `repos/github.com/ghostty-org/ghostty`.
  - Ghostty's flake exposes `libghostty-vt-*` packages and their static archive/pkg-config metadata, but the flake is a Nix packaging path for each host platform rather than an out-of-the-box macOS-host cross-build recipe for downstream Go.
  - This macOS host has Zig 0.15.2 and no `nix`, so the practical path is building `libghostty-vt` directly with `zig build -Dtarget=x86_64-linux-gnu`.
- Minimal repo change: add `docs/cross-compile.md` with exact commands and add a short README pointer. Avoid Makefile/script changes unless the manual recipe becomes too error-prone during verification.

## Steps

- Inspect Ghostty Nix and Zig build support and record why Nix is not the selected route.
- Inspect `go-libghostty` cgo/pkg-config behavior and confirm static vs dynamic tag behavior.
- Build Linux `libghostty-vt` with Zig into an isolated prefix.
- Build `hoot-linux-amd64` with `CGO_ENABLED=1 GOOS=linux GOARCH=amd64`, Zig as the C compiler, and `PKG_CONFIG_PATH` pointed at the Linux libghostty prefix.
- Run `file ./hoot-linux-amd64` locally and capture the ELF evidence.
- `rsync` the binary to `devbox` using a conventional user-local path.
- On `devbox`, smoke test `hoot --help`, `hoot run`, `hoot list`, `hoot serve` plus `/healthz`.
- From macOS, run `hoot attach --host devbox:21000 <key>` against the devbox session and capture evidence that remote attach reaches the Linux session.
- Document the reproducible recipe in `hootty/docs/cross-compile.md` and link it from `README.md`.

## Verification

- Local:
  - exact cross-compile commands pasted into `worklog.md`.
  - `file ./hoot-linux-amd64` reports an ELF 64-bit x86-64 Linux executable.
- Devbox:
  - transcript for `hoot --help`.
  - transcript for `hoot run -- bash -c 'echo hello-from-linux; sleep 30'`.
  - transcript for `hoot list`, showing the Linux default `~/.hoot` state.
  - transcript for `hoot serve --bind 127.0.0.1:21000` and `curl localhost:21000/healthz`.
  - macOS transcript for `hoot attach --host devbox:21000 <key>` showing the remote session output/reconnect path works.
- Repo:
  - committed docs update in `hootty`.

## Open questions

None. If remote attach is hard to capture interactively in a transcript, use `script`/`timeout` and document the command and visible bytes.

## Design notes

- 2026-05-05T14:35Z - Picked Zig direct cross-build over Nix for this task.
  - Nix route investigated: Ghostty's `flake.nix` exposes `packages.<system>.libghostty-vt-*`, and `nix/libghostty-vt.nix` installs both `libghostty-vt.so` and a moved static archive in the `dev` output with pkg-config modules `libghostty-vt` and `libghostty-vt-static`.
  - Why it lost here: the flake does not expose a downstream Go cross-build wrapper or obvious `pkgsCross` package; it is keyed by Nix platform package sets. This host also has no `nix` executable, so using it would add a tool install and still leave the downstream cgo cross step to solve.
  - Zig route picked: Ghostty's own build is Zig-native and already has `-Dtarget`, `-Dapp-runtime=none`, and `-Demit-lib-vt=true`. `go-libghostty` uses pkg-config, so a Zig-built Linux prefix should be enough for Go cgo when paired with `CC="zig cc -target x86_64-linux-gnu"`.

- 2026-05-05T14:58Z - Kept the health check on `127.0.0.1:21000` but used `0.0.0.0:21000` for the cross-machine attach smoke.
  - The section's local HTTP smoke explicitly asks for `hoot serve --bind 127.0.0.1:21000` plus `curl localhost:21000/healthz` on devbox; that worked and proves the HTTP surface.
  - A loopback-only listener is not reachable from macOS as `devbox:21000`, so remote attach needs either an SSH tunnel or a non-loopback bind. I picked `0.0.0.0:21000` for the attach smoke because it exercises the real TCP path that `hoot attach --host devbox:21000` uses.
  - The docs call out this distinction so future users do not copy the loopback health-check bind and then wonder why remote attach cannot connect.
