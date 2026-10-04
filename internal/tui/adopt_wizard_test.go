// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/discover"
)

func TestAdoptWizardModelKeysAndSelection(t *testing.T) {
	candidates := []discover.Candidate{
		{Name: "repo1", Path: "/a/repo1", DetectedLang: "Go"},
		{Name: "repo2", Path: "/b/repo2", DetectedLang: "Rust"},
	}

	m := NewAdoptWizardModel(candidates)
	if m.Init() != nil {
		t.Fatal("expected nil from Init")
	}

	// Verify view rendering
	view := m.View()
	if !strings.Contains(view, "repo1") || !strings.Contains(view, "repo2") {
		t.Fatalf("unexpected view content: %s", view)
	}

	// Toggle first item
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if m.Selected[0] {
		t.Fatal("expected item 0 to be deselected")
	}

	// Down arrow to item 1
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor != 1 {
		t.Fatalf("expected cursor at 1, got %d", m.Cursor)
	}

	// Down arrow again (at boundary)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor != 1 {
		t.Fatalf("expected cursor at 1, got %d", m.Cursor)
	}

	// Up arrow with 'k'
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.Cursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", m.Cursor)
	}

	// Up arrow again (at boundary)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.Cursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", m.Cursor)
	}

	// Select none ('n')
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.Selected[0] || m.Selected[1] {
		t.Fatal("expected all items deselected")
	}

	// Select all ('a')
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !m.Selected[0] || !m.Selected[1] {
		t.Fatal("expected all items selected")
	}

	// Unhandled key
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	// Enter confirms
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.Confirmed || cmd == nil {
		t.Fatal("expected confirmation on Enter")
	}

	// Cancel / quit
	m.Quitting = false
	m.Confirmed = false
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !m.Quitting || cmd == nil {
		t.Fatal("expected quitting on q")
	}
	if m.View() != "" {
		t.Fatal("expected empty view when quitting")
	}
}

func TestRunAdoptWizard(t *testing.T) {
	ctx := context.Background()

	// Zero candidates
	res, ok, err := RunAdoptWizard(ctx, nil)
	if err != nil || ok || res != nil {
		t.Fatalf("expected nil for zero candidates, got %v, %v, %v", res, ok, err)
	}

	candidates := []discover.Candidate{
		{Name: "repo1", Path: "/a/repo1", DetectedLang: "Go"},
	}

	origRunner := runAdoptProgram
	defer func() { runAdoptProgram = origRunner }()

	// Program error
	runAdoptProgram = func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return nil, errors.New("terminal failure")
	}
	_, _, err = RunAdoptWizard(ctx, candidates)
	if err == nil {
		t.Fatal("expected program error")
	}

	// Quitting / canceled
	runAdoptProgram = func(ctx context.Context, m tea.Model) (tea.Model, error) {
		wm := m.(*AdoptWizardModel)
		wm.Quitting = true
		return wm, nil
	}
	res, ok, err = RunAdoptWizard(ctx, candidates)
	if err != nil || ok || res != nil {
		t.Fatalf("expected canceled outcome, got %v, %v, %v", res, ok, err)
	}

	// Confirmed selection
	runAdoptProgram = func(ctx context.Context, m tea.Model) (tea.Model, error) {
		wm := m.(*AdoptWizardModel)
		wm.Confirmed = true
		return wm, nil
	}
	res, ok, err = RunAdoptWizard(ctx, candidates)
	if err != nil || !ok || len(res) != 1 {
		t.Fatalf("expected 1 confirmed candidate, got %v, %v, %v", res, ok, err)
	}

	runAdoptProgram = origRunner
	assertDefaultAdoptRunner(t)
}
