// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"slices"
	"testing"
)

func TestTrustedProxiesParsing(t *testing.T) {
	t.Setenv("MIABI_TRUSTED_PROXIES", " 10.62.0.0/16, ,192.0.2.7,")
	if got, want := New().TrustedProxies, []string{"10.62.0.0/16", "192.0.2.7"}; !slices.Equal(got, want) {
		t.Errorf("TrustedProxies = %v, want %v", got, want)
	}
	t.Setenv("MIABI_TRUSTED_PROXIES", "")
	if got := New().TrustedProxies; len(got) != 0 {
		t.Errorf("unset TrustedProxies = %v, want empty", got)
	}
}
