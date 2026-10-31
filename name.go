package xiaomidrive

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Name rules the service enforces (api-notes §4.5). Validating locally avoids a
// round-trip and gives a clearer error than the server's 10002.
const (
	// MaxNameRunes is the character-count limit (201 ASCII chars are rejected).
	MaxNameRunes = 200
	// MaxNameBytes is the UTF-8 byte-count limit. The true cutoff is somewhere
	// in 381-383; 381 is used as the safe bound.
	MaxNameBytes = 381
)

// illegalNameChars are rejected anywhere in a name.
const illegalNameChars = `/\:*?<>|"` + "\t\n\r\x00"

// ValidateName reports whether name is acceptable to the service. It returns an
// error wrapping ErrInvalidName otherwise.
func ValidateName(name string) error {
	switch name {
	case "", ".", "..", "...":
		return fmt.Errorf("%w: %q is reserved", ErrInvalidName, name)
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: leading or trailing spaces", ErrInvalidName)
	}
	if n := utf8.RuneCountInString(name); n > MaxNameRunes {
		return fmt.Errorf("%w: %d characters exceeds %d", ErrInvalidName, n, MaxNameRunes)
	}
	if n := len(name); n > MaxNameBytes {
		return fmt.Errorf("%w: %d UTF-8 bytes exceeds %d", ErrInvalidName, n, MaxNameBytes)
	}
	if i := strings.IndexAny(name, illegalNameChars); i >= 0 {
		r, _ := utf8.DecodeRuneInString(name[i:])
		return fmt.Errorf("%w: contains %q", ErrInvalidName, r)
	}
	return nil
}

// SanitizeName maps a local file name to one the service accepts, for use when
// mounting a foreign file system. Illegal characters become the Unicode
// full-width equivalent (which the service allows), and the result is trimmed
// and length-capped. It never returns a reserved name.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/':
			b.WriteRune('／')
		case '\\':
			b.WriteRune('＼')
		case ':':
			b.WriteRune('：')
		case '*':
			b.WriteRune('＊')
		case '?':
			b.WriteRune('？')
		case '<':
			b.WriteRune('＜')
		case '>':
			b.WriteRune('＞')
		case '|':
			b.WriteRune('｜')
		case '"':
			b.WriteRune('＂')
		case '\t', '\n', '\r', '\x00':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = truncateToLimits(out)
	switch out {
	case "", ".", "..", "...":
		return "_" + out
	}
	return out
}

func truncateToLimits(s string) string {
	// Cap on bytes first, then on runes, keeping whole runes.
	for len(s) > MaxNameBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	for utf8.RuneCountInString(s) > MaxNameRunes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}
