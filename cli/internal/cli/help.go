package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"quack/internal/wire"
)

func matching(actions []wire.Action, filter *regexp.Regexp) ([]wire.Action, error) {
	if filter == nil {
		return actions, nil
	}
	var kept []wire.Action
	for _, a := range actions {
		if filter.MatchString(a.ID) {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		return nil, wire.Error{Code: wire.ErrorCodeUnknownAction, Message: fmt.Sprintf("no action matches '%s'", filter)}
	}
	return kept, nil
}

func printHelp(w io.Writer, hello wire.Hello, actions []wire.Action, header bool) {
	var out strings.Builder
	if header {
		fmt.Fprintf(&out, "%s %s\n\n", hello.Package, hello.Version)
	}
	for _, a := range actions {
		writeAction(&out, a)
	}
	fmt.Fprint(w, strings.TrimSuffix(out.String(), "\n"))
}

func writeAction(out *strings.Builder, a wire.Action) {
	out.WriteString(a.ID + "\n")
	if a.Description != "" {
		writeWrapped(out, sentence(a.Description), helpIndent)
		out.WriteString("\n")
	}
	for _, p := range a.Params {
		out.WriteString(helpIndent + paramUsage(p) + "\n")
		var text []string
		switch presence := p.Presence.(type) {
		case wire.Required:
			text = append(text, "Required.")
		case wire.Default:
			text = append(text, "Default: "+presence.String()+".")
		}
		if p.Description != "" {
			text = append(text, sentence(p.Description))
		}
		if len(text) > 0 {
			writeWrapped(out, strings.Join(text, " "), helpIndent+helpIndent)
			out.WriteString("\n")
		}
	}
	if !strings.HasSuffix(out.String(), "\n\n") {
		out.WriteString("\n")
	}
}

func paramUsage(p wire.Param) string {
	switch p.Type {
	case wire.ParamTypeEnum:
		return fmt.Sprintf("%s=<%s>", p.Name, strings.Join(p.Choices, "|"))
	case wire.ParamTypeStringList:
		return fmt.Sprintf("%s=<string>...", p.Name)
	default:
		return fmt.Sprintf("%s=<%s>", p.Name, p.Type)
	}
}

func writeWrapped(out *strings.Builder, text, indent string) {
	for _, paragraph := range strings.Split(text, "\n") {
		line := indent
		for _, word := range strings.Fields(paragraph) {
			if len(line) > len(indent) && len(line)+1+len(word) > helpWidth {
				out.WriteString(line + "\n")
				line = indent
			}
			if len(line) > len(indent) {
				line += " "
			}
			line += word
		}
		out.WriteString(line + "\n")
	}
}

func sentence(text string) string {
	if text == "" || strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		return text
	}
	return text + "."
}

const (
	helpIndent = "    "
	helpWidth  = 80
)
