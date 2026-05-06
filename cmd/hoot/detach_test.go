package main

import "testing"

func TestSplitDetachArg(t *testing.T) {
	tests := []struct {
		in       string
		wantSess string
		wantAtt  string
		wantErr  bool
	}{
		{"abc", "abc", "", false},
		{"abc/def", "abc", "def", false},
		{"abcdef/x9k", "abcdef", "x9k", false},
		{"", "", "", true},
		{"/abc", "", "", true},
		{"abc/", "", "", true},
		{"a/b/c", "", "", true},
		{"//", "", "", true},
	}
	for _, tc := range tests {
		gotSess, gotAtt, err := splitDetachArg(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("splitDetachArg(%q) err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitDetachArg(%q): unexpected err: %v", tc.in, err)
			continue
		}
		if gotSess != tc.wantSess || gotAtt != tc.wantAtt {
			t.Errorf("splitDetachArg(%q) = (%q, %q), want (%q, %q)", tc.in, gotSess, gotAtt, tc.wantSess, tc.wantAtt)
		}
	}
}
