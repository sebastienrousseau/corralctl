// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LogMsg represents a log entry to be displayed in the TUI.
type LogMsg struct {
	// RepoName is the name of the repository the entry refers to.
	RepoName string
	// Action is the operation performed (for example CLONE, SYNC, SKIP, ERROR).
	Action string
	// Message is a human-readable description of the outcome.
	Message string
}

// model represents the state of the Bubble Tea application.
type model struct {
	total    int
	done     int
	logs     []LogMsg
	prog     progress.Model
	quitting bool

	cloned   int
	synced   int
	failed   int
	existing int
}

// NewModel initializes a new TUI model with the expected total number of items.
func NewModel(total int) tea.Model {
	return model{
		total: total,
		prog: progress.New(
			progress.WithoutPercentage(),
			progress.WithGradient("#F56B5E", "#FF8A7A"),
		),
	}
}

// Init initializes the Bubble Tea application (no-op).
func (m model) Init() tea.Cmd {
	return nil
}

func (m *model) processLogMsg(msg LogMsg) {
	m.done++
	m.logs = append(m.logs, msg)
	if len(m.logs) > 10 {
		m.logs = m.logs[1:]
	}

	switch msg.Action {
	case "CLONE":
		m.cloned++
	case "SYNC":
		m.synced++
	case "ERROR":
		m.failed++
	case "SKIP":
		m.existing++
	case "DRY-RUN":
		switch msg.Message {
		case "git clone":
			m.cloned++
		case "git pull":
			m.synced++
		}
	}
}

// Update handles incoming Bubble Tea messages, advancing progress and stats as
// repository results arrive and quitting when the run completes or is cancelled.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			m.quitting = true
			return m, tea.Quit
		}
	case LogMsg:
		m.processLogMsg(msg)
		if m.done >= m.total {
			m.quitting = true
			return m, tea.Sequence(m.prog.SetPercent(1.0), tea.Quit)
		}
		return m, m.prog.SetPercent(float64(m.done) / float64(m.total))
	case progress.FrameMsg:
		progressModel, cmd := m.prog.Update(msg)
		m.prog = progressModel.(progress.Model)
		return m, cmd
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).MarginBottom(1)
	logStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// View renders the current progress bar, recent log lines, and, once finished,
// the final summary of the run.
func (m model) View() string {
	pad := strings.Repeat(" ", 2)
	percent := 1.0
	if m.total > 0 {
		percent = float64(m.done) / float64(m.total)
	}
	progBar := m.prog.ViewAs(percent)

	var header string
	if os.Getenv("CORRAL_SHOW_LOGO") != "0" {
		header = GetStyledLogo()
		header += lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("   ⧇ Organising Repositories") + "\n"
		header += lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("   "+strings.Repeat("─", 58)) + "\n\n"
	} else {
		header = titleStyle.Render("Corral - Organising Repositories") + "\n"
	}

	out := header
	out += pad + progBar + fmt.Sprintf(" %d/%d", m.done, m.total) + "\n\n"

	for _, l := range m.logs {
		icon := "✓"
		if l.Action == "ERROR" || strings.HasPrefix(l.Action, "FAIL") {
			icon = "✗"
		} else if l.Action == "SKIP" {
			icon = "-"
		}
		out += logStyle.Render(fmt.Sprintf("%s [%s] %s: %s", icon, l.Action, l.RepoName, l.Message)) + "\n"
	}

	if m.quitting {
		if m.done >= m.total {
			out += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("✓ Done.")
		} else {
			out += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✗ Aborted.")
		}
		out += fmt.Sprintf(" Cloned %d repos, synced %d repos, kept %d repos, %d failures.\n", m.cloned, m.synced, m.existing, m.failed)
	}

	return out
}
