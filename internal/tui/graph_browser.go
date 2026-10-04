// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/graph"
)

var listWorktreesOp = git.ListWorktrees

// GraphBrowserModel manages the interactive dependency graph viewer state.
type GraphBrowserModel struct {
	graph         *graph.Graph
	cursor        int
	quitting      bool
	showWorktrees bool
}

// NewGraphBrowserModel creates a new model for browsing a dependency graph.
func NewGraphBrowserModel(g *graph.Graph) GraphBrowserModel {
	return GraphBrowserModel{
		graph: g,
	}
}

// Init initializes the Bubble Tea application (no-op).
func (m GraphBrowserModel) Init() tea.Cmd {
	return nil
}

// Update handles interactive keyboard input for navigating the dependency graph.
func (m GraphBrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.graph != nil && m.cursor < len(m.graph.Nodes)-1 {
				m.cursor++
			}
		case "w":
			m.showWorktrees = !m.showWorktrees
		}
	}
	return m, nil
}

// View renders the terminal view for the interactive dependency graph explorer.
func (m GraphBrowserModel) View() string {
	if m.quitting {
		return ""
	}
	if m.graph == nil || len(m.graph.Nodes) == 0 {
		return "No repositories in graph.\nPress q to exit.\n"
	}

	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0288D1"))
	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF8A7A"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).BorderForeground(lipgloss.Color("#0288D1"))

	b.WriteString(titleStyle.Render("corralctl graph explorer") + "  " + dimStyle.Render("(navigate: ↑/↓/j/k, toggle worktrees: w, quit: q)") + "\n\n")

	curr := m.graph.Nodes[m.cursor]

	// Left panel: repository list
	var left strings.Builder
	for i, n := range m.graph.Nodes {
		prefix := "  "
		line := fmt.Sprintf("%-20s [%d deps]", n.Name, len(n.Dependencies))
		if i == m.cursor {
			prefix = "> "
			left.WriteString(selectedStyle.Render(prefix+line) + "\n")
		} else {
			left.WriteString(dimStyle.Render(prefix) + line + "\n")
		}
	}

	// Right panel: selected repo details
	var right strings.Builder
	fmt.Fprintf(&right, "Repository:   %s\n", curr.Name)
	fmt.Fprintf(&right, "Path:         %s\n", curr.Path)
	if curr.Language != "" {
		fmt.Fprintf(&right, "Language:     %s\n", curr.Language)
	}
	if curr.PackageName != "" {
		fmt.Fprintf(&right, "Package:      %s\n", curr.PackageName)
	}

	if m.showWorktrees {
		right.WriteString("\nLinked Worktrees:\n")
		wts, err := listWorktreesOp(context.Background(), curr.Path)
		if err != nil || len(wts) == 0 {
			right.WriteString("  none\n")
		} else {
			for _, wt := range wts {
				branch := wt.Branch
				if branch == "" {
					if wt.Bare {
						branch = "(bare)"
					} else {
						branch = "(detached)"
					}
				}
				fmt.Fprintf(&right, "  - %s [%s]\n", branch, wt.Path)
			}
		}
	} else {
		right.WriteString("\nDependencies:\n")
		if len(curr.Dependencies) == 0 {
			right.WriteString("  none\n")
		} else {
			for _, d := range curr.Dependencies {
				fmt.Fprintf(&right, "  - %s\n", d)
			}
		}

		dependents := m.graph.Dependents[curr.Name]
		right.WriteString("\nDependents:\n")
		if len(dependents) == 0 {
			right.WriteString("  none\n")
		} else {
			for _, dep := range dependents {
				fmt.Fprintf(&right, "  - %s\n", dep)
			}
		}
	}

	leftBox := boxStyle.Render(left.String())
	rightBox := boxStyle.Render(right.String())

	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox))
	b.WriteString("\n")
	return b.String()
}

var runGraphProgram = func(m tea.Model) error {
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// RunGraphBrowser launches the interactive dependency graph explorer.
func RunGraphBrowser(g *graph.Graph) error {
	m := NewGraphBrowserModel(g)
	return runGraphProgram(m)
}
