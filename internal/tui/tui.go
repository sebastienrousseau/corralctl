// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package tui provides a Bubble Tea terminal user interface for Corral.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// Version is the build version of Corral, rendered in the TUI footer.
// Injected at build time alongside cmd.Version via -ldflags "-X …=Version";
// the "dev" fallback makes an un-injected build obvious.
var Version = "dev"

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

type selectorModel struct {
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
		BorderForeground(lipgloss.Color("238")). // subtle dark gray border
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("255")).
		Background(lipgloss.Color("#F56B5E")). // bright coral background!
		Bold(true)
	t.SetStyles(s)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#F56B5E"))

	m := &selectorModel{
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
			// For the active row, we construct plain text and render the entire row in white on coral.
			// This prevents ANSI escapes inside the cells from clearing the solid background highlight
			// and ensures the checkmark displays clearly in white.
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
			// For inactive rows, we style cells individually.
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

// slashCommands are the in-session commands the filter line accepts. Kept in
// one place so tab-completion and execution can never disagree about the set.
var slashCommands = []string{"/exit", "/quit", "/help", "/all", "/none", "/sort"}

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
	case "ctrl+a": // Select all filtered
		return m.setFilteredSelection(true)
	case "ctrl+n": // Select none filtered
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

// RunSelector launches the interactive terminal selector program to choose repositories.
func RunSelector(ctx context.Context, owner string, fetchOpts github.FetchOptions, fetchFn FetchFunc) ([]github.Repo, bool, error) {
	m, err := runSelectorProgram(ctx, NewSelectorModel(fetchFn))
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
	vStr := Version
	if vStr == "" {
		vStr = "dev"
	}
	left := " ? for commands"
	right := fmt.Sprintf("Made with ❤️ in London, UK (v%s)", vStr)

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
