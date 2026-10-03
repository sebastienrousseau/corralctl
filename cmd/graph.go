// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/graph"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	graphRepo   string
	graphJSON   bool
	graphOutput string
	graphScan   = corralmcp.Scan
)

// RepoGraphDetail represents single-repository dependency inspection details.
type RepoGraphDetail struct {
	TargetRepo   string   `json:"target_repo"`
	Path         string   `json:"path"`
	Language     string   `json:"language"`
	PackageName  string   `json:"package_name,omitempty"`
	Dependencies []string `json:"dependencies"`
	Dependents   []string `json:"dependents"`
}

// WorkspaceGraphReport represents full workspace dependency topology.
type WorkspaceGraphReport struct {
	Nodes            []graph.Node        `json:"nodes"`
	Edges            []graph.Edge        `json:"edges"`
	TopologicalOrder []string            `json:"topological_order,omitempty"`
	Cycles           [][]string          `json:"cycles,omitempty"`
	Dependents       map[string][]string `json:"dependents,omitempty"`
}

var graphCmd = &cobra.Command{
	Use:   "graph [base_dir]",
	Short: "Analyze inter-repository package dependencies across the workspace",
	Long:  "Analyze inter-repository package dependencies across Go modules, Rust crates, Node packages, and Python projects in the workspace corral.",
	Args:  cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if graphOutput != "" && graphOutput != "text" && graphOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := resolvedBaseDir(args)
		idx, err := graphScan(root)
		if err != nil {
			return err
		}

		repos := make([]graph.RepoInput, 0, len(idx.Repos))
		for _, r := range idx.Repos {
			repos = append(repos, graph.RepoInput{
				Name:      r.Name,
				Path:      r.Path,
				Language:  r.Language,
				RemoteURL: r.RemoteURL,
			})
		}

		g := graph.BuildGraph(repos)
		isJSON := graphJSON || graphOutput == "json"
		return runGraphWith(idx, graphRepo, isJSON, g)
	},
}

func runGraphWith(idx *corralmcp.Index, repoFilter string, isJSON bool, g *graph.Graph) error {
	if repoFilter != "" {
		match, err := idx.Find(repoFilter)
		if err != nil {
			return fmt.Errorf("repository %q: %w", repoFilter, err)
		}
		repoName := match.Name
		var node *graph.Node
		for i := range g.Nodes {
			if g.Nodes[i].Name == repoName {
				node = &g.Nodes[i]
				break
			}
		}
		if node == nil {
			return fmt.Errorf("repository %q not found in graph", repoName)
		}

		deps := node.Dependencies
		if deps == nil {
			deps = []string{}
		}
		dependents := g.Dependents[repoName]
		if dependents == nil {
			dependents = []string{}
		}

		if isJSON {
			res := RepoGraphDetail{
				TargetRepo:   repoName,
				Path:         node.Path,
				Language:     node.Language,
				PackageName:  node.PackageName,
				Dependencies: deps,
				Dependents:   dependents,
			}
			return writeJSON(os.Stdout, res)
		}

		fmt.Printf("Repository:   %s\n", repoName)
		fmt.Printf("Path:         %s\n", node.Path)
		if node.Language != "" {
			fmt.Printf("Language:     %s\n", node.Language)
		}
		if node.PackageName != "" {
			fmt.Printf("Package:      %s\n", node.PackageName)
		}
		if len(deps) > 0 {
			fmt.Printf("Dependencies (%d):\n", len(deps))
			for _, dep := range deps {
				fmt.Printf("  - %s\n", dep)
			}
		} else {
			fmt.Println("Dependencies: none")
		}
		if len(dependents) > 0 {
			fmt.Printf("Dependents (%d):\n", len(dependents))
			for _, dep := range dependents {
				fmt.Printf("  - %s\n", dep)
			}
		} else {
			fmt.Println("Dependents:   none")
		}
		return nil
	}

	if isJSON {
		res := WorkspaceGraphReport{
			Nodes:            g.Nodes,
			Edges:            g.Edges,
			TopologicalOrder: g.TopologicalOrder,
			Cycles:           g.Cycles,
			Dependents:       g.Dependents,
		}
		return writeJSON(os.Stdout, res)
	}

	if len(g.Nodes) == 0 {
		fmt.Println("No repositories found in workspace.")
		return nil
	}

	fmt.Printf("Repositories (%d):\n", len(g.Nodes))
	for _, node := range g.Nodes {
		if len(node.Dependencies) > 0 {
			fmt.Printf("  %-24s -> [%s]\n", node.Name, strings.Join(node.Dependencies, ", "))
		} else {
			fmt.Printf("  %-24s (no dependencies)\n", node.Name)
		}
	}

	if len(g.TopologicalOrder) > 0 {
		fmt.Printf("\nTopological Build Order (%d):\n", len(g.TopologicalOrder))
		for i, name := range g.TopologicalOrder {
			fmt.Printf("  %2d. %s\n", i+1, name)
		}
	}

	if len(g.Cycles) > 0 {
		fmt.Printf("\nCycles Detected (%d):\n", len(g.Cycles))
		for i, cycle := range g.Cycles {
			fmt.Printf("  Cycle %d: %s\n", i+1, strings.Join(cycle, " -> "))
		}
	}

	return nil
}

func init() {
	graphCmd.Flags().StringVar(&graphRepo, "repo", "", "filter graph to a specific repository and its direct dependencies")
	graphCmd.Flags().BoolVar(&graphJSON, "json", false, "output graph as JSON")
	graphCmd.Flags().StringVar(&graphOutput, "output", "", "output format: text or json")
	rootCmd.AddCommand(graphCmd)
}
