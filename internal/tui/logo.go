// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var logoLines = []string{
	`         ⡀ ⢠ ⢐⡂ ⡄ ⢀         `,
	`       ⢀⡄⢿ ⢼⡆⣄⢠⢰⡥ ⡿⢠⡀       `,
	`      ⠆⢲⣡⠱⢸⡋⠐⡌⢡⠂⢙⡇⠏⣌⡆⠰      `,
	`     ⠘⠎⡀⠹⢷⡎ ⢭⣹⣏⡥ ⢱⡾⠏⢀⠱⠃     `,
	`    ⠰⠦⠌⣇⣎⣢⣿⡰⢀⢹⡏⡀⢆⣿⣔⣱⣸⠥⠴⠆    `,
	`    ⢠⣐⢶⣦ ⠚⡎⢷⡌⣼⣧⢡⡾⠱⠓ ⣴⡶⣂⡄    `,
	`    ⠈⡼⡵⢽⣌⢤⠉⢤⠻⣼⣧⠏⡤⢉⡤⣡⡯⠮⠥⠁    `,
	`     ⠈⠋⠡⣰⣛⠟⠲⣵⣹⣏⣮⠖⠻⣛⣖⠌⠙⠁     `,
	`           ⠐⠺⣿⣿⠗            `,
	`           ⢀⣠⡿⣷⣄⡀           `,
}

// GetStyledLogo returns the colored ASCII logo art as a string.
func GetStyledLogo() string {
	colors := []string{
		"#F87171",
		"#FA5B4E",
		"#F25447",
		"#E14F44",
		"#D5473D",
		"#C93F36",
		"#BD362E",
		"#B02E28",
		"#A22030",
		"#9F1239",
	}
	var sb strings.Builder
	sb.WriteString("\n")
	for i, line := range logoLines {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color(colors[i])).Render(line) + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString("  " + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F56B5E")).Render("Corral.") + "\n")
	sb.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render("All your repos. In perfect sync.") + "\n\n")
	return sb.String()
}
