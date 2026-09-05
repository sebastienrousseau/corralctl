// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build ignore

// Command gen_docs renders the distributable documentation artefacts —
// section-1 manpages and shell completions — from the live cobra command
// tree.
//
// Generated, never committed. A hand-written .1 drifts from `--help` the
// first time a flag changes and nothing catches it; deriving both from the
// same command tree that serves `--help` makes drift impossible rather than
// merely discouraged.
//
// The roff is emitted directly rather than via cobra/doc's GenManTree,
// which would pull cpuguy83/go-md2man into go.mod for a build-time concern.
// This module keeps eleven direct dependencies and a hand-maintained
// SBOM.md that CI checks against go.mod, so a new dependency is not free.
//
// The output directory must not be goreleaser's dist/: goreleaser cleans that
// directory *after* running its before-hooks, so anything generated into it is
// deleted before packaging. build/ is used instead.
//
// Usage:
//
//	go run scripts/gen_docs.go [outdir]   # default: build/
//
// Produces:
//
//	<outdir>/man/corralctl.1, corralctl-<sub>.1, …
//	<outdir>/completions/corralctl.{bash,zsh,fish,ps1}
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	manSection = "1"
	manSource  = "corralctl"
	manManual  = "corralctl Manual"
)

func main() {
	outDir := "build"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	manDir := filepath.Join(outDir, "man")
	compDir := filepath.Join(outDir, "completions")
	for _, d := range []string{manDir, compDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("create %s: %v", d, err)
		}
	}

	root := cmd.RootCommand()
	// The date is fixed to the build's source date where one is provided, so
	// two builds of the same commit produce identical manpages. SOURCE_DATE_EPOCH
	// is the reproducible-builds convention; goreleaser sets it from the commit.
	date := buildDate()

	var pages int
	for _, c := range walk(root) {
		name := manName(c)
		body := renderMan(c, root, date)
		path := filepath.Join(manDir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // G306: manpages are world-readable by design
			fail("write %s: %v", path, err)
		}
		pages++
	}

	completions := []struct {
		file string
		gen  func(*cobra.Command, *bytes.Buffer) error
	}{
		{"corralctl.bash", func(c *cobra.Command, b *bytes.Buffer) error { return c.GenBashCompletionV2(b, true) }},
		{"corralctl.zsh", func(c *cobra.Command, b *bytes.Buffer) error { return c.GenZshCompletion(b) }},
		{"corralctl.fish", func(c *cobra.Command, b *bytes.Buffer) error { return c.GenFishCompletion(b, true) }},
		{"corralctl.ps1", func(c *cobra.Command, b *bytes.Buffer) error { return c.GenPowerShellCompletionWithDesc(b) }},
	}
	for _, comp := range completions {
		var buf bytes.Buffer
		if err := comp.gen(root, &buf); err != nil {
			fail("generate %s: %v", comp.file, err)
		}
		path := filepath.Join(compDir, comp.file)
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil { //nolint:gosec // G306: completions are world-readable by design
			fail("write %s: %v", path, err)
		}
	}

	fmt.Printf("gen_docs: %d manpage(s) in %s, %d completion(s) in %s\n",
		pages, manDir, len(completions), compDir)
}

// walk returns root and every reachable non-hidden subcommand.
func walk(c *cobra.Command) []*cobra.Command {
	out := []*cobra.Command{c}
	for _, sub := range c.Commands() {
		if !sub.IsAvailableCommand() || sub.Name() == "help" {
			continue
		}
		out = append(out, walk(sub)...)
	}
	return out
}

// manName is the filename for a command's page: corralctl.1 for the root,
// corralctl-mcp.1 for a subcommand.
func manName(c *cobra.Command) string {
	return strings.ReplaceAll(c.CommandPath(), " ", "-") + "." + manSection
}

// renderMan emits a section-1 page for one command.
func renderMan(c, root *cobra.Command, date string) string {
	var b strings.Builder
	name := c.CommandPath()

	fmt.Fprintf(&b, ".TH %q %s %q %q %q\n",
		strings.ToUpper(strings.ReplaceAll(name, " ", "-")),
		manSection, date, manSource+" "+root.Version, manManual)

	b.WriteString(".SH NAME\n")
	fmt.Fprintf(&b, "%s \\- %s\n", roff(name), roff(firstLine(c.Short)))

	b.WriteString(".SH SYNOPSIS\n")
	fmt.Fprintf(&b, ".B %s\n%s\n", roff(name), roff(useLine(c)))

	b.WriteString(".SH DESCRIPTION\n")
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	b.WriteString(paragraphs(desc))

	if flags := renderFlags(c.NonInheritedFlags()); flags != "" {
		b.WriteString(".SH OPTIONS\n")
		b.WriteString(flags)
	}
	if flags := renderFlags(c.InheritedFlags()); flags != "" {
		b.WriteString(".SH OPTIONS INHERITED FROM PARENT COMMANDS\n")
		b.WriteString(flags)
	}

	if subs := c.Commands(); len(subs) > 0 {
		var listed []string
		for _, sub := range subs {
			if sub.IsAvailableCommand() && sub.Name() != "help" {
				listed = append(listed, sub.Name())
			}
		}
		if len(listed) > 0 {
			b.WriteString(".SH COMMANDS\n")
			for _, sub := range subs {
				if !sub.IsAvailableCommand() || sub.Name() == "help" {
					continue
				}
				fmt.Fprintf(&b, ".TP\n.B %s\n%s\n", roff(sub.Name()), roff(firstLine(sub.Short)))
			}
		}
	}

	if c.Example != "" {
		b.WriteString(".SH EXAMPLES\n.nf\n")
		b.WriteString(roff(c.Example))
		b.WriteString("\n.fi\n")
	}

	b.WriteString(".SH SEE ALSO\n")
	seeAlso(&b, c, root)

	b.WriteString(".SH REPORTING BUGS\n")
	b.WriteString("Report issues at https://github.com/sebastienrousseau/corralctl/issues\n")
	b.WriteString(".SH COPYRIGHT\n")
	b.WriteString("Copyright \\(co 2026 Sebastien Rousseau. Licensed under GPL-3.0-only.\n")
	return b.String()
}

// seeAlso cross-references the root page from a subcommand and every
// subcommand page from the root, so `man corralctl` is a usable index.
func seeAlso(b *strings.Builder, c, root *cobra.Command) {
	var refs []string
	if c != root {
		refs = append(refs, fmt.Sprintf("\\fB%s\\fR(%s)", strings.ReplaceAll(root.CommandPath(), " ", "-"), manSection))
	}
	for _, sub := range c.Commands() {
		if !sub.IsAvailableCommand() || sub.Name() == "help" {
			continue
		}
		refs = append(refs, fmt.Sprintf("\\fB%s\\fR(%s)",
			strings.ReplaceAll(sub.CommandPath(), " ", "-"), manSection))
	}
	if len(refs) == 0 {
		b.WriteString("https://doc.corrallib.com\n")
		return
	}
	b.WriteString(strings.Join(refs, ", ") + "\n")
}

// renderFlags emits a .TP block per flag, skipping hidden ones.
func renderFlags(fs *pflag.FlagSet) string {
	var b strings.Builder
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		b.WriteString(".TP\n")
		var head strings.Builder
		if f.Shorthand != "" {
			fmt.Fprintf(&head, "\\fB\\-%s\\fR, ", f.Shorthand)
		}
		fmt.Fprintf(&head, "\\fB\\-\\-%s\\fR", f.Name)
		if f.Value.Type() != "bool" {
			fmt.Fprintf(&head, " \\fI%s\\fR", f.Value.Type())
		}
		b.WriteString(head.String() + "\n")
		usage := roff(f.Usage)
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "[]" {
			usage += fmt.Sprintf(" (default: %s)", roff(f.DefValue))
		}
		b.WriteString(usage + "\n")
	})
	return b.String()
}

// useLine renders the argument grammar without repeating the command name.
func useLine(c *cobra.Command) string {
	line := c.UseLine()
	line = strings.TrimPrefix(line, c.CommandPath())
	return strings.TrimSpace(line)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// paragraphs turns blank-line-separated prose into roff paragraphs, keeping
// indented blocks verbatim so usage snippets survive.
func paragraphs(s string) string {
	var b strings.Builder
	for i, para := range strings.Split(strings.TrimSpace(s), "\n\n") {
		if i > 0 {
			b.WriteString(".PP\n")
		}
		if strings.HasPrefix(para, "  ") || strings.HasPrefix(para, "\t") {
			b.WriteString(".nf\n" + roff(para) + "\n.fi\n")
			continue
		}
		b.WriteString(roff(para) + "\n")
	}
	return b.String()
}

// roff escapes the characters that would otherwise be read as formatting.
// A leading dot or apostrophe starts a request, and a backslash starts an
// escape, so both must be neutralised or the page renders wrong.
func roff(s string) string {
	s = strings.ReplaceAll(s, `\`, `\e`)
	s = strings.ReplaceAll(s, "-", `\-`)
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "'") {
			line = `\&` + line
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// buildDate honours SOURCE_DATE_EPOCH so a manpage is reproducible.
func buildDate() string {
	if v := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); v != "" {
		var secs int64
		if _, err := fmt.Sscanf(v, "%d", &secs); err == nil {
			return time.Unix(secs, 0).UTC().Format("2006-01-02")
		}
	}
	return time.Now().UTC().Format("2006-01-02")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen_docs: "+format+"\n", args...)
	os.Exit(1)
}
