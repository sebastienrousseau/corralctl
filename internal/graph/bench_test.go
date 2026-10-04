// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package graph

import (
	"fmt"
	"testing"
)

// BenchmarkBuildGraphSmall measures dependency graph construction for 50 repositories.
func BenchmarkBuildGraphSmall(b *testing.B) {
	repos := make([]RepoInput, 50)
	for i := 0; i < 50; i++ {
		repos[i] = RepoInput{
			Name:      fmt.Sprintf("repo-%02d", i),
			Path:      fmt.Sprintf("/workspace/repo-%02d", i),
			Language:  "Go",
			RemoteURL: fmt.Sprintf("https://github.com/org/repo-%02d.git", i),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = BuildGraph(repos)
	}
}

// BenchmarkBuildGraphLarge measures graph construction and dependency linking
// across 1,000 repositories.
func BenchmarkBuildGraphLarge(b *testing.B) {
	const count = 1000
	repos := make([]RepoInput, count)
	for i := 0; i < count; i++ {
		repos[i] = RepoInput{
			Name:      fmt.Sprintf("service-%04d", i),
			Path:      fmt.Sprintf("/workspace/service-%04d", i),
			Language:  "Go",
			RemoteURL: fmt.Sprintf("https://github.com/org/service-%04d.git", i),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = BuildGraph(repos)
	}
}

// BenchmarkCycleDetection10K measures cycle detection performance on a graph
// topology consisting of 10,000 vertices and periodic cyclic feedback loops.
func BenchmarkCycleDetection10K(b *testing.B) {
	const count = 10000
	nodes := make([]Node, count)
	adjList := make(map[string][]string, count)

	for i := 0; i < count; i++ {
		name := fmt.Sprintf("pkg-%05d", i)
		nodes[i] = Node{Name: name}
		if i > 0 {
			prev := fmt.Sprintf("pkg-%05d", i-1)
			adjList[prev] = append(adjList[prev], name)
		}
		// Introduce a cycle every 500 nodes
		if i > 0 && i%500 == 0 {
			cycleTarget := fmt.Sprintf("pkg-%05d", i-10)
			adjList[name] = append(adjList[name], cycleTarget)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = computeTopologicalOrder(nodes, adjList)
	}
}

// BenchmarkTopologicalSort10K measures topological sort throughput over a 10,000-node
// directed acyclic dependency tree.
func BenchmarkTopologicalSort10K(b *testing.B) {
	const count = 10000
	nodes := make([]Node, count)
	adjList := make(map[string][]string, count)

	for i := 0; i < count; i++ {
		name := fmt.Sprintf("module-%05d", i)
		nodes[i] = Node{Name: name}
		if i < count-1 {
			next := fmt.Sprintf("module-%05d", i+1)
			adjList[name] = []string{next}
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = computeTopologicalOrder(nodes, adjList)
	}
}
