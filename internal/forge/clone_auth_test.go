// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import "testing"

func TestCloneOrigin(t *testing.T) {
	cases := []struct{ forge, base, want string }{
		{"github", "", "https://github.com/"},
		{"github", "https://github.com/acme", "https://github.com/"},
		{"github", "https://api.github.com", "https://github.com/"},
		{"github", "https://ghe.corp", "https://ghe.corp/"},
		{"gitlab", "", "https://gitlab.com/"},
		{"gitlab", "https://gitlab.corp:8443/gitlab", "https://gitlab.corp:8443/"},
		{"bitbucket", "", "https://bitbucket.org/"},
		{"bitbucket", "https://api.bitbucket.org", "https://bitbucket.org/"},
		{"bitbucket", "https://www.bitbucket.org", "https://bitbucket.org/"},
		{"codeberg", "", "https://codeberg.org/"},
		{"gitea", "", ""},
		{"gitea", "https://git.example", "https://git.example/"},
		// Clear text and unusable URLs are never sent a token.
		{"gitea", "http://git.example", ""},
		{"gitea", "https://", ""},
		{"gitea", "://bad", ""},
	}
	for _, c := range cases {
		f, err := Get(c.forge)
		if err != nil {
			t.Fatal(err)
		}
		if got := CloneOrigin(f, c.base); got != c.want {
			t.Errorf("CloneOrigin(%s, %q) = %q, want %q", c.forge, c.base, got, c.want)
		}
	}
}

func TestCloneAuth(t *testing.T) {
	want := map[string]string{
		"github": "x-access-token", "gitlab": "oauth2", "bitbucket": "x-token-auth",
		"gitea": cloneLogin, "forgejo": cloneLogin, "codeberg": cloneLogin,
	}
	for name, user := range want {
		f, _ := Get(name)
		if u, s := CloneAuth(f, "tok"); u != user || s != "tok" {
			t.Errorf("CloneAuth(%s) = %q, %q; want %q, tok", name, u, s, user)
		}
	}
	if u, s := CloneAuth(listOnly{}, "tok"); u != "" || s != "" {
		t.Errorf("a forge that cannot push has no clone pair, got %q, %q", u, s)
	}
}
