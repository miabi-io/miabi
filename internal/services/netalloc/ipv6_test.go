// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package netalloc

import "testing"

func TestDeriveIPv6(t *testing.T) {
	const ula = "fd42:6d69:6162::/48"

	cases := map[string]string{
		"10.64.5.0/24":   "fd42:6d69:6162:4005::/64",
		"10.64.0.0/24":   "fd42:6d69:6162:4000::/64",
		"10.79.255.0/24": "fd42:6d69:6162:4fff::/64",
		"172.20.3.0/24":  "fd42:6d69:6162:1403::/64",
	}
	for v4, want := range cases {
		got, err := DeriveIPv6(ula, v4)
		if err != nil {
			t.Fatalf("%s: %v", v4, err)
		}
		if got != want {
			t.Errorf("DeriveIPv6(%s) = %s, want %s", v4, got, want)
		}
	}
}

// Every subnet of the default pool must map to a distinct /64, or two networks would be handed the
// same v6 range and the second create would fail.
func TestDeriveIPv6IsUniqueAcrossTheDefaultPool(t *testing.T) {
	const ula = "fd42:6d69:6162::/48"
	seen := map[string]string{}
	// 10.64.0.0/12 carved into /24s: second octet 64-79, third octet 0-255.
	for b := 64; b < 80; b++ {
		for c := 0; c < 256; c++ {
			v4 := netPrefix(b, c)
			got, err := DeriveIPv6(ula, v4)
			if err != nil {
				t.Fatalf("%s: %v", v4, err)
			}
			if prev, dup := seen[got]; dup {
				t.Fatalf("%s and %s both map to %s", prev, v4, got)
			}
			seen[got] = v4
		}
	}
	if len(seen) != 4096 {
		t.Fatalf("mapped %d subnets, want 4096 (the pool's size)", len(seen))
	}
}

func netPrefix(b, c int) string {
	return "10." + itoa(b) + "." + itoa(c) + ".0/24"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// An empty prefix means "let the daemon assign a ULA", not an error.
func TestDeriveIPv6EmptyPrefixMeansAuto(t *testing.T) {
	got, err := DeriveIPv6("", "10.64.5.0/24")
	if err != nil || got != "" {
		t.Fatalf("got (%q, %v), want an empty subnet and no error", got, err)
	}
}

func TestDeriveIPv6RejectsBadInput(t *testing.T) {
	cases := []struct{ name, ula, v4 string }{
		{"not a prefix", "nonsense", "10.64.5.0/24"},
		{"v4 prefix as ULA", "10.0.0.0/8", "10.64.5.0/24"},
		// Longer than /48 leaves no room for the 16-bit subnet id.
		{"prefix too long", "fd42:6d69:6162:1::/64", "10.64.5.0/24"},
		{"bad subnet", "fd42:6d69:6162::/48", "not-a-subnet"},
		{"v6 given as the v4 subnet", "fd42:6d69:6162::/48", "fd00::/64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DeriveIPv6(c.ula, c.v4); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
