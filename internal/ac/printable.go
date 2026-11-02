package ac

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Printable makes decoded (untrusted) text safe for a terminal: control chars,
// C1, bidi overrides and invalid UTF-8 become visible escapes, so what is shown
// is exactly what is stored. Newlines and tabs are kept.
func Printable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f),
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}
