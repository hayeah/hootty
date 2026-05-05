package session

import (
	"bytes"
	"testing"
)

func TestVTPrimaryScreenFilter_PassesPrimaryBytes(t *testing.T) {
	in := "hello\r\n\x1b[31mred\x1b[0m\r\n\x1b]0;title\x07done\r\n"
	var f vtPrimaryScreenFilter
	got := f.Filter([]byte(in))
	if !bytes.Equal(got, []byte(in)) {
		t.Fatalf("\n  got=%q\n want=%q", got, in)
	}
}

func TestVTPrimaryScreenFilter_StripsAlternateScreenContent(t *testing.T) {
	in := "primary-before\r\n" +
		"\x1b[?1049h" +
		"ALT-PANEL\r\nALT-ROW\r\n" +
		"\x1b[?1049l" +
		"primary-after\r\n"
	want := "primary-before\r\nprimary-after\r\n"

	var f vtPrimaryScreenFilter
	got := f.Filter([]byte(in))
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("\n  got=%q\n want=%q", got, want)
	}
}

func TestVTPrimaryScreenFilter_HandlesLegacyAlternateModes(t *testing.T) {
	for _, mode := range []string{"47", "1047", "1049"} {
		t.Run(mode, func(t *testing.T) {
			in := "a\x1b[?" + mode + "hALT\x1b[?" + mode + "lb"
			var f vtPrimaryScreenFilter
			got := f.Filter([]byte(in))
			if string(got) != "ab" {
				t.Fatalf("got %q, want %q", got, "ab")
			}
		})
	}
}

func TestVTPrimaryScreenFilter_SplitAcrossReads(t *testing.T) {
	fragments := []string{
		"primary\x1b",
		"[?104",
		"9hALT",
		"\x1b[?104",
		"9lafter",
	}
	var f vtPrimaryScreenFilter
	var got bytes.Buffer
	for _, frag := range fragments {
		got.Write(f.Filter([]byte(frag)))
	}
	if got.String() != "primaryafter" {
		t.Fatalf("got %q, want %q", got.String(), "primaryafter")
	}
}

func TestVTPrimaryScreenFilter_MultiParamAltMode(t *testing.T) {
	in := "a\x1b[?25;1049hALT\x1b[?1049;25lb"
	var f vtPrimaryScreenFilter
	got := f.Filter([]byte(in))
	if string(got) != "ab" {
		t.Fatalf("got %q, want %q", got, "ab")
	}
}

func TestVTPrimaryScreenFilter_NonAltPrivateModesPassOnPrimary(t *testing.T) {
	in := "a\x1b[?25lhidden\x1b[?25hshown"
	var f vtPrimaryScreenFilter
	got := f.Filter([]byte(in))
	if !bytes.Equal(got, []byte(in)) {
		t.Fatalf("\n  got=%q\n want=%q", got, in)
	}
}
