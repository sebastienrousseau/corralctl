# Contributing

corralctl is a compiled Go application with a Bubble Tea terminal user interface that clones and organises repositories from GitHub, GitLab, Gitea, Forgejo,
Codeberg and Bitbucket by visibility and language. Contributions are welcome.

## Getting Started

1. Fork and clone the repository.
2. Install development dependencies:
   - **Go 1.26+**
   - **Make**
   - **Git**

3. Set up the project hooks (optional but recommended):

   ```bash
   git config core.hooksPath .githooks
   ```

4. Create a branch **from `main`**:

   ```bash
   git checkout main && git pull
   git checkout -b feat/my-change
   ```

   Branch from `main` and open the pull request against `main` — never
   against another branch. Every workflow in this repository filters on
   `pull_request: branches: [main]`, so a PR aimed elsewhere runs no CI
   at all, and GitHub records it as merged with an empty commit range
   once the branch it was stacked on lands. A `PR Base` check enforces
   this. If your change depends on work that is still in review, wait
   for it to merge or fold the two into one pull request.
5. Make changes.
6. Verify everything passes:

   ```bash
   make format
   make test
   make build
   ```

7. Commit, push, and open a pull request.

## Commits

**Sign your commits cryptographically and add a DCO sign-off trailer.**
Both are required — signing proves who authored the commit; the DCO
sign-off asserts you have the right to contribute the change under the
project licence.

### Cryptographic signing

```bash
# Enable signing once (SSH or GPG both accepted):
git config --global commit.gpgsign true
```

Need a signing key? Follow
[GitHub's guide to signing commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits).

### Developer Certificate of Origin (DCO)

Every commit must include a `Signed-off-by:` trailer matching the
commit author. The full text of the DCO is at
<https://developercertificate.org>; adding the trailer certifies that
you agree to it. Enforced by the DCO workflow on every PR.

```bash
# Sign off a single commit:
git commit -s -m "your message"

# Amend the most recent commit to add a sign-off you forgot:
git commit --amend --signoff

# Retroactively sign off a range of commits before your PR base:
git rebase --signoff <base-sha>
```

Configure `git commit -s` as your default by aliasing it locally
(`git config --global alias.ci 'commit -s'`) or by using
`git config --global format.signoff true` if your git version supports it.

### Commit messages

Use imperative commit messages: "Add dry-run flag", not "Added dry-run flag."

## Pull Request Checklist

- [ ] `make test` passes
- [ ] `make build` succeeds
- [ ] README updated if behaviour changed
- [ ] All commits are signed (`git log --show-signature`)
- [ ] All commits carry a DCO sign-off (`git commit -s`)

## Code Style

- Use standard Go formatting (`gofmt -w .`).
- Ensure all exported functions and types are documented.
- Follow idiomatic Go guidelines.
