package supervisor

import (
	"strings"

	libghostty "github.com/mitchellh/go-libghostty"
)

// keyNameToCode translates a tmux-style key name (e.g. "Enter",
// "C-a", "M-Left", "hello") to a libghostty (Key, Mods) pair.
// Returns known=false if the name is a literal string to type (the
// caller should fall through to raw Write).
//
// Supported prefixes: C- (Ctrl), M- (Alt/Meta), S- (Shift), D-
// (Super). Stack freely: "C-M-Left", "S-Tab". Unknown base names
// return known=false.
func keyNameToCode(name string) (libghostty.Key, libghostty.Mods, bool) {
	var mods libghostty.Mods
	s := name
	for {
		switch {
		case strings.HasPrefix(s, "C-"):
			mods |= libghostty.ModCtrl
			s = s[2:]
		case strings.HasPrefix(s, "M-"):
			mods |= libghostty.ModAlt
			s = s[2:]
		case strings.HasPrefix(s, "S-"):
			mods |= libghostty.ModShift
			s = s[2:]
		case strings.HasPrefix(s, "D-"):
			mods |= libghostty.ModSuper
			s = s[2:]
		default:
			goto lookup
		}
	}
lookup:
	if code, ok := namedKeys[s]; ok {
		return code, mods, true
	}
	// Single ASCII letter with modifiers → KeyA..KeyZ.
	if mods != 0 && len(s) == 1 {
		c := s[0]
		switch {
		case c >= 'a' && c <= 'z':
			return libghostty.Key(int(libghostty.KeyA) + int(c-'a')), mods, true
		case c >= 'A' && c <= 'Z':
			mods |= libghostty.ModShift
			return libghostty.Key(int(libghostty.KeyA) + int(c-'A')), mods, true
		case c >= '0' && c <= '9':
			return libghostty.Key(int(libghostty.KeyDigit0) + int(c-'0')), mods, true
		}
	}
	return 0, 0, false
}

// namedKeys maps symbolic key names to libghostty.Key values. A
// subset of tmux's vocabulary plus common synonyms (Esc / Escape,
// BSpace / Backspace).
var namedKeys = map[string]libghostty.Key{
	"Enter":     libghostty.KeyEnter,
	"Return":    libghostty.KeyEnter,
	"Tab":       libghostty.KeyTab,
	"Space":     libghostty.KeySpace,
	"Escape":    libghostty.KeyEscape,
	"Esc":       libghostty.KeyEscape,
	"Backspace": libghostty.KeyBackspace,
	"BSpace":    libghostty.KeyBackspace,
	"Up":        libghostty.KeyArrowUp,
	"Down":      libghostty.KeyArrowDown,
	"Left":      libghostty.KeyArrowLeft,
	"Right":     libghostty.KeyArrowRight,
	"Home":      libghostty.KeyHome,
	"End":       libghostty.KeyEnd,
	"PageUp":    libghostty.KeyPageUp,
	"PPage":     libghostty.KeyPageUp,
	"PageDown":  libghostty.KeyPageDown,
	"NPage":     libghostty.KeyPageDown,
	"Insert":    libghostty.KeyInsert,
	"IC":        libghostty.KeyInsert,
	"Delete":    libghostty.KeyDelete,
	"DC":        libghostty.KeyDelete,
	"F1":        libghostty.KeyF1,
	"F2":        libghostty.KeyF2,
	"F3":        libghostty.KeyF3,
	"F4":        libghostty.KeyF4,
	"F5":        libghostty.KeyF5,
	"F6":        libghostty.KeyF6,
	"F7":        libghostty.KeyF7,
	"F8":        libghostty.KeyF8,
	"F9":        libghostty.KeyF9,
	"F10":       libghostty.KeyF10,
	"F11":       libghostty.KeyF11,
	"F12":       libghostty.KeyF12,
}
