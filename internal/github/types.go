// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"fmt"
	"net/http"
	"time"
)

// Repo represents a simplified repository structure returned by the GitHub API.
type Repo struct {
	// ID is GitHub's immutable repository identifier.
	ID int64
	// Owner is the repository owner's login.
	Owner string
	// FullName is the canonical owner/name identity.
	FullName string
	// Name is the repository name (without the owner prefix).
	Name string
	// Language is the primary programming language, or "Other" when unknown.
	Language string
	// Visibility is the normalized visibility, either "Public" or "Private".
	Visibility string
	// DefaultBranch is the repository's default branch name.
	DefaultBranch string
	// CloneURL is the HTTPS clone URL for the repository.
	CloneURL string
	// SSHURL is the SSH clone URL for the repository.
	SSHURL string
	// Fork reports whether the repository is a fork.
	Fork bool
	// Archived reports whether the repository is archived.
	Archived bool
	// PushedAt is the timestamp of the last push to any branch. The engine
	// compares this against the cached value in <repo>/.corral-state.json to
	// skip a `git pull` when nothing has changed upstream.
	PushedAt time.Time
	// Stars reports the stargazers count for the repository.
	Stars int
	// IsTemplate reports whether the repository is a template.
	IsTemplate bool
	// IsMirror reports whether the repository is a mirror.
	IsMirror bool
	// CanBeSponsored reports whether the repository has sponsorships enabled.
	CanBeSponsored bool
}

// AuthMode controls how GitHub API credentials are resolved.
type AuthMode string

const (
	// AuthModeAuto resolves credentials from the environment first, then the gh CLI.
	AuthModeAuto AuthMode = "auto"
	// AuthModeToken resolves credentials only from environment variables.
	AuthModeToken AuthMode = "token"
	// AuthModeGH resolves credentials only via the gh CLI (`gh auth token`).
	AuthModeGH AuthMode = "gh"
)

// FetchOptions configures repository fetch behavior.
type FetchOptions struct {
	// Limit caps the number of repositories returned; 0 means no limit.
	Limit int
	// Visibility filters repositories by visibility ("all", "public", or "private").
	Visibility string
	// IncludeForks includes forked repositories when true.
	IncludeForks bool
	// IncludeArchived includes archived repositories when true.
	IncludeArchived bool
	// IncludeLanguages, when non-empty, keeps only repositories matching these languages.
	IncludeLanguages []string
	// ExcludeLanguages removes repositories matching these languages.
	ExcludeLanguages []string
	// AuthMode selects how the GitHub token is resolved.
	AuthMode AuthMode
	// BaseURL is a GitHub Enterprise Server instance root, such as
	// "https://ghe.corp". Empty, or any github.com address, means
	// github.com. The REST API is reached under /api/v3 on the instance,
	// and the token is resolved for that host, never github.com's.
	BaseURL string
	// Type filters repositories by specific category (e.g. "sources", "forks", "archived", "mirrors", etc.).
	Type string
	// Sort specifies how the returned repositories list should be ordered.
	Sort string
	// RetryMax is the maximum number of retry attempts for transient failures.
	RetryMax int
	// RetryMinBackoff is the minimum delay between retry attempts.
	RetryMinBackoff time.Duration
	// RetryMaxBackoff is the maximum delay between retry attempts.
	RetryMaxBackoff time.Duration
	// RequestTimeout bounds a single HTTP request to the GitHub API,
	// including its redirects and body read. It does not bound the
	// operation as a whole: listing a large organisation is many requests.
	RequestTimeout time.Duration
	// TotalTimeout bounds the complete operation — every page, every retry
	// and every backoff between them. It must be at least RequestTimeout,
	// and wants to be far larger: an organisation with 5,000 repositories
	// is 50 pages at 100 per page, and a rate-limited run spends most of
	// its budget waiting rather than transferring.
	TotalTimeout time.Duration
	// Timeout is the pre-v0.0.29 single knob, which was applied as both of
	// the above at once. Kept so an embedder compiled against the old
	// field keeps working: when it is set and the two fields above are
	// not, it supplies both.
	//
	// Deprecated: set RequestTimeout and TotalTimeout instead.
	Timeout time.Duration
}

const (
	// defaultRequestTimeout bounds one HTTP request. 30s is generous for a
	// single GitHub API call and short enough that a hung connection is
	// noticed rather than waited on.
	defaultRequestTimeout = 30 * time.Second
	// defaultTotalTimeout bounds the whole paginated operation.
	//
	// The previous single 30s budget covered every page *and* every
	// backoff, so a large organisation could not be listed at all: 50
	// pages plus retries do not fit, and the run died with a context
	// error that named no cause. 10 minutes fits a listing of any
	// plausible size with room for the retry schedule.
	defaultTotalTimeout    = 10 * time.Minute
	defaultRetryMax        = 4
	defaultRetryMinBackoff = 500 * time.Millisecond
	defaultRetryMaxBackoff = 8 * time.Second
)

// RetryBudgetError reports that a retry was required but could not be
// waited out inside the operation's remaining time. It carries the numbers
// so the message can tell the user what to raise, rather than leaving them
// with a deadline error and no cause.
type RetryBudgetError struct {
	// Wait is how long the server asked us to wait.
	Wait time.Duration
	// Remaining is how much of --api-total-timeout was left.
	Remaining time.Duration
	// Status is the HTTP status that triggered the retry, or 0 for a
	// transport error.
	Status int
}

// Error implements the error interface.
func (e *RetryBudgetError) Error() string {
	reason := "the server asked us to back off"
	if e.Status == http.StatusForbidden || e.Status == http.StatusTooManyRequests {
		reason = "GitHub rate-limited this token"
	}
	return fmt.Sprintf(
		"%s for %s, but only %s of the total budget remained; "+
			"raise --api-total-timeout above %s to wait it out",
		reason, e.Wait.Round(time.Second), e.Remaining.Round(time.Second),
		e.Wait.Round(time.Second))
}
