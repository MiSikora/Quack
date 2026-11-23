package command

import (
	"errors"
	"io"
	"strconv"

	"github.com/spf13/pflag"
)

// Release builds set it with -ldflags "-X quack/internal/command.Version=<version>".
var Version = "dev"

func NewFlagSet(name string) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SortFlags = false
	fs.SetOutput(io.Discard)
	return fs
}

// pflag prints [=true|false] after every bool flag in help, and nothing after a BoolFunc flag, which parses the same.
func BoolFlag(fs *pflag.FlagSet, p *bool, name, shorthand, usage string) {
	fs.BoolFuncP(name, shorthand, usage, func(value string) error {
		on, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("must be true or false")
		}
		*p = on
		return nil
	})
}
