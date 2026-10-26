// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package tui provides a Bubble Tea terminal user interface for Corral.
package tui

// Version is the build version of Corral, rendered in the TUI footer.
// Injected at build time alongside cmd.Version via -ldflags "-X …=Version";
// the "dev" fallback makes an un-injected build obvious. It is the build's
// value only: callers pass a different version to RunSelector instead of
// writing here.
var Version = "dev"
