// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/syncguard"
)

// prepareWorkspace performs the one-off layout work a classic-layout run
// needs before any repository is processed: creating the collection folders,
// migrating clones left by older layouts, and repairing directory case.
func prepareWorkspace(opts RunOptions, repos []github.Repo) error {
	if !usesClassicLayout(opts) || opts.DryRun {
		return nil
	}
	if err := ensureAppleCollections(opts.BaseDir); err != nil {
		return fmt.Errorf("failed creating repository collections: %w", err)
	}
	migrateLegacy(opts.BaseDir, repos)
	normalizeLayoutDirCase(opts.BaseDir, repos)
	return nil
}

func ensureAppleCollections(baseDir string) error {
	for _, collection := range []string{"Public", "Private", "Work", "Forks"} {
		if err := mkdirAll(filepath.Join(baseDir, collection), 0o750); err != nil {
			return err
		}
	}
	return nil
}

// repoRemoteIdentity is syncguard.RemoteIdentity: the canonical identity
// of a listed repository, for comparison against a local clone's remote.
var repoRemoteIdentity = syncguard.RemoteIdentity

func firstPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func discoverExistingRepos(baseDir string) map[string][]string {
	found := make(map[string][]string)
	_ = walkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() || path == baseDir {
			return nil
		}
		if git.IsRepository(path) {
			if remote, err := gitRemoteOrigin(path); err == nil {
				if identity := git.CanonicalRemote(remote); identity != "" {
					found[identity] = append(found[identity], path)
				}
			}
			return filepath.SkipDir
		}
		if skipDiscoveryDirectory(d.Name()) {
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func skipDiscoveryDirectory(name string) bool {
	switch name {
	case ".git", ".cache", ".next", ".venv", "DerivedData", "Pods", "build", "dist", "node_modules", "target", "vendor", "venv":
		return true
	default:
		return false
	}
}

func migrateLegacy(baseDir string, repos []github.Repo) {
	for _, repo := range repos {
		legacyDir := filepath.Join(baseDir, normalizeLanguage(repo.Language), repo.Name)
		targetDir := filepath.Join(baseDir, repositoryCollection(repo), repositoryBucket(repo), repo.Name)

		if info, err := os.Stat(legacyDir); err == nil && info.IsDir() {
			// Only relocate something that is demonstrably this repository's
			// clone. The name match alone is not evidence: a plain directory at
			// ~/Code/go/tools that merely shares a name with a repo called
			// "tools" was moved with no prompt and — because the dry-run
			// preview path requires git.IsRepository — no preview either.
			if ok, why := migratableClone(legacyDir, repo); !ok {
				diag.Warnf("not migrating %s: %s", legacyDir, why)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(targetDir), 0o750); err != nil {
				diag.Warnf("failed creating target parent for migration %s: %v", targetDir, err)
				continue
			}
			if err := os.Rename(legacyDir, targetDir); err != nil {
				diag.Warnf("failed migrating %s to %s: %v", legacyDir, targetDir, err)
			}
		}
	}
}

// migratableClone reports whether dir may be relocated as repo's clone, and if
// not, why. It requires two independent pieces of evidence: dir is a git
// repository, and its origin remote matches the repository being migrated.
func migratableClone(dir string, repo github.Repo) (bool, string) {
	if !git.IsRepository(dir) {
		return false, "not a git repository (name matches " + repo.Name + ", but nothing else does)"
	}
	origin, err := gitRemoteOrigin(dir)
	if err != nil {
		return false, "cannot read its origin remote: " + err.Error()
	}
	want := repoRemoteIdentity(repo)
	got := git.CanonicalRemote(origin)
	if want == "" || got == "" {
		return false, "origin remote could not be compared"
	}
	if got != want {
		return false, "origin is " + got + ", not " + want
	}
	return true, ""
}

// normalizeLayoutDirCase applies Finder-facing capitalization to collection
// and ecosystem directories. APFS/HFS+ need an intermediate name for a
// case-only rename.
func normalizeLayoutDirCase(baseDir string, repos []github.Repo) {
	buckets := make(map[string]string, len(repos))
	for _, r := range repos {
		bucket := repositoryBucket(r)
		buckets[strings.ToLower(bucket)] = bucket
	}
	if len(buckets) == 0 {
		return
	}
	visEntries, err := readDir(baseDir)
	if err != nil {
		return
	}
	for _, ve := range visEntries {
		if !ve.IsDir() {
			continue
		}
		collection := canonicalCollectionName(ve.Name())
		if collection == "" {
			continue
		}
		visDir := filepath.Join(baseDir, ve.Name())
		if ve.Name() != collection && strings.EqualFold(ve.Name(), collection) {
			canonicalPath := filepath.Join(baseDir, collection)
			if renameCaseOnly(visDir, canonicalPath) {
				visDir = canonicalPath
			}
		}
		langEntries, err := readDir(visDir)
		if err != nil {
			continue
		}
		for _, le := range langEntries {
			if !le.IsDir() {
				continue
			}
			name := le.Name()
			canonical, ok := buckets[strings.ToLower(name)]
			if !ok || name == canonical {
				continue
			}
			src := filepath.Join(visDir, name)
			dst := filepath.Join(visDir, canonical)
			renameCaseOnly(src, dst)
		}
	}
}

func renameCaseOnly(src, dst string) bool {
	tmp := dst + ".corral-rename-tmp"
	if err := renamePath(src, tmp); err != nil {
		diag.Warnf("failed normalizing case for %s: %v", src, err)
		return false
	}
	if err := renamePath(tmp, dst); err != nil {
		diag.Warnf("failed normalizing case for %s -> %s: %v", tmp, dst, err)
		_ = renamePath(tmp, src)
		return false
	}
	return true
}

// cleanupEmptyFolders removes the now-empty legacy top-level language
// directories left behind by migrateLegacy. It only targets directories whose
// names match a repository language, and os.Remove deletes a directory only
// when it is empty, so unrelated entries under baseDir (e.g. .claude, other
// projects) are never touched.
func cleanupEmptyFolders(baseDir string, repos []github.Repo) {
	seenRoots := make(map[string]struct{})
	seenBuckets := make(map[string]struct{})
	for _, repo := range repos {
		lang := normalizeLanguage(repo.Language)
		if _, ok := seenRoots[lang]; !ok {
			seenRoots[lang] = struct{}{}
			_ = os.Remove(filepath.Join(baseDir, lang))
		}
		legacyBucket := filepath.Join(repositoryCollection(repo), lang)
		if _, ok := seenBuckets[legacyBucket]; !ok {
			seenBuckets[legacyBucket] = struct{}{}
			_ = os.Remove(filepath.Join(baseDir, legacyBucket))
		}
	}
}

// repoNameFromURL extracts the repository name from a git remote URL, stripping
// any trailing ".git" suffix. It returns an empty string when no segment exists.
func repoNameFromURL(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		return url[i+1:]
	}
	return url
}

func findOrphans(owner, baseDir string, repos []github.Repo) []string {
	return findOrphansOn(owner, baseDir, repos, fetchForgeName, fetchForgeURL)
}

// findOrphansOn is findOrphans with the forge named explicitly.
//
// The host is derived rather than assumed. It used to be the literal
// "github.com", which silently made orphan detection a no-op for every
// other forge: a Codeberg clone never matched the prefix, so it was
// skipped rather than compared.
func findOrphansOn(owner, baseDir string, repos []github.Repo, forgeName, forgeURL string) []string {
	repoMap := make(map[string]bool)
	identities := make(map[string]bool, len(repos))
	cloneURLs := make([]string, 0, len(repos))
	for _, r := range repos {
		repoMap[r.Name] = true
		if id := repoRemoteIdentity(r); id != "" {
			identities[id] = true
		}
		if r.CloneURL != "" {
			cloneURLs = append(cloneURLs, r.CloneURL)
		}
	}
	f, err := forge.Resolve(forgeName, forgeURL)
	if err != nil {
		f = nil
	}
	prefixes := forge.OwnerPrefixes(f, owner, forgeURL, cloneURLs)
	if len(prefixes) == 0 {
		return nil
	}

	var orphans []string
	_ = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // deliberate: best-effort orphan detection
		}
		if d.IsDir() && path != baseDir && git.IsRepository(path) {
			repoDir := path
			url, err := gitRemoteOrigin(repoDir)
			if err != nil {
				return filepath.SkipDir
			}
			identity := git.CanonicalRemote(url)
			if !forge.MatchesOwner(identity, prefixes) {
				return filepath.SkipDir
			}
			if !identities[identity] && !repoMap[filepath.Base(repoDir)] && !repoMap[repoNameFromURL(url)] {
				orphans = append(orphans, repoDir)
			}
			return filepath.SkipDir
		}
		return nil
	})

	return orphans
}

func emitOrphans(owner string, orphans []string) {
	fmt.Println("\n--- Orphan Detection ---")
	for _, orphan := range orphans {
		fmt.Printf("Orphan found: %s\n", orphan)
	}
	if len(orphans) == 0 {
		fmt.Printf("No orphaned repositories found for %s.\n", owner)
	}
}

func detectOrphans(owner, baseDir string, repos []github.Repo) {
	emitOrphans(owner, findOrphans(owner, baseDir, repos))
}

// emitOrphanReport finds and reports local clones that no longer exist
// upstream, returning the paths so the aggregate JSON document can carry
// them. Orphan detection is skipped on cancellation: the local tree may be
// mid-clone and the report would be misleading.
func emitOrphanReport(opts RunOptions, repos []github.Repo, outcome *runOutcome, encoder *json.Encoder) []string {
	if !opts.Orphans || outcome.summary.Canceled {
		return nil
	}
	orphanPaths := findOrphans(opts.Owner, opts.BaseDir, repos)
	switch opts.Output {
	case OutputText:
		emitOrphans(opts.Owner, orphanPaths)
	case OutputNDJSON:
		for _, orphan := range orphanPaths {
			if err := encoder.Encode(RepoResult{Action: "ORPHAN", Target: orphan, Message: "local repository is absent upstream"}); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to encode orphan result: %v\n", err)
				if outcome.outputErr == nil {
					outcome.outputErr = err
				}
			}
		}
	}
	return orphanPaths
}
