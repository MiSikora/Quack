package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// One letter per pixel: W white, O orange, T teal, K black, and . none.
var duck = []string{
	"......W.........",
	".....WWW........",
	"....WWWWW.......",
	"....WKWKW...W...",
	"....WOOOO...W...",
	"....WOOOO..WWW..",
	".....WWW...WWW..",
	".....WWW..WWWW..",
	"...WWTWTWWWWW...",
	"WWWWWTTTWWWW....",
	".WWWWTWTWWW.....",
	"..WWWWWWWWW.....",
	"...WWWWWWWW.....",
	"....WWWWWW......",
	".....O..O.......",
	".....O..O.......",
	"...OOO.OOO......",
}

var duckColors = map[byte]color.Color{
	'W': lipgloss.Color("255"),
	'O': lipgloss.Color("208"),
	'T': lipgloss.Color("37"),
	'K': lipgloss.Color("236"),
}

// Two pixels to a character: the upper one as the foreground of ▀, or the lower one as the foreground of ▄ when only
// it is set.
func duckView() string {
	var lines []string
	for y := 0; y < len(duck); y += 2 {
		var b strings.Builder
		for x := range len(duck[y]) {
			top, bottom := duckColors[duck[y][x]], color.Color(nil)
			if y+1 < len(duck) {
				bottom = duckColors[duck[y+1][x]]
			}
			switch {
			case top == nil && bottom == nil:
				b.WriteString(" ")
			case top == nil:
				b.WriteString(lipgloss.NewStyle().Foreground(bottom).Render("▄"))
			case bottom == nil:
				b.WriteString(lipgloss.NewStyle().Foreground(top).Render("▀"))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(top).Background(bottom).Render("▀"))
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(lines, "\n")
}

func header(version string) string {
	lines := strings.Split(duckView(), "\n")
	text := []string{
		selectedStyle.Render("quack") + statusStyle.Render(" "+version),
		statusStyle.Render("type to find an action or a recipe · $ recipes only · / commands"),
	}
	for i, t := range text {
		lines[i] += strings.Repeat(" ", len(duck[0])-ansi.StringWidth(lines[i])+2) + t
	}
	return strings.Join(lines, "\n")
}
