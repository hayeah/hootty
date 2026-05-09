#!/bin/sh
# install.sh — install hoot from a github.com/hayeah/hootty release.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/hayeah/hootty/master/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/hayeah/hootty/master/install.sh | sh -s -- --version v0.0.1
#   sh install.sh --install-dir /usr/local/bin --version v0.0.1
#   sh install.sh --from-file /tmp/hoot-darwin-arm64.tar.gz   # skip download, use a local tarball
#
# Env:
#   HOOT_INSTALL_DIR  Destination directory for the binary. Default: ~/.local/bin.
#
# Exit status: 0 on success, non-zero on any failure (no partial installs).

set -eu

REPO="hayeah/hootty"
GITHUB="https://github.com"
GH_API="https://api.github.com"

VERSION=""
FROM_FILE=""
INSTALL_DIR="${HOOT_INSTALL_DIR:-$HOME/.local/bin}"

usage() {
    cat <<EOF
install.sh — install hoot from a github.com/$REPO release.

Usage:
  install.sh [--version vX.Y.Z] [--install-dir DIR]

Options:
  --version vX.Y.Z    Install a specific tag (default: latest release).
  --from-file PATH    Skip download; use this local hoot-<os>-<arch>.tar.gz.
                      The tarball must already match the local platform.
  --install-dir DIR   Where to put the hoot binary (default: \$HOOT_INSTALL_DIR
                      or ~/.local/bin).
  -h, --help          Print this help.
EOF
}

err() {
    printf 'install.sh: %s\n' "$1" >&2
    exit 1
}

# Argument parse — POSIX, no getopts long-option support so do it by hand.
while [ $# -gt 0 ]; do
    case "$1" in
        --version)
            [ $# -ge 2 ] || err "--version requires a value (e.g. v0.0.1)"
            VERSION="$2"
            shift 2
            ;;
        --version=*)
            VERSION="${1#--version=}"
            shift
            ;;
        --from-file)
            [ $# -ge 2 ] || err "--from-file requires a path"
            FROM_FILE="$2"
            shift 2
            ;;
        --from-file=*)
            FROM_FILE="${1#--from-file=}"
            shift
            ;;
        --install-dir)
            [ $# -ge 2 ] || err "--install-dir requires a value"
            INSTALL_DIR="$2"
            shift 2
            ;;
        --install-dir=*)
            INSTALL_DIR="${1#--install-dir=}"
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            err "unknown argument: $1 (try --help)"
            ;;
    esac
done

# --- detect platform ---------------------------------------------------------

uname_s=$(uname -s)
uname_m=$(uname -m)

case "$uname_s" in
    Darwin) OS=darwin ;;
    Linux)  OS=linux ;;
    *) err "unsupported OS '$uname_s' (hoot ships darwin and linux only)" ;;
esac

case "$uname_m" in
    arm64|aarch64) ARCH=arm64 ;;
    x86_64|amd64)  ARCH=amd64 ;;
    *) err "unsupported arch '$uname_m'" ;;
esac

# darwin-amd64 is intentionally not built (Apple Silicon target only).
if [ "$OS" = "darwin" ] && [ "$ARCH" = "amd64" ]; then
    err "darwin-amd64 is not currently shipped (arm64 only). See https://github.com/$REPO."
fi

TARGET="$OS-$ARCH"

# --- prepare workspace -------------------------------------------------------

# Cross-platform mktemp (Linux supports -d -t TEMPLATE but macOS differs).
TMP=$(mktemp -d 2>/dev/null || mktemp -d -t hoot-install)
trap 'rm -rf "$TMP"' EXIT INT HUP TERM

ARCHIVE="hoot-$TARGET.tar.gz"

if [ -n "$FROM_FILE" ]; then
    # --- from-file mode: skip download + checksum, use local tarball -------
    [ -f "$FROM_FILE" ] || err "--from-file path does not exist: $FROM_FILE"
    cp "$FROM_FILE" "$TMP/$ARCHIVE" || err "could not stage tarball from $FROM_FILE"
    printf 'install.sh: target=%s using --from-file=%s\n' "$TARGET" "$FROM_FILE" >&2
else
    # --- download mode ------------------------------------------------------
    if [ -z "$VERSION" ]; then
        # Parse tag_name from the latest-release JSON without depending on jq.
        VERSION=$(curl -fsSL "$GH_API/repos/$REPO/releases/latest" \
            | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
            | head -n 1)
        [ -n "$VERSION" ] || err "could not resolve latest release for $REPO (no published stable release yet?)"
    fi

    case "$VERSION" in
        v*) ;;
        *) err "version must start with 'v' (got '$VERSION')" ;;
    esac

    URL_BASE="$GITHUB/$REPO/releases/download/$VERSION"
    printf 'install.sh: target=%s version=%s\n' "$TARGET" "$VERSION" >&2
    printf 'install.sh: downloading %s/%s\n' "$URL_BASE" "$ARCHIVE" >&2

    curl -fsSL --output "$TMP/$ARCHIVE" "$URL_BASE/$ARCHIVE" \
        || err "download failed: $URL_BASE/$ARCHIVE"
    curl -fsSL --output "$TMP/checksums.txt" "$URL_BASE/checksums.txt" \
        || err "download failed: $URL_BASE/checksums.txt"

    # Pick whichever of shasum / sha256sum is available.
    if command -v shasum >/dev/null 2>&1; then
        SHASUM="shasum -a 256"
    elif command -v sha256sum >/dev/null 2>&1; then
        SHASUM="sha256sum"
    else
        err "neither shasum nor sha256sum is available; cannot verify checksum"
    fi

    # Pull the line for our archive out of checksums.txt so the verify command
    # only sees it; otherwise -c complains about missing files for other arches.
    grep " $ARCHIVE\$" "$TMP/checksums.txt" > "$TMP/checksums.this" \
        || err "no checksum entry for $ARCHIVE in checksums.txt"

    (cd "$TMP" && $SHASUM -c checksums.this >/dev/null) \
        || err "checksum verification failed for $ARCHIVE"
fi

# --- extract + install ------------------------------------------------------

(cd "$TMP" && tar -xzf "$ARCHIVE") || err "extract failed"
[ -f "$TMP/hoot" ] || err "expected 'hoot' binary in tarball but did not find it"

mkdir -p "$INSTALL_DIR" || err "could not create $INSTALL_DIR"
chmod 0755 "$TMP/hoot"
# Atomic install: mv onto the final path. Within one filesystem this is
# rename(2), so an existing hoot in $INSTALL_DIR is replaced atomically.
mv -f "$TMP/hoot" "$INSTALL_DIR/hoot" \
    || err "could not install to $INSTALL_DIR/hoot"

printf 'install.sh: installed %s to %s/hoot\n' "${VERSION:-from-file}" "$INSTALL_DIR" >&2

# --- PATH hint --------------------------------------------------------------

case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        ;;
    *)
        printf '\n%s is not on your PATH. Add the following to your shell rc:\n' "$INSTALL_DIR" >&2
        printf '    export PATH="%s:$PATH"\n\n' "$INSTALL_DIR" >&2
        ;;
esac
