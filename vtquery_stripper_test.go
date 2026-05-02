package supervisor

import (
	"bytes"
	"testing"
)

func TestVTQueryStripper_DropsKnownQueries(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"ENQ", "\x05"},
		{"DA1", "\x1b[c"},
		{"DA1_with_zero", "\x1b[0c"},
		{"DA1_private", "\x1b[?c"},
		{"DA2", "\x1b[>c"},
		{"DA3", "\x1b[=c"},
		{"XTVERSION", "\x1b[>q"},
		{"DSR_status", "\x1b[5n"},
		{"CPR", "\x1b[6n"},
		{"DECXCPR", "\x1b[?6n"},
		{"DECRQM", "\x1b[?2026$p"},
		{"DECRQM_multi_digit", "\x1b[?12345$p"},
		{"size_pixels", "\x1b[14t"},
		{"size_cells_per_char", "\x1b[16t"},
		{"size_screen_chars", "\x1b[18t"},
		{"size_screen_pixels", "\x1b[19t"},
		{"kitty_kbd_query", "\x1b[?u"},
		{"OSC10_query_BEL", "\x1b]10;?\x07"},
		{"OSC11_query_BEL", "\x1b]11;?\x07"},
		{"OSC12_query_ST", "\x1b]12;?\x1b\\"},
		{"OSC4_palette_query", "\x1b]4;5;?\x07"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s vtQueryStripper
			got := s.Filter([]byte(tc.in))
			if len(got) != 0 {
				t.Fatalf("expected empty output, got %q", got)
			}
		})
	}
}

func TestVTQueryStripper_PreservesNonQueries(t *testing.T) {
	cases := []string{
		"hello world\r\n",
		"\x1b[0m",         // SGR reset
		"\x1b[31mred\x1b[0m",
		"\x1b[H",          // CUP home
		"\x1b[2J",         // ED clear
		"\x1b[?1049h",     // alt screen enter (set, not query)
		"\x1b[?25l",       // hide cursor (set)
		"\x1b]0;title\x07", // OSC 0 set window title
		"\x1b]11;rgb:2828/2c2c/3434\x07", // OSC 11 set bg
		"\x1b]4;5;rgb:ff/00/00\x07",      // OSC 4 set palette
		"\x1bc",           // RIS reset
		"\x1b[u",          // restore cursor (NOT kitty query — no '?')
		"\x1b[2 q",        // cursor style (NOT XTVERSION)
		"\x1b[8;24;80t",   // window resize op (NOT a query)
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			var s vtQueryStripper
			got := s.Filter([]byte(tc))
			if !bytes.Equal(got, []byte(tc)) {
				t.Fatalf("input=%q\n  got=%q\n want=%q", tc, got, tc)
			}
		})
	}
}

func TestVTQueryStripper_MixedStream(t *testing.T) {
	// A realistic-ish chunk: tmux startup probes interleaved with
	// useful output and SGR.
	in := "hi\x1b[c more\x1b[>c stuff\x1b[31mred\x1b[0m\x1b[6nmore\x1b]11;?\x07!"
	want := "hi more stuff\x1b[31mred\x1b[0mmore!"
	var s vtQueryStripper
	got := s.Filter([]byte(in))
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("\n  got=%q\n want=%q", got, want)
	}
}

// TestVTQueryStripper_SplitAcrossReads exercises the streaming-state
// requirement called out in the spec: a query whose bytes are split
// across two Filter() calls must still be recognized and dropped.
func TestVTQueryStripper_SplitAcrossReads(t *testing.T) {
	type splitCase struct {
		name      string
		fragments []string
		want      string // concatenated expected output across all fragments
	}
	cases := []splitCase{
		{
			name:      "DA1_split_after_ESC",
			fragments: []string{"out\x1b", "[c more"},
			want:      "out more",
		},
		{
			name:      "DA1_split_after_CSI",
			fragments: []string{"out\x1b[", "c more"},
			want:      "out more",
		},
		{
			name:      "DA1_split_in_params",
			fragments: []string{"out\x1b[", "?", "c more"},
			want:      "out more",
		},
		{
			name:      "DECRQM_split_three_ways",
			fragments: []string{"a\x1b[?", "2026", "$p z"},
			want:      "a z",
		},
		{
			name:      "OSC11_query_split_at_terminator",
			fragments: []string{"x\x1b]11;?", "\x07y"},
			want:      "xy",
		},
		{
			name:      "OSC12_query_ST_split_between_ESC_and_backslash",
			fragments: []string{"x\x1b]12;?\x1b", "\\y"},
			want:      "xy",
		},
		{
			name:      "non-query_split_passes_through",
			fragments: []string{"\x1b[31", "mred\x1b[0m"},
			want:      "\x1b[31mred\x1b[0m",
		},
		{
			name:      "OSC11_set_split_passes_through",
			fragments: []string{"\x1b]11;rgb:11", "/22/33\x07"},
			want:      "\x1b]11;rgb:11/22/33\x07",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s vtQueryStripper
			var got bytes.Buffer
			for _, frag := range tc.fragments {
				got.Write(s.Filter([]byte(frag)))
			}
			if got.String() != tc.want {
				t.Fatalf("\n  got=%q\n want=%q", got.String(), tc.want)
			}
		})
	}
}

// TestVTQueryStripper_ConsumesBytewise feeds every input byte to its
// own Filter call, the harshest possible split. The stripper must
// produce identical output to a single bulk Filter call.
func TestVTQueryStripper_ConsumesBytewise(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\x1b[c done\x1b]11;?\x07tail\x05x"
	var bulk vtQueryStripper
	want := bulk.Filter([]byte(in))

	var s vtQueryStripper
	var got bytes.Buffer
	for i := 0; i < len(in); i++ {
		got.Write(s.Filter([]byte{in[i]}))
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("\n  got=%q\n want=%q", got.Bytes(), want)
	}
}
