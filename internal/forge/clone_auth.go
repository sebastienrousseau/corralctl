// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"net/url"
	"strings"
)

// cloneLogin is the username sent with a token to a forge whose PushAuth
// wants the account's own login, which a clone does not know without an
// extra request. The Gitea family ignores it: with a non-empty password,
// Gitea's basic-auth handler (services/auth/basic.go, parseAuthBasic)
// looks the password up as an access token and takes the user from the
// token, never comparing it with the username. Forgejo kept that code.
const cloneLogin = "x-access-token"

// CloneOrigin is the HTTPS origin ("https://gitlab.com/") a clone
// credential for f may be sent to, or "" when there is none.
//
// baseURL is the instance the listing came from. When it names one of the
// forge's public hosts, or their api./www. spellings, the origin is the
// forge's canonical clone host: Bitbucket lists from api.bitbucket.org
// and clones from bitbucket.org. Any other host is a self-hosted instance
// and is its own origin. A plain-http instance gets no origin, so a token
// is never sent in clear text.
func CloneOrigin(f Forge, baseURL string) string {
	hosts := f.Hosts()
	if strings.TrimSpace(baseURL) == "" {
		if len(hosts) == 0 {
			return ""
		}
		return "https://" + hosts[0] + "/"
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	if publicHost(hosts, u.Hostname()) {
		return "https://" + hosts[0] + "/"
	}
	return "https://" + u.Host + "/"
}

// publicHost reports whether host is one of hosts or its api./www. form.
func publicHost(hosts []string, host string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		if host == h || host == "api."+h || host == "www."+h {
			return true
		}
	}
	return false
}

// CloneAuth is the HTTP basic-auth pair git presents to f when cloning
// with token: the same pair a push presents, from the forge's PushAuth,
// so each forge's username convention lives in one place.
func CloneAuth(f Forge, token string) (username, secret string) {
	t, ok := AsTarget(f)
	if !ok {
		return "", ""
	}
	return t.PushAuth(TargetOptions{Token: token, Login: cloneLogin})
}
