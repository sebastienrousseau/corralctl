// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import "testing"

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
	if caps.Logging != nil {
		t.Errorf("logging capability advertised: %+v", caps.Logging)
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
