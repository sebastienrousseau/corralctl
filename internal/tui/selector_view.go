// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sebastienrousseau/corralctl/internal/version"
)

// slashCommands are the in-session commands the filter line accepts. Kept in
// one place so tab-completion and execution can never disagree about the set.
var slashCommands = []string{"/exit", "/quit", "/help", "/all", "/none", "/sort"}

// completeSlashCommand returns the unique command the partial filter
// prefixes, and whether one was found. A filter that is not a slash command,
// or is already complete, completes to nothing.
func completeSlashCommand(filter string) (string, bool) {
	if !strings.HasPrefix(filter, "/") {
		return "", false
	}
	for _, cmd := range slashCommands {
		if len(cmd) > len(filter) && strings.HasPrefix(cmd, filter) {
			return cmd, true
		}
	}
	return "", false
}

func (m *selectorModel) renderCustomTable() string {
	var sb strings.Builder

	headerCheck := "   "
	headerRepo := fmt.Sprintf("%-35s", "Repository")
	headerLang := fmt.Sprintf("%-15s", "Language")
	headerVis := fmt.Sprintf("%-10s", "Visibility")

	headerRow := fmt.Sprintf("%s  %s  %s  %s", headerCheck, headerRepo, headerLang, headerVis)
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Render(headerRow) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("─"+strings.Repeat("─", 69)) + "\n")

	cursor := m.table.Cursor()
	start := 0
	if cursor >= 10 {
		start = cursor - 9
	}
	end := start + 12
	if end > len(m.filteredRepos) {
		end = len(m.filteredRepos)
	}

	for i := start; i < end; i++ {
		r := m.filteredRepos[i]

		var checkChar string
		if m.selected[repoSelectionKey(r)] {
			checkChar = "✔"
		} else {
			checkChar = "·"
		}

		repoVal := repoDisplayName(r)
		if len(repoVal) > 35 {
			repoVal = repoVal[:32] + "..."
		}

		langVal := r.Language
		if len(langVal) > 15 {
			langVal = langVal[:12] + "..."
		}

		visVal := strings.ToLower(r.Visibility)
		if len(visVal) > 10 {
			visVal = visVal[:7] + "..."
		}

		if i == cursor {
			repoStr := fmt.Sprintf("%-35s", repoVal)
			langStr := fmt.Sprintf("%-15s", langVal)
			visStr := fmt.Sprintf("%-10s", visVal)
			rowContent := fmt.Sprintf("%s    %s  %s  %s", checkChar, repoStr, langStr, visStr)

			sb.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Background(lipgloss.Color("#F56B5E")).
				Bold(true).
				Render(rowContent) + "\n")
		} else {
			styledCheck := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(checkChar)
			if m.selected[repoSelectionKey(r)] {
				styledCheck = lipgloss.NewStyle().Foreground(lipgloss.Color("#F56B5E")).Render(checkChar)
			}
			repoStr := lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Render(fmt.Sprintf("%-35s", repoVal))
			langStr := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(fmt.Sprintf("%-15s", langVal))
			visStr := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(fmt.Sprintf("%-10s", visVal))

			_, _ = fmt.Fprintf(&sb, "%s    %s  %s  %s\n", styledCheck, repoStr, langStr, visStr)
		}
	}

	visibleRows := end - start
	for i := visibleRows; i < 12; i++ {
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m *selectorModel) View() string {
	var header string
	if os.Getenv("CORRAL_SHOW_LOGO") != "0" {
		header = GetStyledLogo()
	} else {
		header = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("Corral - Select Repositories") + "\n\n"
	}

	out := header

	if m.loading {
		out += fmt.Sprintf("   %s Loading repositories...\n\n", m.spinner.View())
		out += lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("   [esc] cancel") + "\n"
		return out
	}

	if m.loadingErr != nil {
		out += fmt.Sprintf("   %s\n\n", lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Error: "+m.loadingErr.Error()))
		out += lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("   [esc] exit") + "\n"
		return out
	}

	var promptStr string
	if strings.HasPrefix(m.filter, "/") {
		labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		cmdStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F56B5E"))
		suggStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		var suggestion string
		commands := []string{"/exit", "/quit", "/help", "/all", "/none", "/sort"}
		for _, cmd := range commands {
			if len(cmd) > len(m.filter) && strings.HasPrefix(cmd, m.filter) {
				suggestion = cmd[len(m.filter):]
				break
			}
		}
		promptStr = labelStyle.Render("  Command: ") + cmdStyle.Render(m.filter) + labelStyle.Render("_") + suggStyle.Render(suggestion)
	} else {
		promptStr = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(fmt.Sprintf("  Search repositories (%d found): %s_", len(m.filteredRepos), m.filter))
	}
	out += promptStr + "\n"

	divider := lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("  " + strings.Repeat("─", 58))
	out += divider + "\n\n"

	if m.cmdErr != "" {
		out += "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.cmdErr) + "\n\n"
	}

	if m.showHelp {
		out += m.renderHelpPanel() + "\n"
	} else {
		tableStr := m.renderCustomTable()
		indentedTable := ""
		for _, line := range strings.Split(tableStr, "\n") {
			indentedTable += "  " + line + "\n"
		}
		out += indentedTable + "\n"
	}

	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F56B5E")).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("239"))

	shortcutBar := fmt.Sprintf("  %s %s %s  %s %s %s  %s %s %s  %s %s %s  %s %s",
		keyStyle.Render("[space]"), descStyle.Render("toggle"), sepStyle.Render("•"),
		keyStyle.Render("[ctrl+a]"), descStyle.Render("all"), sepStyle.Render("•"),
		keyStyle.Render("[ctrl+n]"), descStyle.Render("none"), sepStyle.Render("•"),
		keyStyle.Render("[/]"), descStyle.Render("command"), sepStyle.Render("•"),
		keyStyle.Render("[enter]"), descStyle.Render("confirm"),
	)
	out += shortcutBar + "\n"
	out += "\n" + m.renderFooter()

	return out
}

func (m *selectorModel) executeSlashCommand(cmdStr string) tea.Cmd {
	m.cmdErr = ""
	parts := strings.Fields(strings.TrimSpace(cmdStr))
	if len(parts) == 0 {
		return nil
	}
	cmd := parts[0]

	switch cmd {
	case "/exit", "/quit":
		m.quitting = true
		m.confirmed = false
		return tea.Quit

	case "/help":
		m.showHelp = true

	case "/all":
		for _, r := range m.filteredRepos {
			m.selected[repoSelectionKey(r)] = true
		}
		m.updateTableRows()

	case "/none":
		for _, r := range m.filteredRepos {
			m.selected[repoSelectionKey(r)] = false
		}
		m.updateTableRows()

	case "/sort":
		if len(parts) < 2 {
			m.cmdErr = "Usage: /sort <name|language|visibility|language_name>"
			return nil
		}
		field := strings.ToLower(parts[1])
		switch field {
		case "name":
			sort.Slice(m.filteredRepos, func(i, j int) bool {
				return strings.ToLower(m.filteredRepos[i].Name) < strings.ToLower(m.filteredRepos[j].Name)
			})
		case "language", "lang":
			sort.Slice(m.filteredRepos, func(i, j int) bool {
				return strings.ToLower(m.filteredRepos[i].Language) < strings.ToLower(m.filteredRepos[j].Language)
			})
		case "visibility", "vis":
			sort.Slice(m.filteredRepos, func(i, j int) bool {
				return strings.ToLower(m.filteredRepos[i].Visibility) < strings.ToLower(m.filteredRepos[j].Visibility)
			})
		default:
			if field == "public" || field == "private" {
				normVis := "Public"
				if field == "private" {
					normVis = "Private"
				}
				sort.Slice(m.filteredRepos, func(i, j int) bool {
					iVis := m.filteredRepos[i].Visibility == normVis
					jVis := m.filteredRepos[j].Visibility == normVis
					if iVis && !jVis {
						return true
					}
					if !iVis && jVis {
						return false
					}
					return strings.ToLower(m.filteredRepos[i].Name) < strings.ToLower(m.filteredRepos[j].Name)
				})
			} else {
				hasLang := false
				for _, r := range m.repos {
					if strings.ToLower(r.Language) == field {
						hasLang = true
						break
					}
				}
				if hasLang {
					sort.Slice(m.filteredRepos, func(i, j int) bool {
						iLang := strings.ToLower(m.filteredRepos[i].Language) == field
						jLang := strings.ToLower(m.filteredRepos[j].Language) == field
						if iLang && !jLang {
							return true
						}
						if !iLang && jLang {
							return false
						}
						return strings.ToLower(m.filteredRepos[i].Name) < strings.ToLower(m.filteredRepos[j].Name)
					})
				} else {
					m.cmdErr = fmt.Sprintf("Unknown sort field, visibility or language: %s (choose name, language, visibility, public, private, or a language like python)", parts[1])
				}
			}
		}
		m.updateTableRows()

	default:
		m.cmdErr = fmt.Sprintf("Unknown command: %s. Type /help for help.", cmd)
	}
	return nil
}

func (m *selectorModel) renderHelpPanel() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F56B5E")).Render("   In-Session Commands") + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("   "+strings.Repeat("─", 58)) + "\n\n")

	commands := [][]string{
		{"/sort <field>", "Sort list by name, language (lang), or visibility (vis)"},
		{"/all", "Select all filtered repositories"},
		{"/none", "Deselect all filtered repositories"},
		{"/exit, /quit", "Exit the CLI immediately"},
		{"/help", "Show this command help menu"},
	}

	for _, c := range commands {
		cmdStr := fmt.Sprintf("   %-16s", c[0])
		descStr := c[1]
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F56B5E")).Render(cmdStr))
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(descStr) + "\n")
	}

	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("   Press [?] or [esc] to return to the repository list.") + "\n")

	for i := len(commands) + 5; i < 15; i++ {
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m *selectorModel) renderFooter() string {
	left := " ? for commands"
	right := fmt.Sprintf("Made with ❤️ in London, UK (%s)", version.Display(m.version))

	leftLen := len(left)
	rightRunes := []rune(right)
	rightLenVisual := len(rightRunes)

	totalWidth := 71
	spacesCount := totalWidth - leftLen - rightLenVisual + 1
	if spacesCount < 1 {
		spacesCount = 2
	}

	spaces := strings.Repeat(" ", spacesCount)
	footerText := fmt.Sprintf(" %s%s%s", left, spaces, right)
	return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(footerText) + "\n"
}
