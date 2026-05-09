package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// TestInstallShStaysInSync asserts that internal/bootstrap/install.sh is a
// byte-identical copy of the repo-root install.sh. The repo-root file is
// what users curl-pipe; the package-local copy is what we //go:embed into
// the hoot binary. If they ever diverge, `hoot install-remote` would ship
// a different installer than the documented one, which is exactly the
// kind of subtle drift that becomes a long debugging session later.
//
// The release task also re-runs this check before publishing, so an
// out-of-sync state can't reach a tag.
func TestInstallShStaysInSync(t *testing.T) {
	pkg, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("read package install.sh: %v", err)
	}
	root, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("read repo-root install.sh: %v", err)
	}
	if string(pkg) != string(root) {
		t.Fatalf("internal/bootstrap/install.sh is out of sync with repo-root install.sh.\n" +
			"Run: cp install.sh internal/bootstrap/install.sh")
	}
}

// TestInstallScriptEmbedded asserts the embed picked up the actual file
// (not an empty placeholder) and that the bytes match what we'd read off disk.
func TestInstallScriptEmbedded(t *testing.T) {
	if len(installScript) == 0 {
		t.Fatal("installScript is empty; //go:embed didn't pick up install.sh")
	}
	got := InstallScript()
	disk, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	if !reflect.DeepEqual([]byte(got), disk) {
		t.Fatal("InstallScript() bytes diverge from install.sh on disk")
	}
}

func TestSshArgs(t *testing.T) {
	cases := []struct {
		name string
		user string
		host string
		port string
		want []string
	}{
		{"host only", "", "devbox", "", []string{"-o", "BatchMode=yes", "devbox"}},
		{"user@host", "alice", "devbox", "", []string{"-o", "BatchMode=yes", "alice@devbox"}},
		{"user + port", "alice", "devbox", "2222", []string{"-o", "BatchMode=yes", "-p", "2222", "alice@devbox"}},
		{"port no user", "", "devbox", "2222", []string{"-o", "BatchMode=yes", "-p", "2222", "devbox"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sshArgs(tc.user, tc.host, tc.port)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sshArgs(%q,%q,%q) = %v, want %v",
					tc.user, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"foo", `'foo'`},
		{"hello world", `'hello world'`},
		{"it's", `'it'\''s'`},
		{"--from-file=/tmp/x", `'--from-file=/tmp/x'`},
		{"", `''`},
	}
	for _, tc := range cases {
		got := shellQuote(tc.in)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestModeString(t *testing.T) {
	cases := map[Mode]string{
		ModeAuto:     "auto",
		ModeUpload:   "upload",
		ModeFromFile: "from-file",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", int(m), got, want)
		}
	}
}

// TestVerifySHA covers the local checksum verification used by ModeUpload.
func TestVerifySHA(t *testing.T) {
	dir := t.TempDir()
	body := []byte("the quick brown fox jumps over the lazy dog")
	tarball := filepath.Join(dir, "hoot-darwin-arm64.tar.gz")
	if err := os.WriteFile(tarball, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	hex := hex.EncodeToString(sum[:])

	// happy path
	chk := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(chk, []byte(hex+"  hoot-darwin-arm64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := verifySHA(tarball, chk, "hoot-darwin-arm64.tar.gz")
	if err != nil || !ok {
		t.Fatalf("verifySHA happy path: ok=%v err=%v", ok, err)
	}

	// wrong sha
	if err := os.WriteFile(chk,
		[]byte("0000000000000000000000000000000000000000000000000000000000000000  hoot-darwin-arm64.tar.gz\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = verifySHA(tarball, chk, "hoot-darwin-arm64.tar.gz")
	if err != nil {
		t.Fatalf("verifySHA mismatch should not error: %v", err)
	}
	if ok {
		t.Fatal("verifySHA returned true on mismatch")
	}

	// missing entry
	if err := os.WriteFile(chk, []byte("aabb  some-other-archive.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifySHA(tarball, chk, "hoot-darwin-arm64.tar.gz"); err == nil {
		t.Fatal("verifySHA should error on missing entry")
	}

	_ = runtime.GOOS // touch import to keep go fmt happy if conditional tests appear later
}
