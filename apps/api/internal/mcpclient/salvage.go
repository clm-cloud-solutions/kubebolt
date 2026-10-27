package mcpclient

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// salvageText recovers the start of a tool's output from a tools/call reply
// cut at the byte cap. The envelope no longer parses, but the payload is the
// first content block's "text" string, and a JSON string can be decoded up to
// wherever it was cut. When no text value is found the partial body itself is
// returned, as valid UTF-8, so the caller still gets something honest.
func salvageText(partial []byte) string {
	start := bytes.Index(partial, []byte(`"content"`))
	if start < 0 {
		start = 0
	}
	rest := partial[start:]
	for {
		i := bytes.Index(rest, []byte(`"text"`))
		if i < 0 {
			break
		}
		after := bytes.TrimLeft(rest[i+len(`"text"`):], " \t\r\n")
		if len(after) > 0 && after[0] == ':' {
			after = bytes.TrimLeft(after[1:], " \t\r\n")
			if len(after) > 0 && after[0] == '"' {
				return decodeJSONStringPrefix(after[1:])
			}
		}
		rest = rest[i+len(`"text"`):]
	}
	return strings.ToValidUTF8(string(partial), "")
}

// decodeJSONStringPrefix decodes the body of a JSON string (after its opening
// quote) until the closing quote or the end of the input, whichever comes
// first. An escape sequence cut by the end of input is dropped.
func decodeJSONStringPrefix(b []byte) string {
	var out strings.Builder
	out.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '"':
			return out.String()
		case c == '\\':
			if i+1 >= len(b) {
				return out.String()
			}
			switch b[i+1] {
			case '"', '\\', '/':
				out.WriteByte(b[i+1])
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'u':
				r, n := decodeUnicodeEscape(b[i:])
				if n == 0 {
					return out.String()
				}
				out.WriteRune(r)
				i += n
				continue
			default:
				out.WriteByte(b[i+1])
			}
			i += 2
		default:
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size <= 1 {
				if !utf8.FullRune(b[i:]) {
					return out.String() // a multi-byte rune cut in half
				}
				i++
				continue
			}
			out.WriteRune(r)
			i += size
		}
	}
	return out.String()
}

// decodeUnicodeEscape decodes \uXXXX (and a following low surrogate) at the
// start of b. It returns the rune and the bytes consumed, or 0 consumed when
// the escape is incomplete.
func decodeUnicodeEscape(b []byte) (rune, int) {
	if len(b) < 6 {
		return 0, 0
	}
	v, err := strconv.ParseUint(string(b[2:6]), 16, 16)
	if err != nil {
		return utf8.RuneError, 6
	}
	r := rune(v)
	if utf16.IsSurrogate(r) {
		if len(b) < 12 {
			return 0, 0
		}
		if b[6] == '\\' && b[7] == 'u' {
			if lo, err := strconv.ParseUint(string(b[8:12]), 16, 16); err == nil {
				if dec := utf16.DecodeRune(r, rune(lo)); dec != utf8.RuneError {
					return dec, 12
				}
			}
		}
		return utf8.RuneError, 6
	}
	return r, 6
}
