# Installing hoot

`hoot` ships as a single binary per supported platform (darwin-arm64,
linux-amd64, linux-arm64). Linux binaries are fully static (musl) — no
glibc floor, runs on Alpine and distroless containers as well as anything
glibc-based.

There are three ways in.

## Curl-pipe (one-line install)

```sh
curl -fsSL https://raw.githubusercontent.com/hayeah/hootty/master/install.sh | sh
```

What it does:

- Detects OS+arch via `uname -sm`. Refuses anything outside
  `{darwin,linux} × {amd64,arm64}` (and `darwin-amd64` specifically — only
  Apple Silicon ships in v0.0.x).
- Resolves the latest *stable* release from `api.github.com` and downloads
  `hoot-<os>-<arch>.tar.gz` plus `checksums.txt`.
- Verifies the SHA-256, extracts, atomically `mv`s `hoot` into
  `${HOOT_INSTALL_DIR:-$HOME/.local/bin}`.
- Prints a PATH hint if the install dir isn't on `$PATH`.

Pin a specific tag:

```sh
curl -fsSL https://raw.githubusercontent.com/hayeah/hootty/master/install.sh \
    | sh -s -- --version v0.0.1
```

Override the install dir:

```sh
HOOT_INSTALL_DIR=/usr/local/bin curl -fsSL ...install.sh | sh
# or
curl -fsSL ...install.sh | sh -s -- --install-dir /usr/local/bin
```

## `hoot install-remote <host>`

Installs or upgrades hoot on a remote SSH host. Three modes mirror the
behavior of Zed's `upload_binary_over_ssh` toggle.

```sh
hoot install-remote devbox                         # default: ModeAuto
hoot install-remote --upload devbox                # ModeUpload
hoot install-remote --from-file ./hoot.tar.gz devbox   # ModeFromFile
```

| Mode      | Where the tarball comes from                        | When to use                                         |
| --------- | --------------------------------------------------- | --------------------------------------------------- |
| Auto      | Remote downloads from GitHub directly via curl      | Default. Lowest local bandwidth, quickest setup.    |
| Upload    | Local downloads (with checksum), scps to remote     | Restricted-egress hosts that can't reach GitHub.    |
| FromFile  | Caller-supplied local tarball; scps + extracts      | Dev iteration; testing a build before tagging.      |

Default version is the local CLI's compiled-in tag (Zed-style: client and
helper move in lockstep). Override with `--version vX.Y.Z`.

Host accepts `host`, `user@host`, `host:port`, `user@host:port`, or an
explicit `ssh://...`. SSH key/agent auth is required — interactive password
prompts are disabled (BatchMode=yes) so a `hoot @host` auto-bootstrap can't
silently hang on a prompt nobody sees.

Heads-up: stdlib `flag` doesn't accept flags after positionals — put the
flags before the host (`hoot install-remote --upload devbox`, not `hoot
install-remote devbox --upload`).

## `hoot @host` auto-bootstrap

`hoot @host` (and any `hoot <subcommand> --remote ssh://...`) probes the
remote for `hoot version` before opening the SSH tunnel. If the version
doesn't match the local CLI — or hoot is missing entirely — it pipes the
embedded `install.sh` over ssh to install/upgrade, then re-probes once.
Failures (no curl on remote, no writable bin dir, post-install version
mismatch) surface inline rather than becoming an opaque tunnel-startup
timeout.

To opt out — e.g. when you know the remote has an older version that's
intentionally pinned — set `HOOT_NO_BOOTSTRAP=1`.

```sh
HOOT_NO_BOOTSTRAP=1 hoot @devbox
```

## Manual install

Download from <https://github.com/hayeah/hootty/releases>, pick the
matching `hoot-<os>-<arch>.tar.gz`, verify against `checksums.txt`,
extract, drop the `hoot` binary somewhere on `$PATH`.

## Verifying the install

```sh
hoot version          # prints the embedded tag, e.g. v0.0.1
```

This is also what `hoot install-remote` and `hoot @host` use to detect a
version mismatch on the remote — the output is exactly the tag, one line,
no decoration, suitable for plain-string comparison.

## Build from source

See the README's Build section. cgo + Zig + CMake + a pinned Ghostty
checkout — non-trivial; prefer a release binary unless you have a reason.
