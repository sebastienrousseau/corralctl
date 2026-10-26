// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

type selectorModel struct {
	// version is rendered in the footer: the caller's, or the build's.
	version       string
	repos         []github.Repo
	filteredRepos []github.Repo
	filter        string
	selected      map[string]bool // key is owner/name when available
	table         table.Model
	spinner       spinner.Model
	loading       bool
	loadingErr    error
	confirmed     bool
	quitting      bool
	fetchFn       FetchFunc

	showHelp bool
	cmdErr   string
}

// FetchFunc represents the function signature used by the selector to fetch repositories.
type FetchFunc func() ([]github.Repo, error)

var runSelectorProgram = func(ctx context.Context, model tea.Model) (tea.Model, error) {
	return tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
}

func repoSelectionKey(repo github.Repo) string {
	if repo.FullName != "" {
		return strings.ToLower(repo.FullName)
	}
	if repo.Owner != "" {
		return strings.ToLower(repo.Owner + "/" + repo.Name)
	}
	return strings.ToLower(repo.Name)
}

func repoDisplayName(repo github.Repo) string {
	if repo.FullName != "" {
		return repo.FullName
	}
	return repo.Name
}

// NewSelectorModel creates and initializes a new Bubble Tea model for the repository selector.
func NewSelectorModel(fetchFn FetchFunc) *selectorModel {
	sel := make(map[string]bool)

	columns := []table.Column{
		{Title: " ", Width: 3},
		{Title: "Repository", Width: 35},
		{Title: "Language", Width: 15},
		{Title: "Visibility", Width: 10},
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(12),
	)

	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("238")).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("255")).
		Background(lipgloss.Color("#F56B5E")).
		Bold(true)
	t.SetStyles(s)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#F56B5E"))

	m := &selectorModel{
		version:  Version,
		fetchFn:  fetchFn,
		loading:  true,
		selected: sel,
		table:    t,
		spinner:  sp,
	}
	return m
}

type fetchedReposMsg struct {
	repos []github.Repo
	err   error
}

func (m *selectorModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			repos, err := m.fetchFn()
			return fetchedReposMsg{repos: repos, err: err}
		},
	)
}

func (m *selectorModel) applyFilter() {
	if strings.HasPrefix(m.filter, "/") {
		return
	}

	var filtered []github.Repo
	for _, r := range m.repos {
		nameMatch := strings.Contains(strings.ToLower(r.Name), strings.ToLower(m.filter))
		langMatch := strings.Contains(strings.ToLower(r.Language), strings.ToLower(m.filter))
		if m.filter == "" || nameMatch || langMatch {
			filtered = append(filtered, r)
		}
	}
	m.filteredRepos = filtered
	m.updateTableRows()
}

func (m *selectorModel) updateTableRows() {
	var rows []table.Row
	for range m.filteredRepos {
		rows = append(rows, table.Row{"", "", "", ""})
	}
	m.table.SetRows(rows)
}

// Update handles one Bubble Tea message. Anything the selector does not
// consume is forwarded to the embedded table, which owns cursor movement.
func (m *selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case fetchedReposMsg:
		return m.handleFetched(msg)

	case tea.KeyMsg:
		m.cmdErr = ""
		if model, keyCmd, handled := m.handleKey(msg); handled {
			return model, keyCmd
		}
	}

	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// handleFetched installs the repository listing the fetch goroutine
// returned, selecting every repository by default.
func (m *selectorModel) handleFetched(msg fetchedReposMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.err != nil {
		m.loadingErr = msg.err
		return m, tea.Quit
	}
	m.repos = msg.repos
	m.filteredRepos = msg.repos
	for _, r := range msg.repos {
		m.selected[repoSelectionKey(r)] = true
	}
	m.updateTableRows()
	return m, nil
}

// handleKey dispatches one keypress. The third return value reports whether
// the selector consumed the key; when it is false the caller forwards the
// message to the table instead.
func (m *selectorModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit, true
	case "esc":
		if m.showHelp {
			m.showHelp = false
			return m, nil, true
		}
		m.quitting = true
		return m, tea.Quit, true
	case "?":
		if !m.loading {
			m.showHelp = !m.showHelp
			return m, nil, true
		}
	case "right", "tab":
		if completed, ok := completeSlashCommand(m.filter); ok {
			m.filter = completed
			return m, nil, true
		}
	case "enter":
		return m.handleEnter()
	case " ", "space":
		return m.handleSpace()
	case "backspace":
		if m.loading {
			return m, nil, true
		}
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.applyFilter()
		}
		return m, nil, true
	case "ctrl+a":
		return m.setFilteredSelection(true)
	case "ctrl+n":
		return m.setFilteredSelection(false)
	default:
		if m.loading {
			return m, nil, true
		}
		if len(msg.String()) == 1 && len(msg.Runes) > 0 && msg.Runes[0] >= 32 && msg.Runes[0] <= 126 {
			m.filter += msg.String()
			m.applyFilter()
			return m, nil, true
		}
	}
	return m, nil, false
}

// handleEnter either runs the slash command being typed or confirms the
// selection and exits the selector.
func (m *selectorModel) handleEnter() (tea.Model, tea.Cmd, bool) {
	if m.loading {
		return m, nil, true
	}
	if strings.HasPrefix(m.filter, "/") {
		target := m.filter
		for _, cmd := range slashCommands {
			if strings.HasPrefix(cmd, m.filter) {
				target = cmd
				break
			}
		}
		cmd := m.executeSlashCommand(target)
		m.filter = ""
		return m, cmd, true
	}
	m.confirmed = true
	return m, tea.Quit, true
}

// handleSpace toggles the repository under the cursor, unless a slash
// command is being typed — where space is just a character.
func (m *selectorModel) handleSpace() (tea.Model, tea.Cmd, bool) {
	if m.loading {
		return m, nil, true
	}
	if strings.HasPrefix(m.filter, "/") {
		m.filter += " "
		return m, nil, true
	}
	if len(m.filteredRepos) > 0 {
		idx := m.table.Cursor()
		if idx >= 0 && idx < len(m.filteredRepos) {
			key := repoSelectionKey(m.filteredRepos[idx])
			m.selected[key] = !m.selected[key]
			m.updateTableRows()
		}
	}
	return m, nil, true
}

// setFilteredSelection selects or deselects every repository currently
// passing the filter.
func (m *selectorModel) setFilteredSelection(selected bool) (tea.Model, tea.Cmd, bool) {
	if m.loading {
		return m, nil, true
	}
	for _, r := range m.filteredRepos {
		m.selected[repoSelectionKey(r)] = selected
	}
	m.updateTableRows()
	return m, nil, true
}

// RunSelector launches the interactive terminal selector program to choose
// repositories. version is shown in the footer; empty means the build's
// Version. It is passed per call rather than written to Version, so one
// caller's version never leaks into another's selector.
func RunSelector(ctx context.Context, owner, version string, fetchOpts github.FetchOptions, fetchFn FetchFunc) ([]github.Repo, bool, error) {
	sel := NewSelectorModel(fetchFn)
	if version != "" {
		sel.version = version
	}
	m, err := runSelectorProgram(ctx, sel)
	if err != nil {
		return nil, false, err
	}
	selModel := m.(*selectorModel)
	if selModel.loadingErr != nil {
		return nil, false, selModel.loadingErr
	}
	if selModel.quitting || !selModel.confirmed {
		return nil, false, nil
	}
	var out []github.Repo
	for _, r := range selModel.repos {
		if selModel.selected[repoSelectionKey(r)] {
			out = append(out, r)
		}
	}
	return out, true, nil
}
