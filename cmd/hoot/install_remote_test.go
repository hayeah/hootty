package main

import "testing"

func TestParseSSHTarget(t *testing.T) {
	cases := []struct {
		raw                string
		wantUser, wantHost string
		wantPort           string
		wantErr            bool
	}{
		{"devbox", "", "devbox", "", false},
		{"alice@devbox", "alice", "devbox", "", false},
		{"devbox:2222", "", "devbox", "2222", false},
		{"alice@devbox:2222", "alice", "devbox", "2222", false},
		{"ssh://devbox", "", "devbox", "", false},
		{"ssh://alice@devbox:2222", "alice", "devbox", "2222", false},

		{"http://devbox", "", "", "", true}, // wrong scheme
		{"", "", "", "", true},              // empty
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			user, host, port, err := parseSSHTarget(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSSHTarget(%q): want error, got user=%q host=%q port=%q", tc.raw, user, host, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSSHTarget(%q): unexpected error: %v", tc.raw, err)
			}
			if user != tc.wantUser || host != tc.wantHost || port != tc.wantPort {
				t.Fatalf("parseSSHTarget(%q): got (%q,%q,%q), want (%q,%q,%q)",
					tc.raw, user, host, port, tc.wantUser, tc.wantHost, tc.wantPort)
			}
		})
	}
}
