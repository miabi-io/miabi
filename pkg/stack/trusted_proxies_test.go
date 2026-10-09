// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"slices"
	"strings"
	"testing"
)

func gatewayEnv(m *Manifest) []string {
	return gatewaySpec(m, ContainerGateway, m.Images.Gateway).Env
}

// Unset, the control plane believes X-Forwarded-For from any peer. The private network is the only one it
// sits on, so that is exactly the set of peers it should believe.
func TestTheControlPlaneTrustsOnlyThePrivateNetwork(t *testing.T) {
	m := testManifest()
	want := envTrustedProxies + "=" + DefaultInternalSubnet
	if !slices.Contains(controlPlaneEnv(m), want) {
		t.Errorf("control plane env lacks %s", want)
	}
}

func TestTheControlPlaneTrustFollowsTheInternalNetwork(t *testing.T) {
	yes := true
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	m.InternalNetwork = NetworkConfig{Subnet: "10.99.0.0/16", IPv6: &yes}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if got := m.Install.ServerTrustedProxies; !slices.Equal(got, []string{"10.99.0.0/16", "fc00::/7"}) {
		t.Errorf("server.trustedProxies = %q, want the private subnet plus the ULA range Docker allocates IPv6 from", got)
	}
}

// The default is written into the document, so the operator can see what the control plane trusts.
func TestTheServerDefaultIsWrittenBackToTheDocument(t *testing.T) {
	m := testManifest()
	if _, ok := m.Env[envTrustedProxies]; ok {
		t.Errorf("%s was seeded into server.env; the document has a field for it", envTrustedProxies)
	}
	if got := NewDocument(m).Spec.Server.TrustedProxies; !slices.Equal(got, []string{DefaultInternalSubnet}) {
		t.Errorf("document server.trustedProxies = %q, want [%s]", got, DefaultInternalSubnet)
	}
}

func TestServerTrustedProxiesReachTheControlPlane(t *testing.T) {
	m := testManifest()
	m.Install.ServerTrustedProxies = []string{"10.62.0.0/16", " 192.168.10.0/24 "}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(controlPlaneEnv(m), envTrustedProxies+"=10.62.0.0/16,192.168.10.0/24") {
		t.Errorf("control plane env lacks the stated list (got %v)", controlPlaneEnv(m))
	}
}

// An install that already set it through server.env keeps its value, moved into the field.
func TestAServerEnvValueMovesIntoTheField(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	m.Env = map[string]string{envTrustedProxies: "192.168.10.0/24,10.0.0.1"}
	if err := m.Normalize(); err != nil {
		t.Fatalf("refused an operator's own %s: %v", envTrustedProxies, err)
	}
	if got := m.Install.ServerTrustedProxies; !slices.Equal(got, []string{"192.168.10.0/24", "10.0.0.1"}) {
		t.Errorf("server.trustedProxies = %q, want the operator's value", got)
	}
	if _, ok := m.Env[envTrustedProxies]; ok {
		t.Error("the value stayed in server.env as well, so it would have two homes")
	}
}

func TestServerTrustedProxiesHasOneHome(t *testing.T) {
	m := testManifest()
	m.Env[envTrustedProxies] = "192.168.10.0/24"
	m.Install.ServerTrustedProxies = []string{"10.0.0.0/8"}
	if err := m.Normalize(); err == nil {
		t.Error("server.env and server.trustedProxies were both accepted")
	}
}

func TestServerTrustedProxiesAreValidated(t *testing.T) {
	m := testManifest()
	m.Install.ServerTrustedProxies = []string{"0.0.0.0/0"}
	err := m.Normalize()
	if err == nil || !strings.Contains(err.Error(), "server.trustedProxies") {
		t.Errorf("err = %v, want a refusal naming server.trustedProxies", err)
	}
}

// The flat schema has no field, so it keeps the default in env:, and an operator value there wins.
func TestAFlatManifestKeepsTheDefaultInEnv(t *testing.T) {
	m := Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	m.kinded = false
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if got := m.Env[envTrustedProxies]; got != DefaultInternalSubnet {
		t.Errorf("env %s = %q, want %s", envTrustedProxies, got, DefaultInternalSubnet)
	}

	m = Defaults("miabi/miabi:1.4.0")
	m.Domain = "miabi.example.com"
	m.kinded = false
	m.Env = map[string]string{envTrustedProxies: "192.168.10.0/24"}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(controlPlaneEnv(m), envTrustedProxies+"=192.168.10.0/24") {
		t.Error("the seeded default replaced the operator's value")
	}
}

func TestGatewayProxyModeIsOffUnlessAsked(t *testing.T) {
	env := gatewayEnv(testManifest())
	for _, key := range []string{"GOMA_PROXY_ENABLED", "GOMA_PROXY_TRUSTED_PROXIES"} {
		if hasKey(env, key) {
			t.Errorf("%s reached the gateway from a manifest that never set gateway.trustedProxies", key)
		}
	}
}

func TestGatewayTrustedProxiesReachTheGateway(t *testing.T) {
	m := testManifest()
	m.Install.GatewayTrustedProxies = []string{" 173.245.48.0/20", "", "203.0.113.7", "2400:cb00::/32 "}
	if err := m.Normalize(); err != nil {
		t.Fatal(err)
	}
	env := gatewayEnv(m)
	for _, want := range []string{
		"GOMA_PROXY_ENABLED=true",
		"GOMA_PROXY_TRUSTED_PROXIES=173.245.48.0/20,203.0.113.7,2400:cb00::/32",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("gateway env lacks %s (got %v)", want, env)
		}
	}
}

func TestGatewayTrustedProxiesAreValidated(t *testing.T) {
	for name, entry := range map[string]string{
		"a hostname":       "lb.example.com",
		"a broken CIDR":    "10.0.0.0/33",
		"every IPv4":       "0.0.0.0/0",
		"every IPv6":       "::/0",
		"a stray sentence": "cloudflare ranges",
	} {
		t.Run(name, func(t *testing.T) {
			m := testManifest()
			m.Install.GatewayTrustedProxies = []string{"10.0.0.0/8", entry}
			err := m.Normalize()
			if err == nil {
				t.Fatalf("accepted %q", entry)
			}
			if !strings.Contains(err.Error(), "gateway.trustedProxies") {
				t.Errorf("error = %q, want it to name the field", err)
			}
		})
	}
}

// Stated in the spec, the variables belong to it: a raw gateway.env value would silently contradict it.
func TestASpecTrustedProxiesListIsRefusedInGatewayEnv(t *testing.T) {
	m := testManifest()
	m.Install.GatewayTrustedProxies = []string{"10.0.0.0/8"}
	m.Gateway.Env["GOMA_PROXY_TRUSTED_PROXIES"] = "192.168.0.0/16"
	if err := m.Normalize(); err == nil {
		t.Error("gateway.env was allowed to contradict gateway.trustedProxies")
	}
}

func TestRawGomaProxyEnvStaysAvailableWhileTheSpecIsSilent(t *testing.T) {
	m := testManifest()
	m.Gateway.Env["GOMA_PROXY_IP_HEADERS"] = "CF-Connecting-IP,X-Forwarded-For"
	m.Gateway.Env["GOMA_PROXY_TRUSTED_PROXIES"] = "10.0.0.0/8"
	if err := m.Normalize(); err != nil {
		t.Fatalf("refused a gateway variable the spec does not claim: %v", err)
	}
}

func TestChangingGatewayTrustedProxiesRecreatesTheGateway(t *testing.T) {
	m := testManifest()
	before := specHash(gatewaySpec(m, ContainerGateway, m.Images.Gateway))
	m.Install.GatewayTrustedProxies = []string{"10.0.0.0/8"}
	if specHash(gatewaySpec(m, ContainerGateway, m.Images.Gateway)) == before {
		t.Error("the gateway spec hash ignored trustedProxies, so converge would leave the old gateway running")
	}
}
