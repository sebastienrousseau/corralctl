// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

// DoctorRules specifies user-configured thresholds for workspace health.
type DoctorRules struct {
	DiskBudgetBytes     int64 `json:"disk_budget_bytes,omitempty"`
	MaxUnpushedRepos    *int  `json:"max_unpushed_repos,omitempty"`
	MaxDirtyRepos       *int  `json:"max_dirty_repos,omitempty"`
	MaxWorktrees        *int  `json:"max_worktrees,omitempty"`
	RequireCaches       bool  `json:"require_caches,omitempty"`
	WarnOnMissingOrigin bool  `json:"warn_on_missing_origin,omitempty"`
}

// CorralConfig represents top-level configuration loaded from .corral.json.
type CorralConfig struct {
	Doctor DoctorRules `json:"doctor,omitempty"`
}

// loadDoctorRules loads doctor rules from --config, workspace .corral.json, or ~/.config/corral/config.json.
func loadDoctorRules(root, customPath string) (*DoctorRules, error) {
	var candidates []string
	if customPath != "" {
		candidates = append(candidates, customPath)
	} else {
		candidates = append(candidates, filepath.Join(root, ".corral.json"))
		if cfgDir, err := doctorUserConfigDir(); err == nil {
			candidates = append(candidates, filepath.Join(cfgDir, "corral", "config.json"))
		}
	}

	for _, cand := range candidates {
		data, err := doctorReadFile(cand)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading config %s: %w", cand, err)
		}
		var cfg CorralConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", cand, err)
		}
		return &cfg.Doctor, nil
	}
	return nil, nil
}

// evaluateDoctorRules applies user thresholds against the gathered doctor report.
func evaluateDoctorRules(report *DoctorReport, rules *DoctorRules, repos []corralmcp.RepoEntry) {
	if rules == nil {
		return
	}
	report.RulesApplied = true
	if rules.DiskBudgetBytes > 0 && report.TotalSizeBytes > rules.DiskBudgetBytes {
		report.Violations = append(report.Violations,
			fmt.Sprintf("disk usage %s exceeds budget %s", report.TotalSize, formatHumanSize(rules.DiskBudgetBytes)))
	}
	if rules.MaxDirtyRepos != nil && len(report.DirtyRepos) > *rules.MaxDirtyRepos {
		report.Violations = append(report.Violations,
			fmt.Sprintf("dirty repos (%d) exceed allowed limit (%d)", len(report.DirtyRepos), *rules.MaxDirtyRepos))
	}
	if rules.MaxUnpushedRepos != nil && len(report.UnpushedRepos) > *rules.MaxUnpushedRepos {
		report.Violations = append(report.Violations,
			fmt.Sprintf("unpushed repos (%d) exceed allowed limit (%d)", len(report.UnpushedRepos), *rules.MaxUnpushedRepos))
	}
	if rules.MaxWorktrees != nil && len(report.Worktrees) > *rules.MaxWorktrees {
		report.Violations = append(report.Violations,
			fmt.Sprintf("linked worktrees (%d) exceed allowed limit (%d)", len(report.Worktrees), *rules.MaxWorktrees))
	}
	if rules.RequireCaches && (!report.CacheStatus.IndexCacheValid || !report.CacheStatus.SymbolCacheValid) {
		report.Violations = append(report.Violations, "cache integrity check failed: missing required index or symbol cache")
	}
	if rules.WarnOnMissingOrigin {
		for _, repo := range repos {
			if repo.RemoteURL == "" {
				report.Violations = append(report.Violations, fmt.Sprintf("repository %s has no remote origin configured", repo.Name))
			}
		}
	}
}
