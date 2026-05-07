package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeArgvEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []byte
	}{
		{"empty", []string{""}, []byte{}},
		{"plain", []string{"hello"}, []byte("hello")},
		{"cr", []string{`hello\r`}, []byte("hello\r")},
		{"hex_esc", []string{`\x1b[A`}, []byte{0x1b, '[', 'A'}},
		{"ctrl_c", []string{`\x03`}, []byte{0x03}},
		{"join_no_separator", []string{"foo", "bar"}, []byte("foobar")},
		{"join_with_escapes_split", []string{`echo hi`, `\r`}, []byte("echo hi\r")},
		{"unicode_4hex", []string{`ÿ`}, []byte{0xc3, 0xbf}},
		{"unicode_8hex", []string{`\U0001F600`}, []byte("\xf0\x9f\x98\x80")},
		{"octal", []string{`\033`}, []byte{0x1b}},
		{"backslash_dquote", []string{`\\\"`}, []byte(`\"`)},
		{"all_letters", []string{`\a\b\f\n\r\t\v`}, []byte{0x07, 0x08, 0x0c, '\n', '\r', '\t', 0x0b}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeArgvEscapes(tc.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeArgvEscapes_Errors(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantHint  bool
		wantInErr string
	}{
		{"bad_escape_e_gets_hint", []string{`hi\efoo`}, true, "use \\x1b"},
		{"truncated_hex", []string{`\x1`}, false, "invalid escape"},
		{"unknown_escape", []string{`\q`}, false, "invalid escape"},
		{"bare_dquote_unbalanced", []string{`he"llo`}, false, "invalid escape"},
		{"newline_literal_in_argv", []string{"line\nline"}, false, "invalid escape"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeArgvEscapes(tc.in)
			if err == nil {
				t.Fatalf("expected error")
			}
			if tc.wantInErr != "" && !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("err = %q, want substring %q", err, tc.wantInErr)
			}
			if tc.wantHint && !strings.Contains(err.Error(), "\\x1b") {
				t.Fatalf("expected ESC hint, got %q", err)
			}
		})
	}
}
