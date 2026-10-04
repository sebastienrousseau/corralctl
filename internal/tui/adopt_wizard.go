// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sebastienrousseau/corralctl/internal/discover"
)

var runAdoptProgram = func(ctx context.Context, m tea.Model) (tea.Model, error) {
	p := tea.NewProgram(m, tea.WithContext(ctx))
	return p.Run()
}

// AdoptWizardModel manages the interactive selection of candidates for adoption.
type AdoptWizardModel struct {
	// Candidates is the list of discovered repositories presented in the wizard.
	Candidates []discover.Candidate
	// Selected maps candidate index to selection boolean.
	Selected map[int]bool
	// Cursor is the active cursor row index.
	Cursor int
	// Confirmed indicates whether the user confirmed selection with Enter.
	Confirmed bool
	// Quitting indicates whether the wizard was canceled or exited.
	Quitting bool
}

// NewAdoptWizardModel initializes the wizard model with discovered candidates.
func NewAdoptWizardModel(candidates []discover.Candidate) *AdoptWizardModel {
	sel := make(map[int]bool, len(candidates))
	for i := range candidates {
		sel[i] = true
	}
	return &AdoptWizardModel{
		Candidates: candidates,
		Selected:   sel,
	}
}

// Init implements tea.Model.
func (m *AdoptWizardModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m *AdoptWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.Quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down", "j":
			if m.Cursor < len(m.Candidates)-1 {
				m.Cursor++
			}
		case " ":
			m.Selected[m.Cursor] = !m.Selected[m.Cursor]
		case "a":
			for i := range m.Candidates {
				m.Selected[i] = true
			}
		case "n":
			for i := range m.Candidates {
				m.Selected[i] = false
			}
		case "enter":
			m.Confirmed = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m *AdoptWizardModel) View() string {
	if m.Quitting {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n  " + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F56B5E")).Render("Select Repositories to Adopt:") + "\n\n")

	for i, c := range m.Candidates {
		check := "[ ]"
		if m.Selected[i] {
			check = "[x]"
		}
		prefix := "  "
		if i == m.Cursor {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s %-25s %-12s %s", prefix, check, c.Name, c.DetectedLang, c.Path)
		if i == m.Cursor {
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("#F56B5E")).Bold(true).Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n  [Space] toggle  [a] all  [n] none  [Enter] confirm  [q] cancel\n")
	return sb.String()
}

// RunAdoptWizard runs the interactive terminal wizard to select candidates for adoption.
func RunAdoptWizard(ctx context.Context, candidates []discover.Candidate) ([]discover.Candidate, bool, error) {
	if len(candidates) == 0 {
		return nil, false, nil
	}
	m, err := runAdoptProgram(ctx, NewAdoptWizardModel(candidates))
	if err != nil {
		return nil, false, err
	}
	wm := m.(*AdoptWizardModel)
	if wm.Quitting || !wm.Confirmed {
		return nil, false, nil
	}
	var out []discover.Candidate
	for i, c := range wm.Candidates {
		if wm.Selected[i] {
			out = append(out, c)
		}
	}
	return out, true, nil
}
