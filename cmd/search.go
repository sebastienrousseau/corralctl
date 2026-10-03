// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/sebastienrousseau/corralctl/internal/search"
	"github.com/spf13/cobra"
)

var (
	searchRepoFlag      string
	searchLangFlag      string
	searchRegex         bool
	searchCaseSensitive bool
	searchIncludeTests  bool
	searchMax           int
	searchPathGlob      string
	searchOutput        string
	searchJSON          bool

	searchScan   = corralmcp.Scan
	searchRepoOp = search.SearchRepo
)

// SearchHitDetail represents a single line match in a repository file.
type SearchHitDetail struct {
	Repo   string `json:"repo"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Text   string `json:"text"`
}

// SearchResultReport contains the full search results for JSON output.
type SearchResultReport struct {
	Pattern       string            `json:"pattern"`
	TotalHits     int               `json:"total_hits"`
	ReposSearched int               `json:"repos_searched"`
	FilesSearched int               `json:"files_searched"`
	Truncated     bool              `json:"truncated"`
	Hits          []SearchHitDetail `json:"hits"`
}

var searchCmd = &cobra.Command{
	Use:   "search <pattern> [base_dir]",
	Short: "Search file contents across repositories in the workspace",
	Long:  "Search file contents across repositories in the Corral workspace, matching against source and documentation files.",
	Args:  cobra.RangeArgs(1, 2),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if searchOutput != "" && searchOutput != "text" && searchOutput != "json" {
			return errors.New("--output must be text or json")
		}
		if searchMax <= 0 {
			return errors.New("--max must be greater than 0")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		pattern := args[0]
		root := resolvedBaseDir(nil)
		if len(args) > 1 {
			root = resolvedBaseDir(args[1:])
		}
		return runSearch(cmdContext(cmd), root, pattern)
	},
}

func runSearch(ctx context.Context, root, pattern string) error {
	idx, err := searchScan(root)
	if err != nil {
		return err
	}

	targets := idx.Repos
	if searchRepoFlag != "" {
		match, findErr := idx.Find(searchRepoFlag)
		if findErr != nil {
			return fmt.Errorf("repository %q: %w", searchRepoFlag, findErr)
		}
		targets = []corralmcp.RepoEntry{*match}
	} else if searchLangFlag != "" {
		want := strings.ToLower(strings.TrimSpace(searchLangFlag))
		var filtered []corralmcp.RepoEntry
		for _, r := range targets {
			if strings.ToLower(strings.TrimSpace(r.Language)) == want {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("no repositories found with language %q", searchLangFlag)
		}
		targets = filtered
	}

	matcher, err := search.Compile(search.Query{
		Pattern:       pattern,
		Regex:         searchRegex,
		CaseSensitive: searchCaseSensitive,
		PathGlob:      searchPathGlob,
		IncludeTests:  searchIncludeTests,
		MaxHits:       searchMax,
	})
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		if searchJSON || searchOutput == "json" {
			return writeJSON(os.Stdout, SearchResultReport{
				Pattern: pattern,
				Hits:    []SearchHitDetail{},
			})
		}
		fmt.Println("No repositories found to search.")
		return nil
	}

	var (
		allHits       = []SearchHitDetail{}
		reposSearched int
		filesSearched int
		truncated     bool
	)

	for _, repo := range targets {
		if len(allHits) >= searchMax {
			truncated = true
			break
		}
		res, searchErr := searchRepoOp(ctx, repo.Path, matcher, corralmcp.FileAllowed)
		if searchErr != nil {
			continue
		}
		filesSearched += res.Files
		reposSearched++
		for _, h := range res.Hits {
			allHits = append(allHits, SearchHitDetail{
				Repo:   repo.Name,
				File:   h.File,
				Line:   h.Line,
				Column: h.Column,
				Text:   h.Text,
			})
			if len(allHits) >= searchMax {
				truncated = true
				break
			}
		}
		if res.Truncated {
			truncated = true
		}
	}

	if searchJSON || searchOutput == "json" {
		report := SearchResultReport{
			Pattern:       pattern,
			TotalHits:     len(allHits),
			ReposSearched: reposSearched,
			FilesSearched: filesSearched,
			Truncated:     truncated,
			Hits:          allHits,
		}
		return writeJSON(os.Stdout, report)
	}

	if len(allHits) == 0 {
		fmt.Println("No matches found.")
		return nil
	}

	for _, hit := range allHits {
		fmt.Printf("%s/%s:%d:%d: %s\n", hit.Repo, hit.File, hit.Line, hit.Column, hit.Text)
	}

	if truncated {
		fmt.Fprintf(os.Stderr, "corralctl search: results truncated at %d matches\n", searchMax)
	}

	return nil
}

func init() {
	searchCmd.Flags().StringVar(&searchRepoFlag, "repo", "", "Limit search to a single repository by name or relative path")
	searchCmd.Flags().StringVar(&searchLangFlag, "lang", "", "Filter repositories by primary language")
	searchCmd.Flags().BoolVar(&searchRegex, "regex", false, "Treat pattern as a regular expression")
	searchCmd.Flags().BoolVarP(&searchCaseSensitive, "case-sensitive", "s", false, "Enable case-sensitive matching")
	searchCmd.Flags().BoolVarP(&searchIncludeTests, "tests", "t", false, "Include test files in search")
	searchCmd.Flags().IntVar(&searchMax, "max", 50, "Maximum number of matching lines to report")
	searchCmd.Flags().StringVar(&searchPathGlob, "path-glob", "", "Filter files by path glob (e.g. *.go)")
	searchCmd.Flags().StringVar(&searchOutput, "output", "text", "Output format (text, json)")
	searchCmd.Flags().BoolVar(&searchJSON, "json", false, "Output search results as JSON (shorthand for --output json)")

	rootCmd.AddCommand(searchCmd)
}
