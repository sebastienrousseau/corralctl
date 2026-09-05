//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"
)

// headingSlug mirrors the slug that scripts/anchor_headings.py derives from
// a heading's text. The package index links to these, so the two must agree;
// if they diverge the index silently points at nothing.
//
// It handles ASCII only, which is sufficient because it is applied to import
// paths and those cannot contain anything else. The Python side additionally
// strips combining marks, so an accented heading slugs the same way there;
// matching that here would mean taking a dependency on x/text for input that
// cannot occur.
func headingSlug(text string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(text) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

type DocData struct {
	Packages []PkgDoc
	// Date, PackageCount and DeclCount are front-matter values the theme
	// renders, so they are computed before the template runs rather than
	// printed afterwards.
	Date         string
	PackageCount int
	DeclCount    int
}

type PkgDoc struct {
	Name string
	Path string
	// Slug is the id the rendered heading will carry. ssg emits no heading
	// ids, so scripts/anchor_headings.py derives them from the heading text
	// after the build; this must agree with that derivation or the package
	// index links at nothing.
	Slug string
	Doc  string
	// Anchor is the slug used for in-page links.
	Anchor string
	// Importable is true when code outside this module may import the
	// package. Everything under internal/ cannot be, and neither can a
	// main package, so printing an import statement for those would be
	// telling the reader to write something that does not compile.
	Importable bool
	// Kind labels why a package is not importable: "internal" or
	// "program". Empty when it is importable.
	Kind string
	// ImportPath is the full module path, set only when Importable.
	ImportPath string
	// SourceURL points at the package directory on GitHub.
	SourceURL string
	Funcs     []FuncDoc
	Types     []TypeDoc
}

type FuncDoc struct {
	Name   string
	Decl   string
	Doc    string
	Anchor string
}

type TypeDoc struct {
	Name    string
	Doc     string
	Decl    string
	Anchor  string
	Methods []FuncDoc
}

// The site is built by ssg using the vendored Lucid theme, so this emits
// Markdown with the theme's front matter rather than a whole HTML page. The
// previous version carried its own dark palette, its own layout and a Google
// Fonts link, none of which matched the rest of the project's documentation
// and none of which had been through an accessibility gate.
//
// Heading levels matter here and are not cosmetic: the theme's accessibility
// suite asserts that no heading level is skipped, so the three h2 sections
// take h3 per package and h4 per declaration.
//
// FENCE and TICK stand in for backticks. A Go raw string cannot contain one,
// and splicing them in with concatenation produced a template that looked
// right and did not compile, so they are substituted once at startup instead.
const (
	fenceToken = "@@FENCE@@"
	tickToken  = "@@TICK@@"
)

const markdownTemplate = `---
author: "Sebastien Rousseau"
date: "{{.Date}}"
language: "en-GB"
schema: "page"
changefreq: "weekly"
copyright_year: "2026"
locale_path: "/"
base_path: "/"
name: "Corral"
short_name: "CO"
slug_install: "installation"
slug_usage: "usage"
slug_mcp: "mcp"
slug_ref: "reference"
nav_home: "Home"
nav_install: "Installation"
nav_usage: "Usage"
nav_mcp: "MCP Server"
nav_ref: "Reference"
label_skip: "Skip to main content"
label_menu: "Menu"
label_nav: "Main"
label_theme: "Theme"
label_theme_system: "System"
label_docs: "Documentation"
label_footer_nav: "Documentation"
label_docs_nav: "Documentation sections"
label_crumbs: "Breadcrumb"
label_pager: "Page"
label_prev: "Previous"
label_next: "Next"
label_toc: "On this page"
screenshot_alt: "Corral organising GitHub repositories into a Finder-friendly directory hierarchy."
footer_note: "Corral clones and organises GitHub repositories into a Finder-friendly hierarchy. Published under GPL-3.0-only."
copyright: "(c) 2026 Sebastien Rousseau. Licensed under GPL-3.0-only."
translation_key: "reference"
title: "Package Reference - Corral"
description: "Generated reference for the {{.PackageCount}} packages in the Corral module, covering {{.DeclCount}} exported declarations."
keywords: "corral packages, go reference, api documentation"
eyebrow: "Generated"
headline: "Package Reference"
lead: "Generated from the source on every build. These packages are deliberately not a public API - the interfaces Corral offers are its command line and its MCP server."
cur_install: ""
cur_usage: ""
cur_mcp: ""
cur_ref: ' aria-current="page"'
toc_1: "Package index"
toc_1_id: "package-index"
toc_2: "Packages"
toc_2_id: "packages"
toc_3: "How to read this"
toc_3_id: "how-to-read-this"
prev_href: "/mcp/"
prev_label: "MCP Server"
next_href: "/"
next_label: "Home"
layout: "doc"
---

## Package index

{{range .Packages}}- [{{.Path}}](#{{.Slug}}){{if not .Importable}} - {{if eq .Kind "internal"}}internal{{else}}command{{end}}{{end}}
{{end}}
## Packages
{{range .Packages}}
### {{.Path}}
{{if .Doc}}
{{.Doc}}
{{end}}{{if .Importable}}
@@FENCE@@go
import "{{.ImportPath}}"
@@FENCE@@
{{else if eq .Kind "internal"}}
Go refuses an @@TICK@@internal/@@TICK@@ path across module boundaries, so this package cannot be
imported from outside the module. [Read the source]({{.SourceURL}}).
{{else}}
A command, not a library. [Read the source]({{.SourceURL}}).
{{end}}{{range .Funcs}}
#### {{.Name}}

@@FENCE@@go
{{.Decl}}
@@FENCE@@
{{if .Doc}}
{{.Doc}}
{{end}}{{end}}{{range .Types}}
#### {{.Name}}

@@FENCE@@go
{{.Decl}}
@@FENCE@@
{{if .Doc}}
{{.Doc}}
{{end}}{{range .Methods}}
#### {{.Name}}

@@FENCE@@go
{{.Decl}}
@@FENCE@@
{{if .Doc}}
{{.Doc}}
{{end}}{{end}}{{end}}{{end}}

## How to read this

Only exported declarations appear here. Unexported helpers are implementation
detail and change without notice, so publishing them would suggest a stability
this module does not offer.

Where a package cannot be imported, this page says so and links to the source
instead of printing an import statement that would not compile. Six of these
packages sit under @@TICK@@internal/@@TICK@@, which Go refuses to resolve
across module boundaries, and @@TICK@@cmd/corralctl@@TICK@@ is a program
rather than a library.

The interfaces Corral offers the outside world are its command line, described
under [Usage](/usage/), and its [MCP server](/mcp/). Those are the surfaces to
build against.
`

func formatFuncDecl(fset *token.FileSet, decl *ast.FuncDecl) string {
	tmp := *decl
	tmp.Body = nil
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, &tmp)
	return buf.String()
}

func formatDecl(fset *token.FileSet, decl ast.Decl) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, decl)
	return buf.String()
}

const (
	modulePath = "github.com/sebastienrousseau/corralctl"
	sourceRoot = "https://github.com/sebastienrousseau/corralctl/tree/main"
)

// skipDirs are directories that hold Go files which are not part of the
// module's package set: build-ignored example and tooling programs, plus
// generated output and version control metadata.
var skipDirs = map[string]bool{
	".git": true, "scripts": true, "examples": true,
	"public": true, "testdata": true, "vendor": true, "docs": true,
}

// discoverPackages walks the module for directories holding non-test Go
// files, in sorted order.
//
// This used to be a hardcoded list of five paths, and it went stale the
// moment a package was added: internal/diag shipped in v0.0.26 and never
// appeared on the site, while the documentation-coverage gate counted it.
// The gate checked eight packages and the published site showed five.
// Discovering them removes the possibility rather than the current
// instance.
func discoverPackages() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != "." && (skipDirs[name] || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return readErr
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				if path != "." {
					paths = append(paths, filepath.ToSlash(path))
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("found no packages to document")
	}
	sort.Strings(paths)
	return paths, nil
}

// importable reports whether code outside this module may import the
// package. Go refuses an internal/ path across module boundaries, and a
// main package is a program rather than a library, so in both cases an
// import statement would not compile for a reader who copied it.
func importable(path, pkgName string) bool {
	if pkgName == "main" {
		return false
	}
	return path != "internal" &&
		!strings.HasPrefix(path, "internal/") &&
		!strings.Contains(path, "/internal/")
}

// anchor builds a stable, URL-safe in-page identifier.
func anchor(scope, name string) string {
	s := strings.ToLower(scope + "-" + name)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func main() {
	fset := token.NewFileSet()
	var docData DocData

	paths, err := discoverPackages()
	if err != nil {
		fmt.Fprintf(os.Stderr, "discovering packages: %v\n", err)
		os.Exit(1)
	}
	for _, p := range paths {
		pkgs, err := parser.ParseDir(fset, p, func(info os.FileInfo) bool {
			return !strings.HasSuffix(info.Name(), "_test.go")
		}, parser.ParseComments)

		if err != nil {
			fmt.Printf("Error parsing dir %s: %v\n", p, err)
			continue
		}

		for name, pkg := range pkgs {
			// Mode 0, not doc.AllDecls: this is documentation, and an
			// unexported helper is not part of any surface a reader can
			// use. The previous setting published 173 private
			// declarations — three quarters of the page — including
			// several with no doc comment at all, which rendered as
			// empty paragraphs.
			d := doc.New(pkg, p, 0)
			var pkgDoc PkgDoc
			pkgDoc.Name = name
			pkgDoc.Path = p
			pkgDoc.Doc = d.Doc
			pkgDoc.Anchor = anchor("pkg", p)
			pkgDoc.Slug = headingSlug(p)
			pkgDoc.SourceURL = sourceRoot + "/" + p
			pkgDoc.Importable = importable(p, name)
			switch {
			case pkgDoc.Importable:
				pkgDoc.ImportPath = modulePath + "/" + p
			case name == "main":
				pkgDoc.Kind = "program"
			default:
				pkgDoc.Kind = "internal"
			}

			// Functions
			for _, f := range d.Funcs {
				pkgDoc.Funcs = append(pkgDoc.Funcs, FuncDoc{
					Name:   f.Name,
					Decl:   formatFuncDecl(fset, f.Decl),
					Doc:    f.Doc,
					Anchor: anchor(p, f.Name),
				})
			}

			// Types
			for _, t := range d.Types {
				typeDoc := TypeDoc{
					Name:   t.Name,
					Doc:    t.Doc,
					Decl:   formatDecl(fset, t.Decl),
					Anchor: anchor(p, t.Name),
				}
				for _, m := range t.Methods {
					typeDoc.Methods = append(typeDoc.Methods, FuncDoc{
						Name:   m.Name,
						Decl:   formatFuncDecl(fset, m.Decl),
						Doc:    m.Doc,
						Anchor: anchor(p, t.Name+"."+m.Name),
					})
				}
				pkgDoc.Types = append(pkgDoc.Types, typeDoc)
			}

			docData.Packages = append(docData.Packages, pkgDoc)
		}
	}

	// Counted before rendering: the front matter quotes both numbers, and a
	// figure printed after the fact could disagree with the page.
	exported := 0
	for _, pkg := range docData.Packages {
		exported += len(pkg.Funcs)
		for _, t := range pkg.Types {
			exported += 1 + len(t.Methods)
		}
	}
	docData.PackageCount = len(docData.Packages)
	docData.DeclCount = exported
	docData.Date = time.Now().UTC().Format("2006-01-02")

	outDir := filepath.Join("docs-site", "content")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		fmt.Printf("Error creating %s: %v\n", outDir, err)
		os.Exit(1)
	}

	outPath := filepath.Join(outDir, "reference.md")
	f, err := os.Create(outPath) // #nosec G304 -- fixed path within the repo
	if err != nil {
		fmt.Printf("Error creating %s: %v\n", outPath, err)
		os.Exit(1)
	}
	defer f.Close()

	src := strings.ReplaceAll(markdownTemplate, fenceToken, "```")
	src = strings.ReplaceAll(src, tickToken, "`")

	tmpl, err := template.New("docs").Parse(src)
	if err != nil {
		fmt.Printf("Error parsing template: %v\n", err)
		os.Exit(1)
	}

	if err := tmpl.Execute(f, docData); err != nil {
		fmt.Printf("Error executing template: %v\n", err)
		os.Exit(1)
	}

	// A count, not a cheer. An earlier version reported success while
	// publishing five of eight packages and 173 unexported helpers, so the
	// message says what was produced and lets the reader judge it.
	fmt.Printf("Generated %s: %d packages, %d exported declarations\n",
		outPath, docData.PackageCount, docData.DeclCount)
}
