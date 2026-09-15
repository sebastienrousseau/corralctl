// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

func TestModelInit(t *testing.T) {
	m := NewModel(10)
	if m.Init() != nil {
		t.Errorf("Expected Init to return nil")
	}
}

func TestRepositoryIdentityKeepsDuplicateNamesDistinct(t *testing.T) {
	first := github.Repo{Name: "shared", Owner: "one", FullName: "one/shared"}
	second := github.Repo{Name: "shared", Owner: "two", FullName: "two/shared"}
	if repoSelectionKey(first) == repoSelectionKey(second) {
		t.Fatal("owner-qualified repositories must have distinct selection keys")
	}
	if repoDisplayName(first) != "one/shared" {
		t.Fatalf("display name = %q", repoDisplayName(first))
	}
	fallback := github.Repo{Name: "repo", Owner: "owner"}
	if repoSelectionKey(fallback) != "owner/repo" || repoDisplayName(fallback) != "repo" {
		t.Fatalf("unexpected fallback identity: %q %q", repoSelectionKey(fallback), repoDisplayName(fallback))
	}
}

func TestRunSelectorOutcomes(t *testing.T) {
	oldRun := runSelectorProgram
	t.Cleanup(func() { runSelectorProgram = oldRun })
	wantErr := errors.New("runner failed")
	runSelectorProgram = func(context.Context, tea.Model) (tea.Model, error) { return nil, wantErr }
	if _, _, err := RunSelector(context.Background(), "owner", github.FetchOptions{}, nil); !errors.Is(err, wantErr) {
		t.Fatalf("runner error = %v", err)
	}

	runSelectorProgram = func(ctx context.Context, model tea.Model) (tea.Model, error) {
		selected := model.(*selectorModel)
		selected.loadingErr = errors.New("fetch failed")
		return selected, nil
	}
	if _, _, err := RunSelector(context.Background(), "owner", github.FetchOptions{}, nil); err == nil {
		t.Fatal("expected loading error")
	}

	for _, configure := range []func(*selectorModel){
		func(m *selectorModel) { m.quitting = true },
		func(m *selectorModel) { m.confirmed = false },
	} {
		runSelectorProgram = func(ctx context.Context, model tea.Model) (tea.Model, error) {
			selected := model.(*selectorModel)
			configure(selected)
			return selected, nil
		}
		if repos, ok, err := RunSelector(context.Background(), "owner", github.FetchOptions{}, nil); err != nil || ok || repos != nil {
			t.Fatalf("cancel outcome = %v %v %v", repos, ok, err)
		}
	}

	first := github.Repo{Name: "a", Owner: "one", FullName: "one/a"}
	second := github.Repo{Name: "b", Owner: "two", FullName: "two/b"}
	runSelectorProgram = func(ctx context.Context, model tea.Model) (tea.Model, error) {
		selected := model.(*selectorModel)
		selected.confirmed = true
		selected.repos = []github.Repo{first, second}
		selected.selected[repoSelectionKey(second)] = true
		return selected, nil
	}
	repos, ok, err := RunSelector(context.Background(), "owner", github.FetchOptions{}, nil)
	if err != nil || !ok || len(repos) != 1 || repos[0].FullName != second.FullName {
		t.Fatalf("selection outcome = %+v %v %v", repos, ok, err)
	}
}

func TestSelectorInitFetchAndRenderingEdges(t *testing.T) {
	want := []github.Repo{{Name: "repo"}}
	m := NewSelectorModel(func() ([]github.Repo, error) { return want, nil })
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("Init message = %#v", batch)
	}
	msg := batch[1]().(fetchedReposMsg)
	if len(msg.repos) != 1 || msg.err != nil {
		t.Fatalf("fetch message = %+v", msg)
	}

	m.loading = false
	m.repos = []github.Repo{
		{Name: strings.Repeat("n", 40), Language: strings.Repeat("l", 20), Visibility: strings.Repeat("v", 15)},
	}
	for i := 0; i < 12; i++ {
		m.repos = append(m.repos, github.Repo{Name: fmt.Sprintf("repo-%d", i), Language: "Go", Visibility: "Public"})
	}
	m.filteredRepos = m.repos
	m.filteredRepos[11] = github.Repo{Name: strings.Repeat("n", 40), Language: strings.Repeat("l", 20), Visibility: strings.Repeat("v", 15)}
	m.updateTableRows()
	m.table.SetCursor(11)
	m.selected[repoSelectionKey(m.repos[0])] = true
	view := m.renderCustomTable()
	if !strings.Contains(view, "...") {
		t.Fatal("expected long cells to be truncated")
	}

	m.filter = "no-match"
	m.applyFilter()
	if len(m.filteredRepos) != 0 {
		t.Fatal("expected empty filtered set")
	}
	m.filter = "/help"
	m.applyFilter()
	if m.filter != "/help" {
		t.Fatal("slash filter unexpectedly changed")
	}
}

func TestModelUpdate(t *testing.T) {
	m := NewModel(2).(model)

	// Test KeyMsg quit (q)
	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil || !newM.(model).quitting {
		t.Errorf("Expected quitting on 'q'")
	}

	// Test KeyMsg quit (ctrl+c)
	newM, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || !newM.(model).quitting {
		t.Errorf("Expected quitting on 'ctrl+c'")
	}

	// Test KeyMsg not quit
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd != nil {
		t.Errorf("Expected nil cmd for 'a'")
	}

	// Test unknown msg
	_, cmd = m.Update("unknown")
	if cmd != nil {
		t.Errorf("Expected nil cmd for unknown msg")
	}

	// Test LogMsg processing and completion
	msg1 := LogMsg{RepoName: "repo1", Action: "CLONE", Message: "clone ok"}
	newM, _ = m.Update(msg1)
	m2 := newM.(model)
	if m2.done != 1 || m2.cloned != 1 {
		t.Errorf("Expected done=1, cloned=1, got done=%d cloned=%d", m2.done, m2.cloned)
	}

	msg2 := LogMsg{RepoName: "repo2", Action: "SYNC", Message: "sync ok"}
	newM, _ = m2.Update(msg2)
	m3 := newM.(model)
	if !m3.quitting || m3.done != 2 || m3.synced != 1 {
		t.Errorf("Expected quitting=true, done=2, synced=1, got quitting=%v done=%d synced=%d", m3.quitting, m3.done, m3.synced)
	}

	// Test progress.FrameMsg
	frameMsg := progress.FrameMsg{}
	_, _ = m3.Update(frameMsg)
}

func TestProcessLogMsg(t *testing.T) {
	m := NewModel(10).(model)

	m.processLogMsg(LogMsg{Action: "CLONE"})
	m.processLogMsg(LogMsg{Action: "SYNC"})
	m.processLogMsg(LogMsg{Action: "ERROR"})
	m.processLogMsg(LogMsg{Action: "SKIP"})
	m.processLogMsg(LogMsg{Action: "DRY-RUN", Message: "git clone"})
	m.processLogMsg(LogMsg{Action: "DRY-RUN", Message: "git pull"})

	// Fill logs over 10 to trigger shift
	for i := 0; i < 15; i++ {
		m.processLogMsg(LogMsg{Action: "CLONE"})
	}

	if len(m.logs) != 10 {
		t.Errorf("Expected 10 logs, got %d", len(m.logs))
	}
	if m.cloned != 17 { // 1 clone + 1 dry-run + 15 loop
		t.Errorf("Expected 17 cloned, got %d", m.cloned)
	}
	if m.synced != 2 {
		t.Errorf("Expected 2 synced, got %d", m.synced)
	}
	if m.failed != 1 {
		t.Errorf("Expected 1 failed, got %d", m.failed)
	}
	if m.existing != 1 {
		t.Errorf("Expected 1 existing, got %d", m.existing)
	}
}

func TestModelView(t *testing.T) {
	m := NewModel(10).(model)

	m.processLogMsg(LogMsg{RepoName: "repo1", Action: "CLONE", Message: "ok"})
	m.processLogMsg(LogMsg{RepoName: "repo2", Action: "ERROR", Message: "fail"})
	m.processLogMsg(LogMsg{RepoName: "repo3", Action: "FAIL", Message: "fail"})
	m.processLogMsg(LogMsg{RepoName: "repo4", Action: "SKIP", Message: "skip"})

	view := m.View()
	if !strings.Contains(strings.ToLower(view), "perfect sync") && !strings.Contains(strings.ToLower(view), "organising repositories") {
		t.Errorf("Expected view to contain branding text")
	}

	m.quitting = true
	viewAborted := m.View()
	if !strings.Contains(viewAborted, "Aborted.") {
		t.Errorf("Expected view to contain Aborted.")
	}

	m.done = 10
	viewDone := m.View()
	if !strings.Contains(viewDone, "Done.") {
		t.Errorf("Expected view to contain Done.")
	}
}

func TestSelectorModel(t *testing.T) {
	repos := []github.Repo{
		{Name: "repo1", Language: "Go"},
		{Name: "repo2", Language: "Rust"},
	}

	fetchFn := func() ([]github.Repo, error) {
		return repos, nil
	}
	m := NewSelectorModel(fetchFn)
	newM, _ := m.Update(fetchedReposMsg{repos: repos})
	m = newM.(*selectorModel)

	if m.Init() == nil {
		t.Errorf("Expected selector Init to return non-nil loading commands")
	}

	// Test viewport filtering
	if len(m.filteredRepos) != 2 {
		t.Errorf("Expected 2 repos, got %d", len(m.filteredRepos))
	}

	// Test typing/filtering
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m2 := newM.(*selectorModel)
	if m2.filter != "g" {
		t.Errorf("Expected filter to be 'g', got %q", m2.filter)
	}
	if len(m2.filteredRepos) != 1 || m2.filteredRepos[0].Name != "repo1" {
		t.Errorf("Expected only repo1 to match filter 'g', got %v", m2.filteredRepos)
	}

	// Test backspace
	newM, _ = m2.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m3 := newM.(*selectorModel)
	if m3.filter != "" {
		t.Errorf("Expected empty filter, got %q", m3.filter)
	}

	// Test navigation (down/up)
	newM, _ = m3.Update(tea.KeyMsg{Type: tea.KeyDown})
	m4 := newM.(*selectorModel)
	if m4.table.Cursor() != 1 {
		t.Errorf("Expected cursor at 1 after down key, got %d", m4.table.Cursor())
	}

	newM, _ = m4.Update(tea.KeyMsg{Type: tea.KeyUp})
	m5 := newM.(*selectorModel)
	if m5.table.Cursor() != 0 {
		t.Errorf("Expected cursor at 0 after up key, got %d", m5.table.Cursor())
	}

	// Test toggle selection (initially true, toggled to false)
	newM, _ = m5.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m6 := newM.(*selectorModel)
	if m6.selected["repo1"] != false {
		t.Errorf("Expected repo1 selected to toggle to false")
	}

	// Test select none ('ctrl+n')
	newM, _ = m6.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m7 := newM.(*selectorModel)
	if m7.selected["repo1"] != false {
		t.Errorf("Expected repo1 selected to toggle to false after 'ctrl+n'")
	}

	// Test select all ('ctrl+a')
	newM, _ = m7.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m8 := newM.(*selectorModel)
	if m8.selected["repo1"] != true || m8.selected["repo2"] != true {
		t.Errorf("Expected both to be selected after 'ctrl+a'")
	}

	// Test cancel
	newM, _ = m8.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m9 := newM.(*selectorModel)
	if !m9.quitting {
		t.Errorf("Expected quitting to be true after Esc")
	}

	// Test confirm
	newM, _ = m8.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m10 := newM.(*selectorModel)
	if !m10.confirmed {
		t.Errorf("Expected confirmed to be true after Enter")
	}

	// Test render View
	view := m10.View()
	if !strings.Contains(view, "Corral.") && !strings.Contains(view, "Search repositories") {
		t.Errorf("expected view to contain header elements, got %s", view)
	}
}

func TestSlashCommands(t *testing.T) {
	repos := []github.Repo{
		{Name: "c-repo", Language: "Python", Visibility: "Public"},
		{Name: "a-repo", Language: "Go", Visibility: "Private"},
		{Name: "b-repo", Language: "Rust", Visibility: "Public"},
	}
	m := NewSelectorModel(func() ([]github.Repo, error) {
		return repos, nil
	})

	newM, _ := m.Update(fetchedReposMsg{repos: repos, err: nil})
	model := newM.(*selectorModel)

	// Test /all
	model.executeSlashCommand("/all")
	if !model.selected["a-repo"] || !model.selected["b-repo"] || !model.selected["c-repo"] {
		t.Errorf("Expected all repos to be selected")
	}

	// Test /none
	model.executeSlashCommand("/none")
	if model.selected["a-repo"] || model.selected["b-repo"] || model.selected["c-repo"] {
		t.Errorf("Expected all repos to be deselected")
	}

	// Test /sort name
	model.executeSlashCommand("/sort name")
	if model.filteredRepos[0].Name != "a-repo" || model.filteredRepos[1].Name != "b-repo" || model.filteredRepos[2].Name != "c-repo" {
		t.Errorf("Expected sorted by name: a-repo, b-repo, c-repo; got order: %s, %s, %s", model.filteredRepos[0].Name, model.filteredRepos[1].Name, model.filteredRepos[2].Name)
	}

	// Test /sort language
	model.executeSlashCommand("/sort language")
	if model.filteredRepos[0].Name != "a-repo" || model.filteredRepos[1].Name != "c-repo" || model.filteredRepos[2].Name != "b-repo" {
		t.Errorf("Expected sorted by language: a-repo (Go), c-repo (Python), b-repo (Rust)")
	}
	// Test /sort python
	model.executeSlashCommand("/sort python")
	if model.filteredRepos[0].Name != "c-repo" || model.filteredRepos[1].Name != "a-repo" || model.filteredRepos[2].Name != "b-repo" {
		t.Errorf("Expected sorted by Python priority: c-repo (Python), a-repo (Go), b-repo (Rust); got order: %s, %s, %s", model.filteredRepos[0].Name, model.filteredRepos[1].Name, model.filteredRepos[2].Name)
	}
	// Test unknown command
	model.executeSlashCommand("/invalid")
	if !strings.Contains(model.cmdErr, "Unknown command") {
		t.Errorf("Expected unknown command error, got %q", model.cmdErr)
	}
	// Test /exit returns tea.Quit command
	cmdExit := model.executeSlashCommand("/exit")
	if cmdExit == nil {
		t.Errorf("Expected /exit to return a non-nil command")
	}
	// Test character-by-character typing simulation of "/none" and "/all"
	// 1. Reset all to true
	for i := range model.repos {
		model.selected[model.repos[i].Name] = true
	}
	// 2. Type '/'
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if len(model.filteredRepos) != 3 {
		t.Errorf("Expected filteredRepos not to be wiped on slash prefix, got %d", len(model.filteredRepos))
	}
	// 3. Type 'n', 'o', 'n', 'e'
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if model.filter != "/none" {
		t.Errorf("Expected filter to be '/none', got %q", model.filter)
	}
	// 4. Press Enter
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.selected["a-repo"] || model.selected["b-repo"] || model.selected["c-repo"] {
		t.Errorf("Expected all repos to be deselected after typed /none and Enter")
	}

	// 5. Test typing '/e' and pressing 'tab'
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model.Update(tea.KeyMsg{Type: tea.KeyTab})
	if model.filter != "/exit" {
		t.Errorf("Expected Tab key to autocomplete '/e' to '/exit', got %q", model.filter)
	}

	// 6. Test typing '/a' and pressing 'enter' (prefix execution)
	model.filter = ""
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.selected["a-repo"] || !model.selected["b-repo"] || !model.selected["c-repo"] {
		t.Errorf("Expected all repos to be selected after typing /a and Enter")
	}
}

func TestSelectorModelCoverage(t *testing.T) {
	repos := []github.Repo{
		{Name: "c-repo", Language: "Python", Visibility: "Public"},
		{Name: "a-repo", Language: "Go", Visibility: "Private"},
		{Name: "b-repo", Language: "Rust", Visibility: "Public"},
	}

	// 1. Test View states: Loading and Error
	mLoading := NewSelectorModel(func() ([]github.Repo, error) { return nil, nil })
	mLoading.loading = true
	viewLoad := mLoading.View()
	if !strings.Contains(viewLoad, "Loading repositories...") {
		t.Errorf("Expected Loading repositories view, got %q", viewLoad)
	}

	mErr := NewSelectorModel(func() ([]github.Repo, error) { return nil, nil })
	mErr.loading = false
	mErr.loadingErr = fmt.Errorf("my mock error")
	viewErr := mErr.View()
	if !strings.Contains(viewErr, "Error: my mock error") {
		t.Errorf("Expected mock error view, got %q", viewErr)
	}

	// 2. Test Help Menu Rendering
	mHelp := NewSelectorModel(func() ([]github.Repo, error) { return repos, nil })
	mHelp.loading = false
	mHelp.showHelp = true
	viewHelp := mHelp.View()
	if !strings.Contains(viewHelp, "In-Session Commands") {
		t.Errorf("Expected In-Session Commands in help view, got %q", viewHelp)
	}

	// 3. Test Sorting Variants
	mSort := NewSelectorModel(func() ([]github.Repo, error) { return repos, nil })
	newM, _ := mSort.Update(fetchedReposMsg{repos: repos, err: nil})
	model := newM.(*selectorModel)

	// Sort private
	model.executeSlashCommand("/sort private")
	if model.filteredRepos[0].Name != "a-repo" {
		t.Errorf("Expected private repo (a-repo) first, got %s", model.filteredRepos[0].Name)
	}

	// Sort public
	model.executeSlashCommand("/sort public")
	if model.filteredRepos[0].Name != "b-repo" {
		t.Errorf("Expected public repo (b-repo) first, got %s", model.filteredRepos[0].Name)
	}

	// Sort vis / visibility
	model.executeSlashCommand("/sort vis")
	if model.filteredRepos[0].Visibility != "Private" {
		t.Errorf("Expected visibility sorted (Private first), got %s", model.filteredRepos[0].Visibility)
	}

	// Sort lang
	model.executeSlashCommand("/sort lang")
	if model.filteredRepos[0].Language != "Go" {
		t.Errorf("Expected Go first, got %s", model.filteredRepos[0].Language)
	}

	// Sort empty argument
	model.executeSlashCommand("/sort")
	if !strings.Contains(model.cmdErr, "Usage:") {
		t.Errorf("Expected Usage error for empty sort")
	}

	// Sort invalid language
	model.executeSlashCommand("/sort invalid_lang_name")
	if !strings.Contains(model.cmdErr, "Unknown sort field, visibility or language") {
		t.Errorf("Expected unknown field/language error, got %q", model.cmdErr)
	}

	// 4. Test Key Handlers Edge Cases
	mKeys := NewSelectorModel(func() ([]github.Repo, error) { return repos, nil })
	newM, _ = mKeys.Update(fetchedReposMsg{repos: repos, err: nil})
	modelKeys := newM.(*selectorModel)

	// Test Esc to clear help overlay
	modelKeys.showHelp = true
	newM, _ = modelKeys.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if newM.(*selectorModel).showHelp {
		t.Errorf("Expected Esc to dismiss help menu")
	}

	// Test '?' to toggle help overlay
	newM, _ = modelKeys.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !newM.(*selectorModel).showHelp {
		t.Errorf("Expected '?' key to show help menu")
	}
	newM, _ = newM.(*selectorModel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if newM.(*selectorModel).showHelp {
		t.Errorf("Expected '?' key to dismiss help menu when active")
	}

	// Test typing space into command filter
	modelKeys.filter = "/sort"
	newM, _ = modelKeys.Update(tea.KeyMsg{Type: tea.KeySpace})
	if newM.(*selectorModel).filter != "/sort " {
		t.Errorf("Expected Space key to append space inside slash command mode, got %q", newM.(*selectorModel).filter)
	}

	// Test backspace when filter is empty
	modelKeys.filter = ""
	newM, _ = modelKeys.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if newM.(*selectorModel).filter != "" {
		t.Errorf("Expected empty filter to remain empty after Backspace")
	}

	// Test runes keypress when loading (should ignore)
	mLoadKeys := NewSelectorModel(func() ([]github.Repo, error) { return repos, nil })
	mLoadKeys.loading = true
	newM, _ = mLoadKeys.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if newM.(*selectorModel).filter != "" {
		t.Errorf("Expected runes to be ignored when loading is active")
	}

	// Test space keypress when loading (should ignore)
	_, _ = mLoadKeys.Update(tea.KeyMsg{Type: tea.KeySpace})
	// Test enter keypress when loading (should ignore)
	_, _ = mLoadKeys.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// Test backspace when loading (should ignore)
	_, _ = mLoadKeys.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	// Test ctrl+a / ctrl+n when loading (should ignore)
	_, _ = mLoadKeys.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	_, _ = mLoadKeys.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
}

func TestRemainingSelectorStates(t *testing.T) {
	t.Setenv("CORRAL_SHOW_LOGO", "0")
	progressModel := NewModel(0).(model)
	if view := progressModel.View(); !strings.Contains(view, "Corral - Organising Repositories") {
		t.Fatalf("compact progress header missing: %q", view)
	}

	assertCancelledRunnerErrors(t)

	repos := []github.Repo{
		{Name: "alpha", Language: "Go", Visibility: "Private"},
		{Name: "beta", Language: "Python", Visibility: "Public"},
		{Name: "gamma", Language: "Rust", Visibility: "Public"},
	}
	m := NewSelectorModel(func() ([]github.Repo, error) { return repos, nil })
	if _, cmd := m.Update(spinner.TickMsg{}); cmd == nil {
		t.Fatal("spinner tick must schedule its next update")
	}
	if _, cmd := m.Update(fetchedReposMsg{err: errors.New("fetch failed")}); cmd == nil || m.loadingErr == nil {
		t.Fatal("fetch failure must stop the selector")
	}
	m.loadingErr = nil
	m.loading = false
	m.repos = repos
	m.filteredRepos = repos
	m.updateTableRows()
	m.table.SetCursor(2)
	m.filter = "alpha"
	m.applyFilter()
	if m.table.Cursor() != 0 {
		t.Fatalf("filtered cursor = %d", m.table.Cursor())
	}
	m.table.SetCursor(-1)
	m.filter = ""
	m.applyFilter()
	if m.table.Cursor() != 0 {
		t.Fatalf("negative cursor was not normalized: %d", m.table.Cursor())
	}

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil || !m.quitting {
		t.Fatal("ctrl+c must quit")
	}
	m.quitting = false
	m.filter = "/"
	if view := m.View(); !strings.Contains(view, "Command:") {
		t.Fatalf("command prompt missing: %q", view)
	}
	m.filter = "/sort"
	m.cmdErr = "bad command"
	if view := m.View(); !strings.Contains(view, "bad command") {
		t.Fatalf("command error missing: %q", view)
	}

	if cmd := m.executeSlashCommand("   "); cmd != nil {
		t.Fatal("blank slash command returned a command")
	}
	m.executeSlashCommand("/help")
	if !m.showHelp {
		t.Fatal("/help did not enable help")
	}
	m.filteredRepos = []github.Repo{repos[0], repos[1]}
	m.repos = append([]github.Repo(nil), m.filteredRepos...)
	m.executeSlashCommand("/sort go")
	if m.filteredRepos[0].Language != "Go" {
		t.Fatalf("language preference sort = %+v", m.filteredRepos)
	}

	oldVersion := Version
	t.Cleanup(func() { Version = oldVersion })
	Version = ""
	if footer := m.renderFooter(); !strings.Contains(footer, "vdev") {
		t.Fatalf("development footer = %q", footer)
	}
	Version = strings.Repeat("v", 100)
	if footer := m.renderFooter(); !strings.Contains(footer, Version) {
		t.Fatalf("long-version footer = %q", footer)
	}
}
