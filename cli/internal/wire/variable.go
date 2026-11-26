package wire

import "unicode"

// IsVariable reports whether name is an ASCII environment variable name.
func IsVariable(name string) bool {
	for i, r := range name {
		if r != '_' && !(r < unicode.MaxASCII && (unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return name != ""
}
