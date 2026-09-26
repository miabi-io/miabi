// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package metadataguard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

func TestRulesetBlocksMetadataButNotDNS(t *testing.T) {
	r := Ruleset()
	for _, want := range []string{
		"table inet " + Table + " {}\ndelete table inet " + Table,
		"169.254.0.0/16", "168.63.129.16", "100.100.100.200", "fd00:ec2::254",
		"hook forward",
		`iifname "br-*" ip daddr @metadata4 counter drop`,
		`iifname "docker0" ip6 daddr @metadata6 counter drop`,
		`iifname "docker_gwbridge" ip daddr @metadata4 meta l4proto { tcp, udp } th dport 53 accept`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("ruleset is missing %q:\n%s", want, r)
		}
	}
	// The DNS exception must come before the drop, or it never matches.
	if strings.Index(r, `iifname "br-*" ip daddr @metadata4 meta l4proto`) > strings.Index(r, `iifname "br-*" ip daddr @metadata4 counter drop`) {
		t.Error("DNS accept is after the drop")
	}
	// Only traffic forwarded from container bridges: the host's own access (cloud agents,
	// the control plane) must be untouched.
	if strings.Contains(r, "hook input") || strings.Contains(r, "hook output") {
		t.Error("ruleset touches the host's own traffic")
	}
}

func TestSpecIsHostNetworkedAndMinimal(t *testing.T) {
	spec := NewService(nil, nil, true).spec()
	if spec.NetworkMode != "host" || spec.RestartPolicy != "always" {
		t.Fatalf("network=%q restart=%q", spec.NetworkMode, spec.RestartPolicy)
	}
	if strings.Join(spec.CapDrop, ",") != "ALL" || strings.Join(spec.CapAdd, ",") != "NET_ADMIN,SYS_CHROOT" {
		t.Fatalf("caps drop=%v add=%v", spec.CapDrop, spec.CapAdd)
	}
	if len(spec.Binds) != 1 || !spec.Binds[0].ReadOnly || spec.Binds[0].Source != "/" {
		t.Fatalf("binds = %+v", spec.Binds)
	}
	// Housekeeping treats anything with a role as platform infrastructure and never removes it.
	if spec.Labels[docker.LabelRole] != docker.RoleMetadataGuard || spec.Labels[docker.LabelSpecHash] == "" {
		t.Fatalf("labels = %v", spec.Labels)
	}
}

type fakeDocker struct {
	docker.Client
	current *docker.Container
	runs    int
	removes int
	oneShot []string
}

var errNoContainer = errors.New("no such container")

func (f *fakeDocker) InspectContainer(context.Context, string) (docker.Container, error) {
	if f.current == nil {
		return docker.Container{}, errNoContainer
	}
	return *f.current, nil
}
func (f *fakeDocker) ImageExists(context.Context, string) (bool, error) { return true, nil }
func (f *fakeDocker) RemoveContainer(context.Context, string, bool) error {
	f.removes++
	return nil
}
func (f *fakeDocker) RunContainer(_ context.Context, spec docker.RunSpec) (string, error) {
	f.runs++
	f.current = &docker.Container{Labels: spec.Labels, State: "running"}
	return "id", nil
}
func (f *fakeDocker) RunOneShot(_ context.Context, spec docker.RunSpec) (int, string, error) {
	f.oneShot = append(f.oneShot, strings.Join(spec.Cmd, " "))
	return 0, "", nil
}

func TestEnsureLeavesAMatchingGuardAlone(t *testing.T) {
	s := NewService(nil, nil, true)
	dc := &fakeDocker{}
	for i := 0; i < 3; i++ {
		if err := s.Ensure(context.Background(), dc, "edge"); err != nil {
			t.Fatal(err)
		}
	}
	if dc.runs != 1 {
		t.Fatalf("runs = %d, want 1: a running guard with the same spec must not be recreated", dc.runs)
	}

	dc.current.Labels = map[string]string{docker.LabelSpecHash: "older"}
	if err := s.Ensure(context.Background(), dc, "edge"); err != nil {
		t.Fatal(err)
	}
	if dc.runs != 2 {
		t.Fatalf("runs = %d, want 2: a changed spec must be redeployed", dc.runs)
	}
}

func TestDisabledRemovesGuardAndTable(t *testing.T) {
	s := NewService(nil, nil, false)
	dc := &fakeDocker{}
	if err := s.Ensure(context.Background(), dc, "edge"); err != nil {
		t.Fatal(err)
	}
	if dc.removes != 0 || len(dc.oneShot) != 0 {
		t.Fatal("a node without a guard must not be touched")
	}

	dc.current = &docker.Container{State: "running"}
	if err := s.Ensure(context.Background(), dc, "edge"); err != nil {
		t.Fatal(err)
	}
	if dc.removes != 1 || len(dc.oneShot) != 1 || !strings.Contains(dc.oneShot[0], "nft delete table inet "+Table) {
		t.Fatalf("removes=%d oneShot=%v", dc.removes, dc.oneShot)
	}
}
