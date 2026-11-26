// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"fmt"
	"strings"
)

// tierNames are the report headings for each tier.
var tierNames = map[Tier]string{
	TierFast:       "Tier 1, fold as is",
	TierRemediate:  "Tier 2, fix first",
	TierQuarantine: "Tier 3, needs a person",
	TierClose:      "Tier 4, close",
}

// Report renders a plan as Markdown: the count per tier, the fleet
// batches with their canary, and one table row per pull request.
func Report(p Plan) string {
	var b strings.Builder
	b.WriteString("# Dependabot triage\n\n")
	writeCounts(&b, p)
	writeFleets(&b, p.Fleets)
	writeTable(&b, p.Decisions)
	return b.String()
}

// writeCounts writes how many pull requests fall in each tier.
func writeCounts(b *strings.Builder, p Plan) {
	counts := map[Tier]int{}
	for _, d := range p.Decisions {
		counts[d.Tier]++
	}
	fmt.Fprintf(b, "%d open pull request(s).\n\n", len(p.Decisions))
	for _, t := range []Tier{TierFast, TierRemediate, TierQuarantine, TierClose} {
		fmt.Fprintf(b, "* %s: %d\n", tierNames[t], counts[t])
	}
	b.WriteString("\n")
}

// writeFleets writes the bumps shared across repositories.
func writeFleets(b *strings.Builder, fleets []Fleet) {
	b.WriteString("## Fleet batches\n\n")
	if len(fleets) == 0 {
		b.WriteString("None: no bump is open in " + fmt.Sprint(FleetMin) + " or more repositories.\n\n")
		return
	}
	for _, f := range fleets {
		fmt.Fprintf(b, "* `%s` to %s in %d repositories; canary %s, then %s\n",
			f.Package, f.To, len(f.Repos), f.Repos[0], strings.Join(f.Repos[1:], ", "))
	}
	b.WriteString("\n")
}

// writeTable writes one row per pull request.
func writeTable(b *strings.Builder, ds []Decision) {
	b.WriteString("## Pull requests\n\n| Repo | PR | Update | Tier | Reason | Action |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, d := range ds {
		fmt.Fprintf(b, "| %s | [#%d](%s) | %s | %d | %s | %s |\n",
			d.PR.Repo, d.PR.Number, d.PR.URL, cell(describeBump(d)), d.Tier, cell(d.Reason), cell(d.Action))
	}
}

// describeBump names what a pull request changes, for a table cell.
func describeBump(d Decision) string {
	switch {
	case d.Bump.IsGroup():
		return fmt.Sprintf("%s group, %d update(s)", d.Bump.Group, d.Bump.Updates)
	case d.Bump.Package == "":
		return d.PR.Title
	default:
		return fmt.Sprintf("%s %s to %s (%s)", d.Bump.Package, d.Bump.From, d.Bump.To, d.Jump)
	}
}

// cell escapes the one character that would break a Markdown table row.
func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
