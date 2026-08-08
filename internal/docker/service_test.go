// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"testing"

	"github.com/docker/docker/api/types/mount"
)

// TestBuildSwarmServiceSpecIngressAliases verifies the shared ingress overlay gets ONLY the unique
// upstream alias while the per-workspace overlay keeps tenant-scoped aliases. The workspace-scoped
// app name on the shared network would let app names collide across workspaces.
func TestBuildSwarmServiceSpecIngressAliases(t *testing.T) {
	spec := buildSwarmServiceSpec(ServiceSpec{
		Name:           "mb-app-abc-1",
		Image:          "nginx",
		Networks:       []string{"mb-ws-1-overlay"},
		NetworkAliases: []string{"mb-app-abc-1", "web"},
		IngressNetwork: "miabi-ingress",
		IngressAlias:   "mb-app-abc-1",
	})

	nets := spec.TaskTemplate.Networks
	if len(nets) != 2 {
		t.Fatalf("expected 2 network attachments, got %d", len(nets))
	}

	byTarget := map[string][]string{}
	for _, n := range nets {
		byTarget[n.Target] = n.Aliases
	}

	ws, ok := byTarget["mb-ws-1-overlay"]
	if !ok {
		t.Fatal("workspace overlay attachment missing")
	}
	if len(ws) != 2 || ws[0] != "mb-app-abc-1" || ws[1] != "web" {
		t.Fatalf("workspace overlay should carry both aliases, got %v", ws)
	}

	ing, ok := byTarget["miabi-ingress"]
	if !ok {
		t.Fatal("ingress overlay attachment missing")
	}
	if len(ing) != 1 || ing[0] != "mb-app-abc-1" {
		t.Fatalf("ingress overlay should carry only the unique alias, got %v", ing)
	}
}

// TestBuildSwarmServiceSpecMountDrivers verifies a shared-volume mount carries
// its driver config into the swarm spec (so every node materializes the real
// backing share), while a plain mount without a driver config does not.
func TestBuildSwarmServiceSpecMountDrivers(t *testing.T) {
	spec := buildSwarmServiceSpec(ServiceSpec{
		Name:   "svc",
		Image:  "nginx",
		Mounts: map[string]string{"mb-vol-1-share": "/data", "mb-vol-1-local": "/cache"},
		MountDrivers: map[string]ServiceMountDriver{
			"mb-vol-1-share": {Name: "local", Options: map[string]string{"type": "nfs", "device": ":/exports/app", "o": "addr=10.0.0.5,rw"}},
		},
	})

	byTarget := map[string]*mount.VolumeOptions{}
	for _, m := range spec.TaskTemplate.ContainerSpec.Mounts {
		byTarget[m.Target] = m.VolumeOptions
	}

	share := byTarget["/data"]
	if share == nil || share.DriverConfig == nil {
		t.Fatal("shared mount must carry a volume driver config")
	}
	if share.DriverConfig.Name != "local" || share.DriverConfig.Options["type"] != "nfs" {
		t.Fatalf("driver config = %+v, want local/nfs", share.DriverConfig)
	}
	if vo := byTarget["/cache"]; vo != nil {
		t.Fatalf("a mount without a driver config must not get VolumeOptions, got %+v", vo)
	}
}

// TestEncodeRegistryAuth verifies the swarm registry-auth encoder: nil/empty
// credentials yield an empty header (so public images don't get a bogus auth),
// and a real credential yields a non-empty encoded token that workers use to pull.
func TestEncodeRegistryAuth(t *testing.T) {
	if enc, err := encodeRegistryAuth(nil); err != nil || enc != "" {
		t.Fatalf("nil auth = (%q, %v), want (\"\", nil)", enc, err)
	}
	if enc, err := encodeRegistryAuth(&RegistryAuth{Server: "registry.example.com"}); err != nil || enc != "" {
		t.Fatalf("credential-less auth = (%q, %v), want (\"\", nil)", enc, err)
	}
	enc, err := encodeRegistryAuth(&RegistryAuth{Server: "registry.example.com", Username: "_miabi", Password: "tok"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if enc == "" {
		t.Fatal("a real credential must produce a non-empty encoded auth token")
	}
}

// TestBuildSwarmServiceSpecNoIngress confirms a spec without an ingress network
// attaches only its primary networks (unchanged behavior for non-cluster paths).
func TestBuildSwarmServiceSpecNoIngress(t *testing.T) {
	spec := buildSwarmServiceSpec(ServiceSpec{
		Name:     "svc",
		Image:    "nginx",
		Networks: []string{"mb-ws-1-overlay"},
	})
	if got := len(spec.TaskTemplate.Networks); got != 1 {
		t.Fatalf("expected 1 network attachment, got %d", got)
	}
}
