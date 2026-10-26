// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"fmt"
	"net/url"
	"strings"
)

// publicHosts are the names of github.com itself. A --forge-url naming one
// of them is the public service, not an Enterprise Server: GHES's REST API
// lives under /api/v3 on the instance, github.com's on api.github.com, and
// treating github.com as an instance would send every request to a path
// that does not exist.
var publicHosts = map[string]bool{
	"github.com":     true,
	"www.github.com": true,
	"api.github.com": true,
}

// enterpriseBase reads a --forge-url for GitHub.
//
// It returns the instance root ("https://ghe.corp") and its host
// ("ghe.corp") for a GitHub Enterprise Server, or two empty strings for
// github.com or no URL at all. A URL that cannot name an instance is an
// error rather than a fall back to github.com: listing a different host
// than the one the user named, under the same owner, is exactly how a run
// clones the wrong repositories.
//
// The path is dropped. A forge URL names the instance, and an
// owner-looking path ("https://ghe.corp/acme") would otherwise become
// part of every API path.
func enterpriseBase(raw string) (base, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", fmt.Errorf("GitHub forge URL %q must be an http or https URL naming a host", raw)
	}
	if publicHosts[strings.ToLower(u.Hostname())] {
		return "", "", nil
	}
	return u.Scheme + "://" + u.Host, u.Host, nil
}
