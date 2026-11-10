package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// duck is quack's mascot in a bowtie, with a raised wing that waves, or not quite. It is one letter per pixel: W
// white, O orange, T teal, K black, and . none.
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

// duckView draws the duck with half blocks, two pixels to a character: the upper one in the foreground of ▀, or the
// lower one in the foreground of ▄ when only it is set.
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
