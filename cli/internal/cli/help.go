package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"quack/internal/wire"
)

func usage(flags string) string {
	return "quack runs the debug actions of a quack-enabled Android app over adb.\n\n" +
		"With no action, quack opens a terminal UI to pick a device and an app, then find and run actions. It prints\n" +
		"this help instead when stdin or stdout is not a terminal, AI_AGENT is set, or CI is set. With an action,\n" +
		"quack runs it once, prints the outcome and exits. Scripts and agents: list the actions with --list-actions,\n" +
		"run them with quack <action>, and add --json for output to parse.\n\n" +
		"usage: quack [options]                                open the terminal UI\n" +
		"       quack [options] <action> [key=value]...        run an action\n" +
		"       quack [options] --recipe <name> [key=value]... run a recipe\n" +
		"       quack [options] --list-actions [regex]         list the app's actions and their params\n" +
		"       quack [options] --list-recipes [regex]         list the app's recipes and their params\n" +
		"       quack [--app <package>] --init-recipes         set up recipes and editors to check them\n" +
		"       quack [options] --list-devices                 list the devices and their quack-enabled apps\n\n" +
		"arguments:\n" +
		"  key=value   a param of the action or the recipe. Repeat the key for a list: tag=a tag=b. The value takes the\n" +
		"              environment variable NAME as ${NAME}, and $${NAME} writes ${NAME}:\n" +
		"              quack account.login 'token=${API_TOKEN}'\n\n" +
		"options:\n" + flags + "\n" +
		"recipes:\n" +
		"  A recipe is a TypeScript or JavaScript function that runs actions and commands, and decides what to do with\n" +
		"  what they return. A project keeps them in .quack/recipes/, which quack finds in the current dir or above it,\n" +
		"  up to the home dir. [recipes] in .quack/quack.toml maps the apps whose packages a regex fully matches to\n" +
		"  files there, and [recipes] in config.toml maps apps to your own files in $XDG_CONFIG_HOME/quack/recipes/:\n\n" +
		"    [recipes]\n" +
		"    'io\\.example\\.app' = \"recipes.ts\"\n" +
		"    'io\\.example\\..*' = [\"common.ts\", \"features/*.ts\"]\n\n" +
		"  A path can be in a subdir, or a glob whose *, ? and [...] match within one dir. An app gets the recipes that\n" +
		"  every file it matches in both declares itself, and two recipes of one name fail them all. Files may import\n" +
		"  other local files, whose recipes run through quack.runRecipe() without being listed, but no packages.\n" +
		"  quack --init-recipes writes quack.d.ts and tsconfig.json next to the recipes, so that editors check them,\n" +
		"  and with --app starts .quack/ in the current dir when there is none. The terminal UI lists the recipes with\n" +
		"  the actions, marked ƒ, and $ lists only the recipes:\n\n" +
		"    import quack from \"quack\"\n\n" +
		"    export const login = quack.recipe({\n" +
		"      name: \"login\",\n" +
		"      description: \"Log in as the test user\",\n" +
		"      params: { user: { type: \"string\", default: \"foo\", hints: [\"foo\", \"bar\"] } },\n" +
		"      run({ user }) {\n" +
		"        quack.openApp()\n" +
		"        const status = quack.runAction<{ loggedIn: boolean }>(\"account.status\")\n" +
		"        if (!status.data.loggedIn) quack.runAction(\"account.login\", { user, remember: true })\n" +
		"        quack.shell(\"pm grant io.example android.permission.POST_NOTIFICATIONS\")\n" +
		"        quack.wait(\"2s\")\n" +
		"      },\n" +
		"    })\n\n" +
		"  A step that fails throws, and ends the recipe unless it catches the error.\n\n" +
		"settings:\n" +
		"  The terminal UI reads its settings from $XDG_CONFIG_HOME/quack/config.toml, or from\n" +
		"  ~/.config/quack/config.toml when XDG_CONFIG_HOME is unset or empty. quack never writes the file, and its\n" +
		"  keys are optional:\n\n" +
		"    show-data = false         # show the JSON data of notifications and results; /toggle-data flips it for\n" +
		"                              # the session\n" +
		"    group-namespaces = true   # while browsing with ↑, group the ids that share a namespace, such as sync.\n\n" +
		"  The terminal UI logs each problem with the file when it starts, and leaves the keys it could not read at\n" +
		"  their defaults.\n\n" +
		"exit codes:\n" +
		"  0  done\n" +
		"  1  the action rejected the call, failed, or ended without an answer, or a recipe failed\n" +
		"  2  bad usage, an unknown action, or invalid args\n" +
		"  3  no device or app to reach\n"
}

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
