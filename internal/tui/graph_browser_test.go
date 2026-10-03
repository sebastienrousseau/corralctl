// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/graph"
)

func TestGraphBrowserModelNavigation(t *testing.T) {
	g := &graph.Graph{
		Nodes: []graph.Node{
			{Name: "repo-a", Path: "/a", Language: "Go", PackageName: "pkgA", Dependencies: []string{"repo-b"}},
			{Name: "repo-b", Path: "/b", Language: "", PackageName: "", Dependencies: nil},
		},
		Dependents: map[string][]string{
			"repo-b": {"repo-a"},
		},
	}

	m := NewGraphBrowserModel(g)
	if cmd := m.Init(); cmd != nil {
		t.Errorf("expected Init to return nil, got %v", cmd)
	}

	// 1. Move down with down and j
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = res.(GraphBrowserModel)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	// Boundary check at bottom
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(GraphBrowserModel)
	if m.cursor != 1 {
		t.Fatalf("cursor at bottom = %d, want 1", m.cursor)
	}

	// 2. Move up with up and k
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = res.(GraphBrowserModel)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}

	// Boundary check at top
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = res.(GraphBrowserModel)
	if m.cursor != 0 {
		t.Fatalf("cursor at top = %d, want 0", m.cursor)
	}

	// 3. Other key message
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = res.(GraphBrowserModel)
	if m.cursor != 0 {
		t.Fatalf("cursor after right = %d, want 0", m.cursor)
	}

	// Non-key message
	res, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = res.(GraphBrowserModel)
	if m.cursor != 0 {
		t.Fatalf("cursor after window size = %d, want 0", m.cursor)
	}

	// 4. Quit keys
	quitKeys := []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	}
	for _, qk := range quitKeys {
		fresh := NewGraphBrowserModel(g)
		qRes, cmd := fresh.Update(qk)
		qModel := qRes.(GraphBrowserModel)
		if !qModel.quitting {
			t.Fatalf("expected quitting=true for key %v", qk)
		}
		if cmd == nil {
			t.Fatal("expected quit cmd, got nil")
		}
	}
}

func TestGraphBrowserModelView(t *testing.T) {
	// 1. Quitting view
	mQuit := GraphBrowserModel{quitting: true}
	if v := mQuit.View(); v != "" {
		t.Fatalf("expected empty view when quitting, got %q", v)
	}

	// 2. Empty graph view
	mEmpty := GraphBrowserModel{}
	if v := mEmpty.View(); !strings.Contains(v, "No repositories in graph") {
		t.Fatalf("expected No repositories message, got: %s", v)
	}

	// 3. Populated graph view
	g := &graph.Graph{
		Nodes: []graph.Node{
			{Name: "repo-a", Path: "/a", Language: "Go", PackageName: "pkgA", Dependencies: []string{"repo-b"}},
			{Name: "repo-b", Path: "/b", Language: "", PackageName: "", Dependencies: nil},
		},
		Dependents: map[string][]string{
			"repo-b": {"repo-a"},
		},
	}
	m := NewGraphBrowserModel(g)

	// View for cursor 0 (has language, package, dependencies, no dependents)
	v0 := m.View()
	if !strings.Contains(v0, "repo-a") || !strings.Contains(v0, "Package:      pkgA") || !strings.Contains(v0, "repo-b") {
		t.Fatalf("unexpected view for cursor 0:\n%s", v0)
	}

	// View for cursor 1 (no language, no package, no dependencies, has dependent repo-a)
	m.cursor = 1
	v1 := m.View()
	if !strings.Contains(v1, "repo-b") || !strings.Contains(v1, "none") || !strings.Contains(v1, "repo-a") {
		t.Fatalf("unexpected view for cursor 1:\n%s", v1)
	}
}

func TestRunGraphBrowser(t *testing.T) {
	origRunner := runGraphProgram
	t.Cleanup(func() { runGraphProgram = origRunner })

	var launchedModel tea.Model
	runGraphProgram = func(m tea.Model) error {
		launchedModel = m
		return nil
	}

	g := &graph.Graph{
		Nodes: []graph.Node{{Name: "test-repo"}},
	}
	if err := RunGraphBrowser(g); err != nil {
		t.Fatalf("RunGraphBrowser failed: %v", err)
	}
	if launchedModel == nil {
		t.Fatal("expected model to be passed to runGraphProgram")
	}

	// Error path
	runGraphProgram = func(m tea.Model) error {
		return errors.New("simulated tui error")
	}
	if err := RunGraphBrowser(g); err == nil || !strings.Contains(err.Error(), "simulated tui error") {
		t.Fatalf("expected simulated tui error, got: %v", err)
	}

	// Exercise default runGraphProgram implementation with an immediate quit model
	runGraphProgram = origRunner
	if err := runGraphProgram(immediateQuitModel{}); err != nil {
		t.Fatalf("default runGraphProgram failed: %v", err)
	}
}

type immediateQuitModel struct{}

func (immediateQuitModel) Init() tea.Cmd                       { return tea.Quit }
func (immediateQuitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return immediateQuitModel{}, tea.Quit }
func (immediateQuitModel) View() string                        { return "" }

