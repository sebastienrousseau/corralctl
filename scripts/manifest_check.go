// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build ignore

// Command manifest_check verifies that the repository's hand-maintained
// manifests still describe the repository.
//
// Two things here have drifted before, in both cases silently and in both
// cases into something a reader would reasonably trust:
//
//   - SBOM.md listed `go-github/v74` for six releases after go.mod moved to
//     v90, and omitted three direct dependencies entirely — while SECURITY.md
//     linked to it as the *full* bill of materials.
//   - server.json's version sat at 0.0.13 through five releases, so the MCP
//     registry advertised a stale OCI image tag.
//
// Neither is the kind of mistake review catches, because neither file is
// where the change happens. So they are checked mechanically instead:
//
//	go run scripts/manifest_check.go
//
// With -fix, the one class of drift that has an unambiguous repair is repaired
// rather than reported: a module already listed in SBOM.md whose version has
// moved on in go.mod. That is the dependabot case — dependabot updates go.mod
// and cannot touch SBOM.md, so every Go-module bump it opens fails this check
// and can never go green on its own.
//
//	go run scripts/manifest_check.go -fix
//
// Adding or removing a dependency is deliberately NOT repaired: a new row
// needs a Purpose and a Licence, and inventing either would defeat the point
// of the file. Those are still reported for a human.
//
// Checks performed:
//
//  1. SBOM.md's dependency table matches go.mod's direct requirements
//     exactly — same module set, same versions, in both directions.
//  2. server.json's version matches the newest release in CHANGELOG.md, and
//     its OCI image tag matches that same version.
//  3. No prose file quotes a base image that disagrees with the Dockerfile.
//  4. The OSPS self-assessment's tables and its prefilled form links agree.
//
// Exits non-zero, listing every mismatch, if any check fails.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// directRequire matches one non-indirect line inside go.mod's require block.
var directRequire = regexp.MustCompile(`^\s+(\S+)\s+(v\S+)\s*$`)

// sbomRow matches one dependency row of SBOM.md's table, whose first two
// cells are the backticked module path and its version.
var sbomRow = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|\\s*(v\\S+)\\s*\\|")

// changelogHeading matches a released version heading in CHANGELOG.md.
var changelogHeading = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+)\]`)

func main() {
	fix := flag.Bool("fix", false,
		"rewrite SBOM.md versions that go.mod has moved past, then re-check")
	flag.Parse()

	if *fix {
		fixed, err := fixSBOMVersions()
		if err != nil {
			fmt.Fprintf(os.Stderr, "manifest fix failed: %v\n", err)
			os.Exit(1)
		}
		for _, f := range fixed {
			fmt.Printf("fixed: %s\n", f)
		}
		if len(fixed) == 0 {
			fmt.Println("fix: nothing to change in SBOM.md")
		}
	}

	var problems []string
	problems = append(problems, checkSBOM()...)
	problems = append(problems, checkServerManifest()...)
	problems = append(problems, checkBaseImage()...)
	problems = append(problems, checkOSPSConsistency()...)
	problems = append(problems, checkChangelogLinks()...)
	problems = append(problems, checkVersionedProse()...)
	problems = append(problems, checkInstallSnippets()...)
	problems = append(problems, checkRegistryLimits()...)

	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "manifest check failed:")
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		os.Exit(1)
	}
	fmt.Println("Manifest check: SBOM.md, server.json and the prose docs agree with go.mod, CHANGELOG.md and the Dockerfile")
}

// checkVersionedProse verifies that documents quoting a concrete version
// still quote the current one.
//
// Both of these went stale unnoticed. pkg/VERIFY.md opens with
// "VERSION=0.0.29" and every command below it interpolates that, so a
// packager following the page downloads and verifies the wrong release —
// and gets a passing checksum for it, which is worse than a failure. The
// documentation site's hero_tag is rendered as the version badge on the
// homepage; it sat three releases behind.
//
// Neither is reachable from server.json or the Dockerfile, so the existing
// version check did not see them.
func checkVersionedProse() []string {
	released, err := latestChangelogVersion("CHANGELOG.md")
	if err != nil {
		return []string{fmt.Sprintf("reading CHANGELOG.md: %v", err)}
	}

	checks := []struct {
		path    string
		pattern *regexp.Regexp
		want    string
		why     string
	}{
		{
			"pkg/VERIFY.md", verifyDocVersion, released,
			"packagers interpolate it into every download and verification command",
		},
		{
			"docs-site/content/index.md", heroTagVersion, "v" + released,
			"it is rendered as the version badge on the documentation site's homepage",
		},
	}

	var problems []string
	for _, c := range checks {
		b, err := os.ReadFile(c.path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("reading %s: %v", c.path, err))
			continue
		}
		m := c.pattern.FindSubmatch(b)
		if m == nil {
			problems = append(problems, fmt.Sprintf(
				"%s no longer states a version where one is expected; "+
					"either restore it or drop this check", c.path))
			continue
		}
		if got := string(m[1]); got != c.want {
			problems = append(problems, fmt.Sprintf(
				"%s states version %s, newest release is %s — %s",
				c.path, got, c.want, c.why))
		}
	}
	return problems
}

// verifyDocVersion matches the VERSION assignment packagers copy.
var verifyDocVersion = regexp.MustCompile(`(?m)^VERSION=([0-9]+\.[0-9]+\.[0-9]+)\s*$`)

// registryMaxDescription is the MCP registry's limit on server.json's
// description, in characters.
//
// The registry enforces it at publish time and nowhere earlier, so a value
// over the limit is not a warning — it is a 422 that fails the release job,
// after the artefacts have already been published.
const registryMaxDescription = 100

// checkRegistryLimits keeps server.json publishable.
//
// v0.0.35 shipped its artefacts and then failed: the description had been
// rewritten from 91 characters to 143 while removing some GitHub-only
// wording, and the registry rejected it with
//
//	422 validation failed: expected length <= 100
//
// Nothing local knew the limit existed. The rewrite was correct prose and
// passed every gate the project had; it was simply unpublishable, and the
// only thing that could tell us was the registry itself, at the worst
// possible moment.
func checkRegistryLimits() []string {
	raw, err := os.ReadFile("server.json")
	if err != nil {
		return []string{fmt.Sprintf("reading server.json: %v", err)}
	}
	var manifest struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return []string{fmt.Sprintf("parsing server.json: %v", err)}
	}
	if manifest.Description == "" {
		return []string{"server.json states no description; the registry requires one"}
	}
	// Counted in runes, not bytes: the limit is a character count, and this
	// project's prose uses en dashes and curly quotes freely enough that the
	// two figures diverge.
	if n := utf8.RuneCountInString(manifest.Description); n > registryMaxDescription {
		return []string{fmt.Sprintf(
			"server.json description is %d characters; the MCP registry rejects anything "+
				"over %d, and it does so at publish time — after the release has shipped "+
				"its artefacts", n, registryMaxDescription)}
	}
	return nil
}

// downloadPin matches a release-download URL that hard-codes a version
// instead of interpolating one.
//
// The `${VERSION}` form in pkg/VERIFY.md is deliberately not matched: that
// file states its version once, on a line checkVersionedProse already
// guards, and every command below interpolates it.
var downloadPin = regexp.MustCompile(`releases/download/v([0-9]+\.[0-9]+\.[0-9]+)/`)

// installPin matches a version-pinned `go install`.
var installPin = regexp.MustCompile(`go install [^\s]+@v([0-9]+\.[0-9]+\.[0-9]+)`)

// snippetDocs are the documents a user copies commands out of.
//
// CHANGELOG.md is excluded on purpose: every version it names is history,
// and history is supposed to name old versions.
var snippetDocs = []string{
	"README.md", "DEVELOPMENT.md", "CONTRIBUTING.md",
	"docs", "pkg", "examples",
}

// checkInstallSnippets fails when a document hard-codes a version in a
// command a reader would run.
//
// Every install path in this project is currently version-less — `@latest`,
// `brew install`, `make install` — or interpolates ${VERSION} from the one
// line that is gated. That is the state worth keeping: a hard-coded version
// in an install snippet is invisible when it goes stale, because the command
// still runs and still succeeds. It just installs the wrong release, and the
// checksum matches it, which is worse than a failure.
//
// pkg/VERIFY.md shipped exactly that defect at 0.0.29 and it took two
// releases to notice.
func checkInstallSnippets() []string {
	released, err := latestChangelogVersion("CHANGELOG.md")
	if err != nil {
		return []string{fmt.Sprintf("reading CHANGELOG.md: %v", err)}
	}

	var problems []string
	for _, root := range snippetDocs {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			// A root that does not exist, or a non-Markdown file, is
			// skipped rather than reported: these roots are optional and
			// this check is about what the documents say, not which of
			// them are present.
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			b, err := os.ReadFile(path) // #nosec G304 -- walking the fixed roots above
			if err != nil {
				return nil
			}
			for _, re := range []*regexp.Regexp{downloadPin, installPin} {
				for _, m := range re.FindAllSubmatch(b, -1) {
					if got := string(m[1]); got != released {
						problems = append(problems, fmt.Sprintf(
							"%s pins version %s in a command a reader would run; "+
								"newest release is %s — a stale pin still succeeds, "+
								"it just installs the wrong release",
							path, got, released))
					}
				}
			}
			return nil
		})
	}
	sort.Strings(problems)
	return problems
}

// heroTagVersion matches the documentation site's version badge.
var heroTagVersion = regexp.MustCompile(`(?m)^hero_tag:\s*"(v[0-9]+\.[0-9]+\.[0-9]+)"`)

// checkChangelogLinks verifies every release heading in CHANGELOG.md has
// a matching link reference, and that no reference points at a release
// that does not exist.
//
// Keep a Changelog puts the compare links in a block at the bottom, far
// from the entry somebody just wrote — so they are the part that drifts,
// silently, and only show up as a dead link in a rendered changelog
// months later. Version 0.0.28 shipped without one.
func checkChangelogLinks() []string {
	b, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		return []string{fmt.Sprintf("reading CHANGELOG.md: %v", err)}
	}

	headings := map[string]bool{}
	links := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if m := anyChangelogHeading.FindStringSubmatch(line); m != nil {
			headings[m[1]] = true
		}
		if m := changelogLinkRef.FindStringSubmatch(line); m != nil {
			links[m[1]] = true
		}
	}

	var problems []string
	for h := range headings {
		if !links[h] {
			problems = append(problems, fmt.Sprintf(
				"CHANGELOG.md has a [%s] heading with no link reference at the bottom of the file", h))
		}
	}
	for l := range links {
		if !headings[l] {
			problems = append(problems, fmt.Sprintf(
				"CHANGELOG.md has a [%s] link reference with no matching heading", l))
		}
	}
	sort.Strings(problems)
	return problems
}

// anyChangelogHeading matches a heading, released or not, so Unreleased
// is checked for a link too — it is the one people follow most.
var anyChangelogHeading = regexp.MustCompile(`^## \[([^\]]+)\]`)

// changelogLinkRef matches a Markdown link reference definition.
var changelogLinkRef = regexp.MustCompile(`^\[([^\]]+)\]:\s+http`)

// fixSBOMVersions rewrites the version cell of every SBOM.md row whose module
// is still a direct requirement but at a different version in go.mod, and
// returns a line describing each change.
//
// It repairs only versions of rows that already exist. A module in go.mod with
// no row, or a row for a module no longer required, is left alone: the first
// needs a Purpose and a Licence that only a person can supply, and the second
// is a removal that should be seen rather than done silently. checkSBOM still
// reports both.
//
// The file is rewritten only when something actually changed, so running this
// twice is a no-op rather than a no-op-shaped write.
func fixSBOMVersions() ([]string, error) {
	goMod, err := parseDirectRequires("go.mod")
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile("SBOM.md")
	if err != nil {
		return nil, fmt.Errorf("reading SBOM.md: %w", err)
	}

	var changed []string
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		m := sbomRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mod, have := m[1], m[2]
		want, required := goMod[mod]
		if !required || want == have {
			continue
		}
		// Replace only the version cell. The row is matched by the same regex
		// the checker uses, so the token being replaced is the one it read.
		lines[i] = strings.Replace(line, have, want, 1)
		changed = append(changed, fmt.Sprintf("SBOM.md: %s %s -> %s", mod, have, want))
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := os.WriteFile("SBOM.md", []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return nil, fmt.Errorf("writing SBOM.md: %w", err)
	}
	return changed, nil
}

// checkSBOM compares SBOM.md's table with go.mod's direct requirements.
func checkSBOM() []string {
	goMod, err := parseDirectRequires("go.mod")
	if err != nil {
		return []string{err.Error()}
	}
	sbom, err := parseSBOMTable("SBOM.md")
	if err != nil {
		return []string{err.Error()}
	}

	var problems []string
	for _, mod := range sortedKeys(goMod) {
		version, listed := sbom[mod]
		switch {
		case !listed:
			problems = append(problems, fmt.Sprintf("SBOM.md is missing %s %s", mod, goMod[mod]))
		case version != goMod[mod]:
			problems = append(problems, fmt.Sprintf("SBOM.md lists %s %s, go.mod requires %s", mod, version, goMod[mod]))
		}
	}
	for _, mod := range sortedKeys(sbom) {
		if _, required := goMod[mod]; !required {
			problems = append(problems, fmt.Sprintf("SBOM.md lists %s, which is not a direct requirement", mod))
		}
	}
	return problems
}

// checkServerManifest compares server.json's version with the newest
// CHANGELOG.md release, and with its own OCI image tag.
func checkServerManifest() []string {
	raw, err := os.ReadFile("server.json")
	if err != nil {
		return []string{fmt.Sprintf("reading server.json: %v", err)}
	}
	var manifest struct {
		Version  string `json:"version"`
		Packages []struct {
			Identifier string `json:"identifier"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return []string{fmt.Sprintf("parsing server.json: %v", err)}
	}

	released, err := latestChangelogVersion("CHANGELOG.md")
	if err != nil {
		return []string{err.Error()}
	}

	var problems []string
	if manifest.Version != released {
		problems = append(problems, fmt.Sprintf(
			"server.json version is %s, newest CHANGELOG.md release is %s", manifest.Version, released))
	}
	for _, pkg := range manifest.Packages {
		_, tag, found := strings.Cut(pkg.Identifier, ":")
		if !found {
			problems = append(problems, fmt.Sprintf("server.json package %q has no image tag", pkg.Identifier))
			continue
		}
		if tag != manifest.Version {
			problems = append(problems, fmt.Sprintf(
				"server.json image tag is %s, its version field is %s", tag, manifest.Version))
		}
	}
	return problems
}

// ospsRow matches one criterion row of the OSPS self-assessment table.
var ospsRow = regexp.MustCompile(`(?m)^\| ` + "`" + `OSPS-([A-Z]{2}-\d\d\.\d\d)` + "`" + ` \| \*\*([^*]+)\*\* \| (.*?) \| `)

// ospsLink matches a prefilled bestpractices.dev form link.
var ospsLink = regexp.MustCompile(`https://www\.bestpractices\.dev/en/projects/\d+/baseline-\d/edit\?[^)\s]+`)

// ospsDoc is the self-assessment this rule guards.
const ospsDoc = "docs/osps-baseline-fillable.md"

// checkOSPSConsistency verifies that every justification in the OSPS tables
// is byte-identical to the one carried in the prefilled form link for the
// same criterion.
//
// The tables are what a reviewer reads; the links are what actually reaches
// bestpractices.dev, where the answers become a public attestation under the
// maintainer's name. Nothing but discipline kept the two in step, and
// discipline is what failed when SECURITY.md was rewritten and five
// justifications kept citing text that no longer existed. A reader checking
// the table would have seen the corrected wording while the link still
// submitted the old.
func checkOSPSConsistency() []string {
	body, err := os.ReadFile(ospsDoc)
	if err != nil {
		return []string{fmt.Sprintf("reading %s: %v", ospsDoc, err)}
	}
	text := string(body)

	table := make(map[string]string)
	for _, m := range ospsRow.FindAllStringSubmatch(text, -1) {
		table["OSPS-"+m[1]] = m[3]
	}
	if len(table) == 0 {
		return []string{ospsDoc + ": found no criterion rows"}
	}

	var problems []string
	seen := make(map[string]bool)
	for _, raw := range ospsLink.FindAllString(text, -1) {
		parsed, err := url.Parse(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: unparseable prefilled link: %v", ospsDoc, err))
			continue
		}
		for key, values := range parsed.Query() {
			if !strings.HasSuffix(key, "_justification") || len(values) == 0 {
				continue
			}
			id := ospsCriterionID(strings.TrimSuffix(strings.TrimPrefix(key, "osps_"), "_justification"))
			seen[id] = true
			want, listed := table[id]
			switch {
			case !listed:
				problems = append(problems, fmt.Sprintf(
					"%s: prefilled link answers %s, which has no row in the tables", ospsDoc, id))
			case want != values[0]:
				problems = append(problems, fmt.Sprintf(
					"%s: %s reads differently in the table and in the prefilled link", ospsDoc, id))
			}
		}
	}
	for id := range table {
		if !seen[id] {
			problems = append(problems, fmt.Sprintf(
				"%s: %s is in the tables but no prefilled link submits it", ospsDoc, id))
		}
	}
	sort.Strings(problems)
	return problems
}

// ospsCriterionID turns a form field stem such as "ac_01_01" back into the
// canonical criterion identifier "OSPS-AC-01.01".
func ospsCriterionID(stem string) string {
	parts := strings.Split(stem, "_")
	if len(parts) != 3 {
		return "OSPS-" + strings.ToUpper(stem)
	}
	return fmt.Sprintf("OSPS-%s-%s.%s", strings.ToUpper(parts[0]), parts[1], parts[2])
}

// dockerFrom captures the image reference on the Dockerfile's FROM line.
var dockerFrom = regexp.MustCompile(`(?m)^FROM\s+(\S+)`)

// quotedImage finds a name:tag image reference inside prose, with or without
// a trailing digest.
var quotedImage = regexp.MustCompile(`\b([a-z][a-z0-9._-]*):(\d+\.\d+(?:\.\d+)?)(@sha256:[0-9a-f]+)?`)

// proseFiles are the documents that make claims about the build, and so can
// contradict it.
var proseFiles = []string{"README.md", "SECURITY.md", "SBOM.md",
	"docs/security-model.md", "docs/osps-baseline-fillable.md"}

// checkBaseImage reports prose that names a base image version other than the
// one the Dockerfile actually uses.
//
// docs/osps-baseline-fillable.md quoted "alpine:3.20@sha256:d9e853…" as an
// OSPS attestation. When Dependabot bumped the base to 3.24 the attestation
// became false, silently, in a file nobody edits during a dependency bump —
// the same shape of drift that put go-github v74 in SBOM.md for six releases.
// The prose no longer names a version; this makes sure it stays that way, or
// that any version it does name is the right one.
func checkBaseImage() []string {
	body, err := os.ReadFile("Dockerfile")
	if err != nil {
		return []string{fmt.Sprintf("reading Dockerfile: %v", err)}
	}
	m := dockerFrom.FindSubmatch(body)
	if m == nil {
		return []string{"Dockerfile: no FROM line found"}
	}
	actual := string(m[1])
	name, tag, _ := strings.Cut(strings.SplitN(actual, "@", 2)[0], ":")

	var problems []string
	for _, file := range proseFiles {
		text, err := os.ReadFile(file) // #nosec G304 -- fixed list of repository documents
		if err != nil {
			problems = append(problems, fmt.Sprintf("reading %s: %v", file, err))
			continue
		}
		seen := map[string]bool{}
		for _, hit := range quotedImage.FindAllStringSubmatch(string(text), -1) {
			if hit[1] != name || hit[2] == tag || seen[hit[0]] {
				continue
			}
			seen[hit[0]] = true
			problems = append(problems, fmt.Sprintf(
				"%s names %s:%s, the Dockerfile builds on %s", file, hit[1], hit[2], actual))
		}
	}
	return problems
}

// parseDirectRequires returns the module-to-version map of every
// non-indirect requirement in a go.mod file.
func parseDirectRequires(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	requires := make(map[string]string)
	inBlock := false
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
		case inBlock && strings.HasPrefix(line, ")"):
			inBlock = false
		case inBlock && !strings.Contains(line, "// indirect"):
			if m := directRequire.FindStringSubmatch(line); m != nil {
				requires[m[1]] = m[2]
			}
		}
	}
	if len(requires) == 0 {
		return nil, fmt.Errorf("%s: found no direct requirements", path)
	}
	return requires, nil
}

// parseSBOMTable returns the module-to-version map of SBOM.md's dependency
// table. Rows whose version cell is absent are documentation, not entries.
func parseSBOMTable(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	listed := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		if m := sbomRow.FindStringSubmatch(line); m != nil {
			listed[m[1]] = m[2]
		}
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("%s: found no dependency rows", path)
	}
	return listed, nil
}

// latestChangelogVersion returns the newest released version heading in a
// Keep a Changelog file, skipping the Unreleased section.
func latestChangelogVersion(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if m := changelogHeading.FindStringSubmatch(line); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("%s: found no released version heading", path)
}

// sortedKeys returns a map's keys in a stable order so output is
// deterministic across runs.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
