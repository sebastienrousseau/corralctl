// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package graph

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RepoInput represents a repository within the corral workspace to analyze.
type RepoInput struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Language  string `json:"language"`
	RemoteURL string `json:"remote_url"`
}

// Node represents a repository vertex in the workspace dependency graph.
type Node struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Language     string   `json:"language"`
	PackageName  string   `json:"package_name,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// Edge represents a directed dependency link from one repository to another.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph represents the complete cross-repository dependency topology.
type Graph struct {
	Nodes            []Node              `json:"nodes"`
	Edges            []Edge              `json:"edges"`
	TopologicalOrder []string            `json:"topological_order,omitempty"`
	Cycles           [][]string          `json:"cycles,omitempty"`
	Dependents       map[string][]string `json:"dependents,omitempty"`
}

// BuildGraph inspects the package manifests across the provided repositories and
// constructs an inter-repository dependency graph with topological ordering.
func BuildGraph(repos []RepoInput) *Graph {
	nodes := make([]Node, 0, len(repos))
	pkgToRepo := make(map[string]string)
	nameToRepo := make(map[string]string)

	rawDeps := make(map[string][]string)

	for _, r := range repos {
		pkgName, deps := extractManifestInfo(r.Path, r.Language)
		node := Node{
			Name:         r.Name,
			Path:         r.Path,
			Language:     r.Language,
			PackageName:  pkgName,
			Dependencies: []string{},
		}
		nodes = append(nodes, node)
		nameToRepo[strings.ToLower(r.Name)] = r.Name
		if pkgName != "" {
			pkgToRepo[strings.ToLower(pkgName)] = r.Name
		}
		if r.RemoteURL != "" {
			canon := canonicalURL(r.RemoteURL)
			if canon != "" {
				pkgToRepo[canon] = r.Name
			}
		}
		rawDeps[r.Name] = deps
	}

	edges := make([]Edge, 0)
	edgeSet := make(map[string]bool)
	dependents := make(map[string][]string)
	adjList := make(map[string][]string) // from -> to (dependencies)

	for i := range nodes {
		repoName := nodes[i].Name
		resolved := make([]string, 0)
		for _, dep := range rawDeps[repoName] {
			targetRepo := resolveDep(dep, pkgToRepo, nameToRepo)
			if targetRepo != "" && targetRepo != repoName {
				edgeKey := repoName + "->" + targetRepo
				if !edgeSet[edgeKey] {
					edgeSet[edgeKey] = true
					edges = append(edges, Edge{From: repoName, To: targetRepo})
					resolved = append(resolved, targetRepo)
					dependents[targetRepo] = append(dependents[targetRepo], repoName)
					adjList[repoName] = append(adjList[repoName], targetRepo)
				}
			}
		}
		sort.Strings(resolved)
		nodes[i].Dependencies = resolved
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})

	for k := range dependents {
		sort.Strings(dependents[k])
	}

	order, cycles := computeTopologicalOrder(nodes, adjList)

	return &Graph{
		Nodes:            nodes,
		Edges:            edges,
		TopologicalOrder: order,
		Cycles:           cycles,
		Dependents:       dependents,
	}
}

// resolveDep maps a raw dependency string to a known corral repository name.
func resolveDep(dep string, pkgToRepo, nameToRepo map[string]string) string {
	lower := strings.ToLower(dep)
	if target, ok := pkgToRepo[lower]; ok {
		return target
	}
	if target, ok := nameToRepo[lower]; ok {
		return target
	}
	base := filepath.Base(lower)
	if target, ok := nameToRepo[base]; ok {
		return target
	}
	return ""
}

// canonicalURL extracts a normalized host/path key from a Git remote URL.
func canonicalURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimPrefix(u, "git@")
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "ssh://")
	u = strings.Replace(u, ":", "/", 1)
	u = strings.TrimSuffix(u, ".git")
	return strings.ToLower(u)
}

// computeTopologicalOrder performs topological sorting and cycle detection.
func computeTopologicalOrder(nodes []Node, adjList map[string][]string) ([]string, [][]string) {
	inDegree := make(map[string]int)
	depMap := make(map[string][]string) // to -> list of from (who depends on it)

	for _, n := range nodes {
		inDegree[n.Name] = len(adjList[n.Name])
		for _, dep := range adjList[n.Name] {
			depMap[dep] = append(depMap[dep], n.Name)
		}
	}

	queue := make([]string, 0)
	for _, n := range nodes {
		if inDegree[n.Name] == 0 {
			queue = append(queue, n.Name)
		}
	}
	sort.Strings(queue)

	order := make([]string, 0, len(nodes))
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		order = append(order, curr)

		for _, dependent := range depMap[curr] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Strings(queue)
			}
		}
	}

	var cycles [][]string
	if len(order) < len(nodes) {
		remaining := make(map[string]bool)
		for _, n := range nodes {
			if inDegree[n.Name] > 0 {
				remaining[n.Name] = true
			}
		}
		var cycleNodes []string
		for name := range remaining {
			cycleNodes = append(cycleNodes, name)
		}
		sort.Strings(cycleNodes)
		if len(cycleNodes) > 0 {
			cycles = append(cycles, cycleNodes)
		}
	}

	return order, cycles
}

// extractManifestInfo reads language-specific package manifests from a repository path.
func extractManifestInfo(root, lang string) (string, []string) {
	lowerLang := strings.ToLower(lang)

	switch {
	case lowerLang == "go" || fileExists(filepath.Join(root, "go.mod")):
		return parseGoMod(filepath.Join(root, "go.mod"))
	case lowerLang == "rust" || fileExists(filepath.Join(root, "Cargo.toml")):
		return parseCargoTOML(filepath.Join(root, "Cargo.toml"))
	case lowerLang == "javascript" || lowerLang == "typescript" || fileExists(filepath.Join(root, "package.json")):
		return parsePackageJSON(filepath.Join(root, "package.json"))
	case lowerLang == "python" || fileExists(filepath.Join(root, "pyproject.toml")):
		return parsePyprojectTOML(filepath.Join(root, "pyproject.toml"))
	default:
		return "", nil
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// parseGoMod parses a go.mod file extracting the module path and required dependencies.
func parseGoMod(path string) (string, []string) {
	file, err := os.Open(filepath.Clean(path)) //nolint:gosec // G304: manifest path is within repository
	if err != nil {
		return "", nil
	}
	defer func() { _ = file.Close() }()

	var moduleName string
	var deps []string
	inRequire := false

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			moduleName = strings.TrimSpace(strings.TrimPrefix(line, "module"))
			continue
		}
		if line == "require (" {
			inRequire = true
			continue
		}
		if inRequire && line == ")" {
			inRequire = false
			continue
		}
		if inRequire {
			fields := strings.Fields(line)
			if len(fields) > 0 && !strings.HasPrefix(fields[0], "//") {
				deps = append(deps, fields[0])
			}
			continue
		}
		if strings.HasPrefix(line, "require ") {
			fields := strings.Fields(strings.TrimPrefix(line, "require"))
			if len(fields) > 0 {
				deps = append(deps, fields[0])
			}
		}
	}
	return moduleName, deps
}

// parsePackageJSON parses a Node package.json manifest.
func parsePackageJSON(path string) (string, []string) {
	data, err := os.ReadFile(filepath.Clean(path)) //nolint:gosec // G304: manifest path is within repository
	if err != nil {
		return "", nil
	}
	var manifest struct {
		Name            string            `json:"name"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", nil
	}
	var deps []string
	for dep := range manifest.Dependencies {
		deps = append(deps, dep)
	}
	for dep := range manifest.DevDependencies {
		deps = append(deps, dep)
	}
	return manifest.Name, deps
}

// parseCargoTOML parses a Rust Cargo.toml manifest.
func parseCargoTOML(path string) (string, []string) {
	file, err := os.Open(filepath.Clean(path)) //nolint:gosec // G304: manifest path is within repository
	if err != nil {
		return "", nil
	}
	defer func() { _ = file.Close() }()

	var name string
	var deps []string
	section := ""

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if section == "[package]" && strings.HasPrefix(line, "name") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				name = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			}
			continue
		}
		if strings.HasPrefix(section, "[dependencies") || strings.HasPrefix(section, "[dev-dependencies") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				depName := strings.TrimSpace(parts[0])
				deps = append(deps, depName)
			}
		}
	}
	return name, deps
}

// parsePyprojectTOML parses a Python pyproject.toml manifest.
func parsePyprojectTOML(path string) (string, []string) {
	file, err := os.Open(filepath.Clean(path)) //nolint:gosec // G304: manifest path is within repository
	if err != nil {
		return "", nil
	}
	defer func() { _ = file.Close() }()

	var name string
	var deps []string
	section := ""

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if (section == "[project]" || section == "[tool.poetry]") && strings.HasPrefix(line, "name") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				name = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			}
			continue
		}
		if section == "[tool.poetry.dependencies]" {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				depName := strings.TrimSpace(parts[0])
				if depName != "python" {
					deps = append(deps, depName)
				}
			}
		}
	}
	return name, deps
}
