// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"context"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Seams for the origin guard: what git resolves origin to, and what host
// ssh connects to for a name.
var (
	resolveOrigin = resolvedOrigin
	sshHostname   = sshConfigHostname
	sshBinary     = "ssh"
)

// forgeHostAliases maps a forge's alternative SSH host to its canonical
// one. A clone over GitHub's port-443 endpoint pushes to github.com all
// the same.
var forgeHostAliases = map[string]string{
	"ssh.github.com":       "github.com",
	"altssh.gitlab.com":    "gitlab.com",
	"altssh.bitbucket.org": "bitbucket.org",
}

// sshHostPattern is the host shape passed to `ssh -G`. A leading letter
// or digit keeps a crafted origin from reaching ssh as an option.
var sshHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// resolvedOrigin is origin's URL after git's insteadOf rewrites.
func resolvedOrigin(ctx context.Context, path string) (string, error) {
	return git.ResolvedRemoteURL(ctx, path, "origin")
}

// sshConfigHostname is the host ssh connects to for host, after the
// user's ssh config: `Host github-work` with `HostName github.com` is
// github.com. Any failure leaves host as it is.
func sshConfigHostname(ctx context.Context, host string) string {
	if !sshHostPattern.MatchString(host) {
		return host
	}
	out, err := exec.CommandContext(ctx, sshBinary, "-G", "--", host).Output() // #nosec G204 -- fixed binary, host validated above
	if err != nil {
		return host
	}
	for _, line := range strings.Split(string(out), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok && key == "hostname" {
			return strings.ToLower(value)
		}
	}
	return host
}

// canonicalHost folds case and a forge's alternative SSH host.
func canonicalHost(host string) string {
	host = strings.ToLower(host)
	if canonical, ok := forgeHostAliases[host]; ok {
		return canonical
	}
	return host
}

// isSSHRemote reports whether git reaches remote over ssh, where the
// user's ssh config can rename the host.
func isSSHRemote(remote string) bool {
	lower := strings.ToLower(strings.TrimSpace(remote))
	if strings.HasPrefix(lower, "ssh://") || strings.HasPrefix(lower, "git+ssh://") {
		return true
	}
	return !strings.Contains(lower, "://")
}

// originHosts is every host the repository's origin may reach: as
// written, as git rewrites it, and as ssh resolves it. The guard compares
// against all of them, so no spelling of the destination slips through.
func originHosts(ctx context.Context, path string) []string {
	var remotes []string
	if raw, err := originURL(path); err == nil {
		remotes = append(remotes, raw)
	}
	if resolved, err := resolveOrigin(ctx, path); err == nil {
		remotes = append(remotes, resolved)
	}
	var hosts []string
	for _, remote := range remotes {
		host := hostOf(remote)
		if host == "" {
			continue
		}
		hosts = append(hosts, canonicalHost(host))
		if isSSHRemote(remote) {
			hosts = append(hosts, canonicalHost(sshHostname(ctx, host)))
		}
	}
	return hosts
}

// originOn reports whether any origin host is the destination's.
func originOn(hosts []string, destHost string) bool {
	want := canonicalHost(destHost)
	for _, host := range hosts {
		if host == want {
			return true
		}
	}
	return false
}
