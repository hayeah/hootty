package sessionpick

import (
	"errors"
	"strings"
	"testing"
	"time"

	session "github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/shortid"
)

func mk(key, cwd string, argv []string, attached bool, alive bool, host string) SessionWithMeta {
	st := &session.StateFile{
		Session: session.SessionState{
			Key:       key,
			CreatedAt: time.Unix(1700000000, 0),
			CWD:       cwd,
			Argv:      argv,
		},
	}
	if attached {
		st.Session.Attachments = []session.AttachmentRecord{{ID: "x"}}
	}
	return SessionWithMeta{State: st, Alive: alive, Host: host}
}

func TestFormat(t *testing.T) {
	t.Setenv("HOME", "/Users/me")

	tests := []struct {
		name string
		sm   SessionWithMeta
		want string
	}{
		{
			name: "alive attached local with tildified cwd",
			sm:   mk("a3fxx", "/Users/me/proj/api", []string{"bash", "--login"}, true, true, "local"),
			want: "[a3fxx]\t@local\t~/proj/api\tbash --login\t*attached",
		},
		{
			name: "alive idle (no tag)",
			sm:   mk("k7qzz", "/Users/me/notes", []string{"nvim", "spec.md"}, false, true, "local"),
			want: "[k7qzz]\t@local\t~/notes\tnvim spec.md\t",
		},
		{
			name: "dead session marked",
			sm:   mk("zz9aa", "/tmp", []string{"sh"}, false, false, "local"),
			want: "[zz9aa]\t@local\t/tmp\tsh\t(dead)",
		},
		{
			name: "remote host shown verbatim",
			sm:   mk("rm0bb", "/srv", []string{"top"}, false, true, "user@m4mini"),
			want: "[rm0bb]\t@user@m4mini\t/srv\ttop\t",
		},
		{
			name: "empty host falls back to local",
			sm:   mk("hh1cc", "/x", []string{"sh"}, false, true, ""),
			want: "[hh1cc]\t@local\t/x\tsh\t",
		},
		{
			name: "home itself tildifies",
			sm:   mk("hh2dd", "/Users/me", []string{"sh"}, false, true, "local"),
			want: "[hh2dd]\t@local\t~\tsh\t",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Format(tc.sm)
			if got != tc.want {
				t.Errorf("Format = %q\nwant     %q", got, tc.want)
			}
		})
	}
}

func TestFormatHUD(t *testing.T) {
	t.Setenv("HOME", "/Users/me")

	longArgv := []string{"bash", "-c", strings.Repeat("x ", 60)} // ~120 chars joined

	tests := []struct {
		name string
		sm   SessionWithMeta
		want string
	}{
		{
			name: "local session drops @host marker",
			sm:   mk("a3fxx", "/Users/me/proj/api", []string{"bash", "--login"}, false, true, "local"),
			want: "🦉 a3fxx ~/proj/api bash --login",
		},
		{
			name: "remote session keeps @host",
			sm:   mk("rm0bb", "/srv", []string{"top"}, false, true, "user@m4mini"),
			want: "🦉 rm0bb @user@m4mini /srv top",
		},
		{
			name: "empty host treated as local",
			sm:   mk("hh1cc", "/x", []string{"sh"}, false, true, ""),
			want: "🦉 hh1cc /x sh",
		},
		{
			name: "missing argv is omitted (no trailing space)",
			sm:   mk("nox00", "/Users/me", nil, false, true, "local"),
			want: "🦉 nox00 ~",
		},
		{
			name: "long argv truncated with single ellipsis rune",
			sm:   mk("trnc1", "/x", longArgv, false, true, "local"),
			// Cmd field capped at HUDMaxCmdLen (60) runes including the
			// trailing "…". 8 chars of "bash -c " + 25 "x " pairs + "x" + "…" = 60.
			want: "🦉 trnc1 /x bash -c x x x x x x x x x x x x x x x x x x x x x x x x x x…",
		},
		{
			name: "wide unicode in cwd survives intact",
			sm:   mk("uniq1", "/Users/me/プロジェクト", []string{"sh"}, false, true, "local"),
			want: "🦉 uniq1 ~/プロジェクト sh",
		},
		{
			name: "spaces in argv are preserved",
			sm:   mk("spc01", "/x", []string{"sh", "-c", "echo hi"}, false, true, "local"),
			want: "🦉 spc01 /x sh -c echo hi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatHUD(tc.sm)
			if got != tc.want {
				t.Errorf("FormatHUD =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}

	t.Run("nil state returns empty", func(t *testing.T) {
		if got := FormatHUD(SessionWithMeta{}); got != "" {
			t.Errorf("FormatHUD(empty) = %q, want \"\"", got)
		}
	})

	t.Run("truncation is rune-safe (no split mid-rune)", func(t *testing.T) {
		// Argv whose joined string lands the truncation boundary inside
		// a multi-byte rune. We assert no replacement-rune ('�')
		// appears in the output.
		sm := mk("rune1", "/x", []string{strings.Repeat("é", 200)}, false, true, "local")
		got := FormatHUD(sm)
		if strings.ContainsRune(got, '�') {
			t.Errorf("FormatHUD produced replacement rune: %q", got)
		}
	})
}

func TestIDResolve(t *testing.T) {
	states := []SessionWithMeta{
		mk("a3fabc", "/x", nil, false, true, "local"),
		mk("a3fxyz", "/y", nil, false, true, "local"),
		mk("k7qzzz", "/z", nil, false, true, "local"),
	}

	t.Run("unique prefix returns the session", func(t *testing.T) {
		got, err := IDResolve(states, "k7q")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got.State.Session.Key != "k7qzzz" {
			t.Fatalf("got key %q, want k7qzzz", got.State.Session.Key)
		}
	})

	t.Run("ambiguous prefix returns AmbiguousIDError", func(t *testing.T) {
		_, err := IDResolve(states, "a3f")
		var amb *shortid.AmbiguousIDError
		if !errors.As(err, &amb) {
			t.Fatalf("got %T (%v), want *AmbiguousIDError", err, err)
		}
	})

	t.Run("no match returns IDNotFoundError", func(t *testing.T) {
		_, err := IDResolve(states, "zzzzz")
		var nf *shortid.IDNotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("got %T (%v), want *IDNotFoundError", err, err)
		}
	})

	t.Run("too-short query returns IDTooShortError", func(t *testing.T) {
		_, err := IDResolve(states, "a")
		var ts *shortid.IDTooShortError
		if !errors.As(err, &ts) {
			t.Fatalf("got %T (%v), want *IDTooShortError", err, err)
		}
	})

	t.Run("exact full key matches even when other keys share prefix", func(t *testing.T) {
		got, err := IDResolve(states, "a3fabc")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got.State.Session.Key != "a3fabc" {
			t.Fatalf("got %q, want a3fabc", got.State.Session.Key)
		}
	})
}
