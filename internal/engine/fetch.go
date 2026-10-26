// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

// fetchRepos is the seam the engine lists repositories through. The forge
// is passed on every call, from the run's own options, rather than read
// from package state: two runs in one process with different forges must
// not see each other's selection.
var fetchRepos = FetchRepos

// installTokenProvider lets git authenticate HTTPS clones and pulls of
// private repositories with the credential the run's forge listing uses:
// GitHub's (or an Enterprise Server's) through its own ladder, every other
// forge's from its environment variable, presented with that forge's
// username convention and scoped to that forge's origin. The token is
// resolved at most once, and only if git actually asks for it.
//
// It used to install only the GitHub token, scoped to github.com, so a
// private repository on GitLab, the Gitea family or Bitbucket was cloned
// anonymously and failed unless a git credential helper happened to be
// configured.
func installTokenProvider(ctx context.Context, opts RunOptions) {
	f, err := forge.Resolve(opts.Forge, opts.ForgeURL)
	if err != nil {
		git.CloneCredential = nil
		return
	}
	origin := forge.CloneOrigin(f, opts.ForgeURL)
	var once sync.Once
	var cred *git.PushCredential
	git.CloneCredential = func() (string, *git.PushCredential) {
		once.Do(func() {
			if origin == "" {
				return
			}
			if tok := forge.Token(ctx, f.Name(), opts.Fetch.AuthMode, opts.ForgeURL); tok != "" {
				user, secret := forge.CloneAuth(f, tok)
				cred = &git.PushCredential{Username: user, Secret: secret}
			}
		})
		return origin, cred
	}
}

// resolveRepos produces the repository set for this run, either from the
// interactive selector or from a direct API fetch. The bool reports whether
// the run should proceed: it is false when the user dismissed the selector
// or selected nothing, which is a clean exit rather than a failure.
func resolveRepos(ctx context.Context, opts RunOptions, isTTY bool) ([]github.Repo, bool, error) {
	if !opts.Interactive {
		announceFetch(opts, isTTY)
		repos, err := fetchRepos(ctx, opts.Owner, opts.Fetch, opts.Forge, opts.ForgeURL)
		if err != nil {
			return nil, false, err
		}
		warnOnLimit(opts, len(repos))
		return repos, true, nil
	}

	tui.Version = opts.Version
	repos, ok, err := runSelector(ctx, opts.Owner, opts.Fetch, func() ([]github.Repo, error) {
		return fetchRepos(ctx, opts.Owner, opts.Fetch, opts.Forge, opts.ForgeURL)
	})
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if len(repos) == 0 {
		if opts.Output == OutputText {
			fmt.Println("No repositories selected.")
		}
		return nil, false, nil
	}
	if opts.Output == OutputText && isTTY {
		fmt.Print("\033[2J\033[H")
	}
	warnOnLimit(opts, len(repos))
	return repos, true, nil
}

// announceFetch prints the banner and progress line that precede a
// non-interactive fetch, honouring the selected output format and whether
// stdout is a terminal.
func announceFetch(opts RunOptions, isTTY bool) {
	if opts.Output != OutputText {
		return
	}
	if !isTTY {
		log.Println(fetchAnnouncement(opts))
		return
	}
	if os.Getenv("CORRAL_SHOW_LOGO") != "0" {
		fmt.Print(tui.GetStyledLogo())
		fmt.Print(lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("   ⧇ Organising Repositories") + "\n")
		fmt.Print(lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("   "+strings.Repeat("─", 58)) + "\n\n")
	}
	fmt.Println(fetchAnnouncement(opts))
}

// fetchAnnouncement names the host being listed from.
//
// It said "GitHub" unconditionally, which became a lie the moment
// cloning worked against five forges: somebody running `--forge codeberg`
// was told their repositories were coming from GitHub. A message that
// names the wrong service is worse than no message, because it is the
// only confirmation a user gets that the flag took effect.
func fetchAnnouncement(opts RunOptions) string {
	name := "GitHub"
	if f, err := forge.Resolve(opts.Forge, opts.ForgeURL); err == nil {
		name = f.Name()
	}
	if opts.ForgeURL != "" {
		// The instance matters more than the software for a self-hosted
		// deployment: two of them run the same forge.
		return fmt.Sprintf("Fetching repositories from %s (%s)...", name, opts.ForgeURL)
	}
	return fmt.Sprintf("Fetching repositories from %s...", name)
}

// warnOnLimit reports a fetch that returned exactly --limit repositories,
// which means the listing was probably truncated.
func warnOnLimit(opts RunOptions, fetched int) {
	if opts.Fetch.Limit > 0 && fetched == opts.Fetch.Limit && opts.Output == OutputText {
		fmt.Printf("WARNING: Fetched exactly %d repositories. There may be more.\n", opts.Fetch.Limit)
	}
}

// FetchRepos lists an owner's repositories from the named forge.
//
// Exported because prune needs it. prune used to call the GitHub client
// directly, so `--forge codeberg` was accepted and then ignored: it
// compared Codeberg clones against a GitHub listing. Everything that
// decides what is missing upstream has to come through one path, or the
// flag lies.
//
// It is also the one place a forge.Repo becomes the engine's own
// repository shape. That shape still lives in internal/github, which is
// now a misnomer: it is the engine's type, and GitHub is one of five
// things that can produce it. Renaming it across the engine, the TUI and
// the command layer is churn with no behaviour attached, so it is left for
// a change that has a reason of its own. The conversion below is where the
// seam actually is.
func FetchRepos(ctx context.Context, owner string, opts github.FetchOptions, forgeName, forgeURL string) ([]github.Repo, error) {
	f, err := forge.Resolve(forgeName, forgeURL)
	if err != nil {
		return nil, err
	}
	// GitHub keeps its existing path entirely: the adapter delegates to
	// the same client, so its auth ladder, rate-limit handling and search
	// pagination are untouched by this indirection.
	repos, err := f.List(ctx, owner, forge.Options{
		Limit:           opts.Limit,
		Visibility:      opts.Visibility,
		IncludeForks:    opts.IncludeForks,
		IncludeArchived: opts.IncludeArchived,
		Token:           forge.EnvToken(f.Name()),
		BaseURL:         forgeURL,
		RequestTimeout:  opts.RequestTimeout,
		TotalTimeout:    opts.TotalTimeout,
	})
	if err != nil {
		return nil, err
	}

	out := make([]github.Repo, 0, len(repos))
	for _, r := range repos {
		out = append(out, github.Repo{
			ID:            r.ID,
			Owner:         r.Owner,
			FullName:      r.FullName,
			Name:          r.Name,
			Language:      r.Language,
			Visibility:    r.Visibility,
			DefaultBranch: r.DefaultBranch,
			CloneURL:      r.CloneURL,
			SSHURL:        r.SSHURL,
			Fork:          r.Fork,
			Archived:      r.Archived,
			PushedAt:      r.PushedAt,
			Stars:         r.Stars,
			IsTemplate:    r.IsTemplate,
			IsMirror:      r.IsMirror,
		})
	}
	return out, nil
}
