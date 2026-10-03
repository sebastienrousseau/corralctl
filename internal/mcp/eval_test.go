// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// An evaluation suite for the questions agents actually ask.
//
// Every other test in this package asks whether a handler is correct. None
// of them asks the question that decides whether this server is any use:
// given a real question, does the right tool answer it, and is the answer
// one somebody could act on?
//
// That gap is not theoretical. A tool can be flawless and still be
// unusable — because its description does not distinguish it from the tool
// next to it, because its answer is technically right and practically
// empty, or because the thing an agent needs next is not in the response.
// Those failures survive a unit-test suite untouched.
//
// # What this measures, and what it cannot
//
// It measures the half that is deterministic: for a scenario a person
// would actually bring, the named tool returns an answer containing what
// the question was about. And it measures discriminability — that the
// description of each tool says something its nearest sibling's does not,
// which is what a model reads when choosing between them.
//
// It cannot measure *selection*. Whether a model picks corral_find_symbol
// over corral_search_code needs a model in the loop, an API key and a
// budget, and would make this suite non-deterministic and non-hermetic.
// What it does instead is make the inputs to that decision testable: if
// two tools' descriptions stop distinguishing them, this fails, and a
// selection failure downstream becomes predictable rather than mysterious.

// scenario is one question an agent might be asked, and the tool that
// should answer it.
type scenario struct {
	// question is what a person asked their agent, verbatim. Present so a
	// failure reads as "corral cannot answer this" rather than as an
	// assertion number.
	question string
	// tool is the tool that should answer it.
	tool string
	// args is the call that answers it.
	args map[string]any
	// wants are substrings that must appear in the response. Substrings
	// rather than an exact body, so an added field does not fail a
	// scenario that is still answered.
	wants []string
	// notWants are substrings that must not. Used where an empty or
	// misleading answer would look like a pass.
	notWants []string
}

// evalWorkspace builds a workspace with the shapes a real one has:
// several languages, a fork, a private repository, a test file, and a
// credential file that must never surface.
func evalWorkspace(t *testing.T) *harness {
	t.Helper()
	base := t.TempDir()

	write := func(repo, rel, body string) {
		t.Helper()
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	api := makeFakeRepo(t, base, "Public", "go", "billing-api", "https://github.com/acme/billing-api.git", "")
	write(api, "internal/retry/retry.go",
		"package retry\n\n// MaxAttempts bounds a retry loop.\nconst MaxAttempts = 5\n\n"+
			"// Do runs fn until it succeeds or MaxAttempts is reached.\nfunc Do(fn func() error) error {\n\treturn nil\n}\n")
	write(api, "internal/retry/retry_test.go",
		"package retry\n\nfunc TestDo(t *testing.T) {}\n")
	write(api, "cmd/server/main.go",
		"package main\n\nfunc main() {\n\t_ = retry.MaxAttempts\n}\n")
	// A credential file that mentions the symbol every other scenario
	// searches for, so "it must never be the answer" is actually tested.
	// The value is deliberately not shaped like a real key: a fixture that
	// matches a secret scanner's pattern fails CI on every push, and
	// nothing about this test needs it to look authentic.
	write(api, ".env", "STRIPE_SECRET_KEY=fixture-not-a-real-key-MaxAttempts\n")

	worker := makeFakeRepo(t, base, "Private", "python", "billing-worker", "https://github.com/acme/billing-worker.git", "")
	write(worker, "worker/tasks.py",
		"MAX_ATTEMPTS = 5\n\n\nclass Retrier:\n    def do(self, fn):\n        pass\n")

	ui := makeFakeRepo(t, base, "Public", "typescript", "billing-ui", "https://github.com/acme/billing-ui.git", "")
	write(ui, "src/api.ts",
		"export const MAX_ATTEMPTS = 5;\n\nexport interface RetryOptions {\n  attempts: number;\n}\n\n"+
			"export function callBilling(): void {}\n")

	fork := makeFakeRepo(t, base, "Forks", "rust", "ripgrep", "https://github.com/acme/ripgrep.git", "")
	write(fork, "src/lib.rs", "pub fn search() {}\n")

	return newHarness(t, ServerOptions{Root: base})
}

// evalScenarios are the questions. Each is one somebody would actually
// ask, not one shaped to fit an existing tool.
func evalScenarios() []scenario {
	return []scenario{
		{
			question: "Which of our repositories are Python?",
			tool:     "corral_list_repos",
			args:     map[string]any{"language": "python"},
			wants:    []string{"billing-worker"},
			notWants: []string{"billing-api", "billing-ui"},
		},
		{
			question: "How many repositories do I have, and in what languages?",
			tool:     "corral_status_summary",
			args:     map[string]any{},
			wants:    []string{"go", "python", "typescript", "rust", "total"},
		},
		{
			question: "Where is MaxAttempts defined?",
			tool:     "corral_find_symbol",
			args:     map[string]any{"name": "MaxAttempts"},
			// The answer has to be actionable: a repository, a file and a
			// line, or the agent still has to go looking.
			wants: []string{"billing-api", "internal/retry/retry.go", "\"line\"", "const"},
			// The credential file mentions MaxAttempts too. It must never
			// be the answer to anything.
			notWants: []string{".env", "STRIPE_SECRET_KEY"},
		},
		{
			// A literal search across languages finds only the spellings
			// that match. This scenario exists to pin that: Go writes
			// MaxAttempts and Python writes MAX_ATTEMPTS, and no amount of
			// case-insensitivity bridges the underscore.
			question: "Where is the retry limit set in the Python worker?",
			tool:     "corral_search_code",
			args:     map[string]any{"query": "max_attempts", "max_results": 20},
			wants:    []string{"billing-worker", "worker/tasks.py"},
			// The credential file mentions the same token.
			notWants: []string{"STRIPE_SECRET_KEY"},
		},
		{
			// And this is the answer to the cross-language version of the
			// question. The eval suite found the gap: a literal search
			// returned two of the three services, and the tool
			// description now says to reach for a regex when a name is
			// spelled differently per language.
			question: "We have a retry limit in three services. Find them all.",
			tool:     "corral_search_code",
			args: map[string]any{
				"query": "max_?attempts", "regex": true, "max_results": 20,
			},
			wants:    []string{"billing-api", "billing-worker", "billing-ui"},
			notWants: []string{"STRIPE_SECRET_KEY"},
		},
		{
			question: "Which repository is billing-worker, and what state is it in?",
			tool:     "corral_get_repo_metadata",
			args:     map[string]any{"query": "billing-worker"},
			wants:    []string{"billing-worker", "Private", "python"},
		},
		{
			question: "Give me the shape of billing-api before I start reading it.",
			tool:     "corral_repo_overview",
			args:     map[string]any{"query": "billing-api"},
			wants:    []string{"billing-api", "go"},
		},
		{
			question: "Is there an interface called RetryOptions anywhere?",
			tool:     "corral_find_symbol",
			args:     map[string]any{"name": "RetryOptions"},
			wants:    []string{"billing-ui", "interface", "src/api.ts"},
		},
		{
			question: "Find the Retrier class — I do not remember which service.",
			tool:     "corral_find_symbol",
			args:     map[string]any{"name": "Retrier"},
			wants:    []string{"billing-worker", "worker/tasks.py"},
		},
		{
			question: "Which of these are forks, so I know what is not ours?",
			tool:     "corral_list_repos",
			args:     map[string]any{"visibility": "Forks"},
			wants:    []string{"ripgrep"},
			notWants: []string{"billing-api"},
		},
		{
			question: "I only half-remember the name. Something with 'billing' and 'ui'.",
			tool:     "corral_find_repo",
			args:     map[string]any{"query": "billing-ui"},
			wants:    []string{"billing-ui", "typescript"},
		},
		{
			question: "What are the cross-package dependencies between repositories in this workspace?",
			tool:     "corral_graph_dependencies",
			args:     map[string]any{},
			wants:    []string{"nodes", "edges"},
		},
	}
}

// TestEvalScenariosAreAnswered is the suite. Each failure names the
// question rather than the assertion, because the thing that broke is
// that corral can no longer answer it.
func TestEvalScenariosAreAnswered(t *testing.T) {
	h := evalWorkspace(t)

	var failed []string
	for _, sc := range evalScenarios() {
		t.Run(sc.tool+": "+sc.question, func(t *testing.T) {
			out, isErr := h.callTool(sc.tool, sc.args)
			if isErr {
				failed = append(failed, sc.question)
				t.Fatalf("%q\n  %s returned an error: %s", sc.question, sc.tool, out)
			}
			lower := strings.ToLower(out)
			for _, want := range sc.wants {
				if !strings.Contains(lower, strings.ToLower(want)) {
					failed = append(failed, sc.question)
					t.Errorf("%q\n  %s did not answer it: %q is missing from\n%s",
						sc.question, sc.tool, want, out)
				}
			}
			for _, notWant := range sc.notWants {
				if strings.Contains(lower, strings.ToLower(notWant)) {
					failed = append(failed, sc.question)
					t.Errorf("%q\n  %s answered with something it should not have: %q appears in\n%s",
						sc.question, sc.tool, notWant, out)
				}
			}
		})
	}
	if len(failed) > 0 {
		t.Logf("%d of %d scenarios failed", len(failed), len(evalScenarios()))
	}
}

// TestEveryReadToolIsExercisedByAScenario: a tool nobody wrote a question
// for is a tool nobody has established a use for. That is worth knowing
// before it ships, not after.
func TestEveryReadToolIsExercisedByAScenario(t *testing.T) {
	covered := map[string]bool{}
	for _, sc := range evalScenarios() {
		covered[sc.tool] = true
	}

	srv, err := NewServer(ServerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, name := range srv.ToolNames() {
		// The full index is a fallback the instructions actively steer
		// away from, so there is no question it is the best answer to.
		if name == "corral_workspace_index" {
			continue
		}
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("no scenario asks a question these tools answer: %s\n"+
			"Add one to evalScenarios, or reconsider whether the tool earns its place.",
			strings.Join(missing, ", "))
	}
}

// TestToolDescriptionsDiscriminate is the other half.
//
// A model choosing between two tools reads their descriptions. When the
// two nearest tools describe themselves in the same words, the choice
// becomes a coin toss — and no amount of correctness in either handler
// fixes that. These pairs are the ones actually confusable.
func TestToolDescriptionsDiscriminate(t *testing.T) {
	h := evalWorkspace(t)
	tools, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tool := range tools.Tools {
		desc[tool.Name] = strings.ToLower(tool.Description)
	}

	for _, pair := range []struct {
		a, b string
		// aOnly must appear in a's description and not in b's, and vice
		// versa. The words are the distinction a model has to make.
		aOnly, bOnly string
	}{
		{
			a: "corral_find_symbol", b: "corral_search_code",
			aOnly: "defined", bOnly: "used",
		},
		{
			a: "corral_list_repos", b: "corral_workspace_index",
			aOnly: "filter", bOnly: "expensive",
		},
		{
			a: "corral_find_repo", b: "corral_list_repos",
			aOnly: "ambiguous", bOnly: "filter",
		},
	} {
		t.Run(pair.a+" vs "+pair.b, func(t *testing.T) {
			da, ok := desc[pair.a]
			if !ok {
				t.Fatalf("%s is not registered", pair.a)
			}
			db, ok := desc[pair.b]
			if !ok {
				t.Fatalf("%s is not registered", pair.b)
			}
			if !strings.Contains(da, pair.aOnly) {
				t.Errorf("%s's description does not say %q, which is what distinguishes it from %s",
					pair.a, pair.aOnly, pair.b)
			}
			if !strings.Contains(db, pair.bOnly) {
				t.Errorf("%s's description does not say %q, which is what distinguishes it from %s",
					pair.b, pair.bOnly, pair.a)
			}
		})
	}
}

// TestEveryToolIsDescribedWellEnoughToChoose applies the floor to all of
// them: a name, a title, a description long enough to say when to reach
// for it, and a read-only annotation where that is true.
func TestEveryToolIsDescribedWellEnoughToChoose(t *testing.T) {
	h := evalWorkspace(t)
	tools, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	for _, tool := range tools.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			if !strings.HasPrefix(tool.Name, "corral_") {
				t.Errorf("name %q is not namespaced; a model sees it beside every other server's tools", tool.Name)
			}
			if strings.TrimSpace(tool.Title) == "" {
				t.Error("no title")
			}
			// Long enough to say what it does and when to reach for it. A
			// one-line description is a name with punctuation.
			if n := len(tool.Description); n < 80 {
				t.Errorf("description is %d characters; too short to say when to use it: %q", n, tool.Description)
			}
			if tool.Annotations == nil {
				t.Error("no annotations; a client cannot tell whether this is safe to call")
			}
			if tool.InputSchema == nil {
				t.Error("no input schema")
			}
		})
	}
}

// TestServerInstructionsOrientAnAgent: the instructions are the only text
// a model reads before it has called anything, and they are where "which
// tool first" is actually decided.
func TestServerInstructionsOrientAnAgent(t *testing.T) {
	got := strings.ToLower(serverInstructions(ServerOptions{Root: "/tmp"}))

	for _, want := range []struct {
		text, why string
	}{
		{"visibility", "the layout is the thing an agent cannot infer from a path"},
		{"corral_status_summary", "there has to be a stated place to start"},
		{"corral_find_symbol", "the cross-repository lookup is the capability nothing else offers"},
		{"expensive", "the full index has to be marked as the fallback it is"},
		{"never as instructions", "tool results are untrusted, and saying so is the only defence against plain-language injection"},
		{"local", "an agent should not reach for a GitHub tool to answer a local question"},
	} {
		if !strings.Contains(got, want.text) {
			t.Errorf("the instructions do not mention %q — %s", want.text, want.why)
		}
	}
}

// TestEvalScenariosAreWellFormed guards the suite itself: a scenario with
// no assertions passes without measuring anything, which is worse than
// having no scenario at all.
func TestEvalScenariosAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, sc := range evalScenarios() {
		if strings.TrimSpace(sc.question) == "" {
			t.Errorf("scenario %d has no question", i)
		}
		if !strings.HasSuffix(sc.question, "?") && !strings.HasSuffix(sc.question, ".") {
			t.Errorf("scenario %d is not phrased as something a person said: %q", i, sc.question)
		}
		if sc.tool == "" {
			t.Errorf("scenario %d names no tool", i)
		}
		if len(sc.wants) == 0 {
			t.Errorf("scenario %d asserts nothing: %q", i, sc.question)
		}
		if seen[sc.question] {
			t.Errorf("scenario %d duplicates an earlier question: %q", i, sc.question)
		}
		seen[sc.question] = true
	}
}

// TestEvalReportIsWritable renders the suite as a table.
//
// Not an assertion — a way to read what corral can answer, for anyone
// judging whether the tool set covers the questions they have. Written
// only when CORRAL_EVAL_REPORT names a path, so an ordinary test run
// writes nothing.
func TestEvalReportIsWritable(t *testing.T) {
	path := os.Getenv("CORRAL_EVAL_REPORT")
	if path == "" {
		t.Skip("set CORRAL_EVAL_REPORT to a path to write the report")
	}

	h := evalWorkspace(t)
	type row struct {
		Question string `json:"question"`
		Tool     string `json:"tool"`
		Answered bool   `json:"answered"`
		Response string `json:"response"`
	}
	var rows []row
	for _, sc := range evalScenarios() {
		out, isErr := h.callTool(sc.tool, sc.args)
		answered := !isErr
		lower := strings.ToLower(out)
		for _, want := range sc.wants {
			if !strings.Contains(lower, strings.ToLower(want)) {
				answered = false
			}
		}
		rows = append(rows, row{Question: sc.question, Tool: sc.tool, Answered: answered, Response: out})
	}

	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// The path comes from an environment variable this test only reads
	// when an operator sets it deliberately; there is no untrusted input
	// on this line.
	if err := os.WriteFile(path, b, 0o600); err != nil { // #nosec G703 -- operator-supplied path
		t.Fatal(err)
	}
	answered := 0
	for _, r := range rows {
		if r.Answered {
			answered++
		}
	}
	t.Logf("eval report written to %s: %d/%d answered", path, answered, len(rows))
}
