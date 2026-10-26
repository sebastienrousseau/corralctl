// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"encoding/json"
	"testing"
)

// TestCapabilitiesAdvertiseOnlyWhatIsServed: the server never emits
// notifications/message, so advertising "logging" promised a feature it
// does not have. The SDK adds it by default; Logging is also Deprecated
// in protocol 2026-07-28 (SEP-2577). Tools, resources and prompts are
// still inferred from what is registered and must survive the override.
func TestCapabilitiesAdvertiseOnlyWhatIsServed(t *testing.T) {
	h := newHarness(t, ServerOptions{Root: t.TempDir()})
	caps := h.session.InitializeResult().Capabilities
	if caps == nil {
		t.Fatal("initialize result carries no capabilities")
	}
	// Checked on the wire form: the Logging field itself is deprecated in
	// the SDK, and what a client reads is the JSON.
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if v, ok := wire["logging"]; ok {
		t.Errorf("logging capability advertised: %s", v)
	}
	if caps.Tools == nil || !caps.Tools.ListChanged {
		t.Errorf("tools capability = %+v, want listChanged", caps.Tools)
	}
	if caps.Resources == nil || !caps.Resources.ListChanged {
		t.Errorf("resources capability = %+v, want listChanged", caps.Resources)
	}
	if caps.Prompts == nil || !caps.Prompts.ListChanged {
		t.Errorf("prompts capability = %+v, want listChanged", caps.Prompts)
	}
}
