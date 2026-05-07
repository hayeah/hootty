package main

import (
	"fmt"
	"strconv"
	"strings"
)

// decodeArgvEscapes joins argv tokens with no separator and runs the
// result through Go's string-literal interpreter (`strconv.Unquote`).
// The grammar users get is exactly what Go accepts inside `"…"`:
// `\xHH`, `\uXXXX`, `\UXXXXXXXX`, octal `\NNN`, the standard letter
// escapes (`\a \b \f \n \r \t \v`), `\\`, `\"`, `\'`. Anything else
// is a parse error.
//
// One Go-specific gotcha worth surfacing: `\e` is NOT in Go's grammar,
// even though it is in printf / bash $'…' / many other places. When
// the parser fails on `\e`, we attach a helper hint so the user
// knows to write `\x1b` instead.
//
// Returns the decoded byte slice, or a wrapped error suitable for
// CLI exit-code 2.
func decodeArgvEscapes(argv []string) ([]byte, error) {
	src := `"` + strings.Join(argv, "") + `"`
	out, err := strconv.Unquote(src)
	if err != nil {
		hint := ""
		// strings.Contains here is a syntactic check on the input,
		// not a guarantee about which escape strconv objected to;
		// it's a heuristic that catches the common case cheaply.
		if strings.Contains(strings.Join(argv, ""), `\e`) {
			hint = " (use \\x1b for ESC; Go string literals do not support \\e)"
		}
		return nil, fmt.Errorf("invalid escape: %w%s", err, hint)
	}
	return []byte(out), nil
}
