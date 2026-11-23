package recipe

import (
	"strings"

	"quack/internal/wire"
)

type Recipe struct {
	Name        string
	Description string
	Params      []wire.Param
	// The absolute path of the file whose code declares it: a mapped file, or one that a mapped file imports. It is
	// the mapped file when the source map does not tell.
	File   string
	script string
	env    map[string]string
}

// Follows the rules of action ids: ASCII letters, digits, '_', '.' and '-', and no '-' first, which would read as a flag.
func isName(name string) bool {
	return name != "" && name[0] != '-' && strings.IndexFunc(name, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("_.-", r))
	}) < 0
}
