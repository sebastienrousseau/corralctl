// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/sebastienrousseau/corralctl/internal/engine"
)

// resultPrinter renders a command's per-item results in the selected
// output format: text calls the command's own line renderer, ndjson
// writes one JSON object per result as it arrives, and json writes a
// single {"results": [...], "summary": {...}} document once the run is
// over. Results arrive from worker goroutines, so writes are serialised.
type resultPrinter[R, S any] struct {
	format  string
	text    func(R)
	mu      sync.Mutex
	results []R
}

func newResultPrinter[R, S any](format string, text func(R)) *resultPrinter[R, S] {
	return &resultPrinter[R, S]{format: format, text: text}
}

func (p *resultPrinter[R, S]) report(r R) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.format {
	case string(engine.OutputNDJSON):
		_ = writeNDJSON(os.Stdout, r)
	case string(engine.OutputJSON):
		p.results = append(p.results, r)
	default:
		p.text(r)
	}
}

// finish writes the JSON document, which needs every result and the
// summary, and is a no-op for the streaming formats.
func (p *resultPrinter[R, S]) finish(summary S) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.format != string(engine.OutputJSON) {
		return nil
	}
	if p.results == nil {
		p.results = []R{}
	}
	return writeJSON(os.Stdout, struct {
		Results []R `json:"results"`
		Summary S   `json:"summary"`
	}{p.results, summary})
}

// writeNDJSON writes one value as a single JSON line.
func writeNDJSON(target *os.File, value any) error {
	encoder := json.NewEncoder(target)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
