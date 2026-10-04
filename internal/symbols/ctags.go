// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// CtagsJSONRecord models one line emitted by universal-ctags with --output-format=json.
type CtagsJSONRecord struct {
	Type      string `json:"_type"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Pattern   string `json:"pattern"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
	Language  string `json:"language"`
}

// MapCtagsKind maps ctags kind identifiers to corral Kind.
func MapCtagsKind(raw string) Kind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "f", "func", "function", "sub", "subroutine", "procedure":
		return KindFunc
	case "m", "method":
		return KindMethod
	case "c", "class", "s", "struct", "e", "enum", "t", "type", "typedef", "module", "record":
		return KindType
	case "i", "interface", "protocol", "trait":
		return KindInterface
	case "d", "macro", "define", "constant", "const":
		return KindConst
	case "v", "variable", "var", "field", "property", "member":
		return KindVar
	default:
		return KindFunc
	}
}

// ParseCtagsJSON parses Universal Ctags JSON output stream into symbols.
func ParseCtagsJSON(r io.Reader) ([]Symbol, error) {
	scanner := bufio.NewScanner(r)
	var syms []Symbol
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec CtagsJSONRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Name == "" || rec.Name == "_" {
			continue
		}
		lineNum := rec.Line
		if lineNum <= 0 {
			lineNum = 1
		}
		lang := strings.ToLower(rec.Language)
		if lang == "" {
			ext := filepath.Ext(rec.Path)
			lang = strings.TrimPrefix(ext, ".")
		}
		syms = append(syms, Symbol{
			Name:     rec.Name,
			Kind:     MapCtagsKind(rec.Kind),
			File:     filepath.ToSlash(rec.Path),
			Line:     lineNum,
			Receiver: rec.Scope,
			Exported: true,
			Language: lang,
		})
	}
	return syms, scanner.Err()
}

// ParseCtagsTags parses standard Exuberant/Universal Ctags tags format into symbols.
func ParseCtagsTags(r io.Reader) ([]Symbol, error) {
	scanner := bufio.NewScanner(r)
	var syms []Symbol
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "!_TAG_") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		name := parts[0]
		file := filepath.ToSlash(parts[1])
		addr := parts[2]

		lineNum := 1
		cleanAddr := addr
		if idx := strings.Index(cleanAddr, ";\""); idx >= 0 {
			cleanAddr = cleanAddr[:idx]
		}
		if n, err := strconv.Atoi(cleanAddr); err == nil && n > 0 {
			lineNum = n
		}

		kindRaw := "func"
		receiver := ""
		if len(parts) > 3 {
			for _, extra := range parts[3:] {
				extra = strings.TrimSpace(extra)
				if len(extra) == 1 {
					kindRaw = extra
				} else if strings.HasPrefix(extra, "kind:") {
					kindRaw = strings.TrimPrefix(extra, "kind:")
				} else if strings.HasPrefix(extra, "class:") {
					receiver = strings.TrimPrefix(extra, "class:")
				} else if strings.HasPrefix(extra, "struct:") {
					receiver = strings.TrimPrefix(extra, "struct:")
				}
			}
		}

		ext := filepath.Ext(file)
		lang := strings.TrimPrefix(ext, ".")
		if lang == "" {
			lang = "ctags"
		}

		syms = append(syms, Symbol{
			Name:     name,
			Kind:     MapCtagsKind(kindRaw),
			File:     file,
			Line:     lineNum,
			Receiver: receiver,
			Exported: true,
			Language: lang,
		})
	}
	return syms, scanner.Err()
}

var execCtags = func(ctx context.Context, cmd string, path string, src []byte) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd, "--output-format=json", "--fields=* -x", "-")
	c.Stdin = bytes.NewReader(src)
	return c.Output()
}

// CtagsExtractor is an Extractor that uses universal-ctags to parse declarations for arbitrary languages.
type CtagsExtractor struct {
	Command string
}

// Language returns "ctags".
func (c CtagsExtractor) Language() string {
	return "ctags"
}

// Extensions returns nil since CtagsExtractor is an unconstrained polyglot fallback.
func (c CtagsExtractor) Extensions() []string {
	return nil
}

// Extract invokes ctags to parse src into symbols.
func (c CtagsExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	cmd := c.Command
	if cmd == "" {
		cmd = "ctags"
	}
	out, err := execCtags(context.Background(), cmd, path, src)
	if err != nil {
		return nil, nil //nolint:nilerr // graceful degradation when external tool is absent or fails
	}
	syms, _ := ParseCtagsJSON(bytes.NewReader(out))
	for i := range syms {
		if syms[i].File == "" || syms[i].File == "-" {
			syms[i].File = filepath.ToSlash(path)
		}
	}
	return syms, nil
}
