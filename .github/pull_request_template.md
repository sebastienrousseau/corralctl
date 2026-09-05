<!--
Thanks for contributing to corralctl.

Before opening: branch from `main` and target `main`. A pull request based on
another branch runs no CI here (every workflow filters on
`branches: [main]`) and GitHub records it as merged with an empty commit range
once the branch it was stacked on lands. The `PR Base` check enforces this.
-->

## What this changes

<!-- One or two sentences. What is different after this merges? -->

## Why

<!-- The problem, not the patch. If it fixes an issue, link it: Fixes #123 -->

## How it was verified

<!--
Not "it builds". What did you run, and what did you observe? Delete the lines
that do not apply and say what you actually did for the rest.
-->

- [ ] `make test` passes
- [ ] `go test -race -shuffle=on ./...` passes
- [ ] New or changed behaviour is covered by a test that fails without the change
- [ ] `make sbom-check` passes (required if `go.mod` or `server.json` changed)
- [ ] `CHANGELOG.md` has an entry under the right heading

## Risk

<!--
What could this break, and what would that look like? "Nothing" is a valid
answer for a docs change; it is rarely the right answer for anything touching
clone, sync, delete, or path resolution.
-->

---

- [ ] Commits are signed and carry a DCO `Signed-off-by` trailer (`git commit -s -S`)
