// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests drive a real ClientSession rather than calling handlers.
//
// That distinction is the whole point here. The SDK validates a tool's output
// against its generated schema inside the AddTool wrapper, so a handler called
// directly never meets the schema at all — the rest of this package's tests
// would keep passing against an output that no client could accept.

// TestEveryToolDeclaresAnOutputSchema is the regression for the reported gap.
//
// Without an output schema a result is unverifiable: a client cannot tell a
// renamed field from a missing one, and a handler that quietly stops emitting
// something breaks nothing that anyone would notice.
//
// It asserts over every registered tool rather than a sample, because the
// failure mode is one tool being forgotten — which a sample is exactly the
// wrong instrument for.
func TestEveryToolDeclaresAnOutputSchema(t *testing.T) {
	h := newHarness(t, ServerOptions{
		Root:                       symbolWorkspace(t),
		EnableMutations:            true,
		EnableDestructiveMutations: true,
	})

	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered; this test would pass vacuously")
	}

	for _, tool := range res.Tools {
		if tool.OutputSchema == nil {
			t.Errorf("tool %q declares no outputSchema", tool.Name)
			continue
		}
		// The schema arrives as decoded JSON on the client side, so it is
		// inspected as JSON rather than as the server-side schema type.
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := remarshal(tool.OutputSchema, &schema); err != nil {
			t.Errorf("tool %q outputSchema is not readable: %v", tool.Name, err)
			continue
		}
		// An object at the root is what structuredContent requires.
		if schema.Type != "object" {
			t.Errorf("tool %q outputSchema root is %q, want \"object\"", tool.Name, schema.Type)
		}
		if len(schema.Properties) == 0 {
			t.Errorf("tool %q outputSchema declares no properties", tool.Name)
		}
	}
	t.Logf("%d tools, all with an output schema", len(res.Tools))
}

// TestToolResultCarriesStructuredContent checks the other half: a schema is
// only useful if results actually arrive in it.
func TestToolResultCarriesStructuredContent(t *testing.T) {
	h := newHarness(t, ServerOptions{Root: symbolWorkspace(t)})

	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "corral_status_summary",
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("status summary failed: %s", resultText(res))
	}
	if res.StructuredContent == nil {
		t.Fatal("no structuredContent in the result")
	}

	var got StatusSummaryOutput
	if err := remarshal(res.StructuredContent, &got); err != nil {
		t.Fatalf("structuredContent does not fit its own type: %v", err)
	}
	if got.Root == "" {
		t.Error("structuredContent is missing root")
	}
	if got.Total == 0 {
		t.Error("structuredContent reports no repositories in a populated workspace")
	}

	// The text block must agree with the structure. They are rendered from
	// one value precisely so they cannot disagree; this is what would catch a
	// future change that reintroduces a second source.
	var fromText StatusSummaryOutput
	if err := json.Unmarshal([]byte(resultText(res)), &fromText); err != nil {
		t.Fatalf("text content is not the same shape: %v", err)
	}
	if fromText.Total != got.Total || fromText.Root != got.Root {
		t.Errorf("text and structuredContent disagree: %+v vs %+v", fromText, got)
	}
}

// TestEmptyResultsSatisfyTheSchema is the case most likely to break and least
// likely to be noticed.
//
// A Go nil slice marshals to `null`, and `null` does not satisfy a schema that
// says `type: array`. Every one of these tools returns an empty collection on
// the ordinary "nothing matched" path, so if nil slices were left unguarded the
// failure would land on the most common call an agent makes — and the
// package's handler-level tests could not see it, because they never validate.
func TestEmptyResultsSatisfyTheSchema(t *testing.T) {
	h := newHarness(t, ServerOptions{Root: symbolWorkspace(t)})
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		params *mcp.CallToolParams
	}{
		{"search with no match", &mcp.CallToolParams{
			Name:      "corral_search_code",
			Arguments: map[string]any{"query": "zzz-no-such-token-zzz"},
		}},
		{"symbol with no match", &mcp.CallToolParams{
			Name:      "corral_find_symbol",
			Arguments: map[string]any{"name": "ZzzNoSuchSymbol"},
		}},
		{"list filtered to nothing", &mcp.CallToolParams{
			Name:      "corral_list_repos",
			Arguments: map[string]any{"language": "no-such-language"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := h.session.CallTool(ctx, tc.params)
			if err != nil {
				// A schema violation surfaces here, as a protocol error
				// from the SDK's output validation.
				t.Fatalf("call failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("empty result reported as an error: %s", resultText(res))
			}
			if res.StructuredContent == nil {
				t.Fatal("empty result carries no structuredContent")
			}
			// The collection must be present and empty, not absent and not
			// null: "no matches" is a fact a client should be able to read.
			var body map[string]json.RawMessage
			if err := remarshal(res.StructuredContent, &body); err != nil {
				t.Fatalf("structuredContent is not an object: %v", err)
			}
			for _, key := range []string{"hits", "symbols", "repos"} {
				if raw, ok := body[key]; ok && string(raw) == "null" {
					t.Errorf("%q is null; a schema saying type:array rejects that", key)
				}
			}
		})
	}
}

// TestMutationToolsDeclareSchemasToo guards the tools that are off by default.
//
// They are registered only when mutations are unlocked, so a test using the
// default options cannot see them — which is how a gap like this hides until
// someone enables the flag in production.
func TestMutationToolsDeclareSchemasToo(t *testing.T) {
	h := newHarness(t, ServerOptions{
		Root:                       symbolWorkspace(t),
		EnableMutations:            true,
		EnableDestructiveMutations: true,
	})

	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	seen := map[string]bool{}
	for _, tool := range res.Tools {
		seen[tool.Name] = tool.OutputSchema != nil
	}
	for _, name := range []string{"corral_sync_repo", "corral_clone_repo", "corral_delete_repo"} {
		declared, present := seen[name]
		if !present {
			t.Errorf("%s is not registered even with mutations enabled", name)
			continue
		}
		if !declared {
			t.Errorf("%s declares no outputSchema", name)
		}
	}
}

// remarshal moves a decoded-JSON value into a typed destination.
//
// Schemas and structured content reach a client as `any`, so asserting on them
// means going back through JSON rather than type-asserting a wire shape.
func remarshal(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}
