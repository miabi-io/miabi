// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package saferollout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

// fakeEngine is the smallest Docker that can express the failures that matter: an
// image that will not boot, and a promotion that will not come up.
type fakeEngine struct {
	docker.Client // embedded: every method we do not need panics loudly if reached

	// running maps container name -> image.
	running map[string]string
	// health maps container name -> the health Inspect reports.
	health map[string]string
	// dies is the set of images whose containers immediately exit.
	dies map[string]bool
	// unhealthy is the set of images whose containers RUN but never pass their
	// healthcheck — a gateway that boots with a broken config and serves nothing.
	unhealthy map[string]bool
	// unstartable is the set of images RunContainer refuses outright.
	unstartable map[string]bool
	// pullFails simulates an unreachable registry (air-gapped, or a locally built
	// image that was never pushed); localImages is what is already on the host.
	pullFails   bool
	localImages map[string]bool

	pulled  []string
	created []string
	removed []string
}

func newFake() *fakeEngine {
	return &fakeEngine{
		running:     map[string]string{},
		health:      map[string]string{},
		dies:        map[string]bool{},
		unhealthy:   map[string]bool{},
		unstartable: map[string]bool{},
		localImages: map[string]bool{},
	}
}

func (f *fakeEngine) ImageExists(_ context.Context, ref string) (bool, error) {
	return f.localImages[ref], nil
}

func (f *fakeEngine) PullImage(_ context.Context, ref string, _ *docker.RegistryAuth) error {
	if f.pullFails {
		return errors.New("dial tcp: registry unreachable")
	}
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *fakeEngine) RunContainer(_ context.Context, spec docker.RunSpec) (string, error) {
	if f.unstartable[spec.Image] {
		return "", errors.New("no such image")
	}
	f.created = append(f.created, spec.Name+"="+spec.Image)
	f.running[spec.Name] = spec.Image
	switch {
	case f.dies[spec.Image]:
		f.health[spec.Name] = "exited"
	case f.unhealthy[spec.Image]:
		f.health[spec.Name] = "unhealthy"
	default:
		f.health[spec.Name] = "healthy"
	}
	return spec.Name, nil
}

func (f *fakeEngine) RemoveContainer(_ context.Context, name string, _ bool) error {
	f.removed = append(f.removed, name)
	delete(f.running, name)
	delete(f.health, name)
	return nil
}

func (f *fakeEngine) InspectContainer(_ context.Context, name string) (docker.Container, error) {
	img, ok := f.running[name]
	if !ok {
		return docker.Container{}, docker.ErrNotFound
	}
	switch f.health[name] {
	case "exited":
		return docker.Container{Names: []string{"/" + name}, Image: img, State: "exited", Status: "Exited (1)"}, nil
	case "unhealthy":
		// Running, but its healthcheck never passes: it boots and serves nothing.
		return docker.Container{Names: []string{"/" + name}, Image: img, State: "running",
			Health: "unhealthy", Status: "Up 30 seconds (unhealthy)"}, nil
	}
	return docker.Container{Names: []string{"/" + name}, Image: img, State: "running", Health: "healthy"}, nil
}

func (f *fakeEngine) InspectContainerConfig(_ context.Context, name string) (docker.ContainerConfig, error) {
	img, ok := f.running[name]
	if !ok {
		return docker.ContainerConfig{}, docker.ErrNotFound
	}
	return docker.ContainerConfig{Name: name, Image: img}, nil
}

func spec(name string, f *fakeEngine, image string, test, rollback bool) Spec {
	return Spec{
		Name:  name,
		Image: image,
		Build: func(n, img string) docker.RunSpec {
			return docker.RunSpec{Name: n, Image: img, Labels: map[string]string{}}
		},
		Test:     test,
		Observe:  10 * time.Millisecond,
		Rollback: rollback,
	}
}

func TestHappyPathPromotesTheNewImage(t *testing.T) {
	f := newFake()
	f.running["gw"] = "goma:v1"
	f.health["gw"] = "healthy"

	if err := Run(context.Background(), f, spec("gw", f, "goma:v2", true, true)); err != nil {
		t.Fatalf("rollout: %v", err)
	}
	if f.running["gw"] != "goma:v2" {
		t.Errorf("live container runs %q, want goma:v2", f.running["gw"])
	}
	if _, ok := f.running["gw-test"]; ok {
		t.Error("the test container was left behind")
	}
}

// A bad image is the case the test container exists for. The live container must not
// be touched at all — not stopped, not removed.
func TestABadImageNeverTouchesTheLiveContainer(t *testing.T) {
	f := newFake()
	f.running["gw"] = "goma:v1"
	f.health["gw"] = "healthy"
	f.dies["goma:bad"] = true

	err := Run(context.Background(), f, spec("gw", f, "goma:bad", true, true))
	if err == nil {
		t.Fatal("a crashing image was accepted")
	}
	if !strings.Contains(err.Error(), "did not stay up") {
		t.Errorf("unhelpful error: %v", err)
	}
	if f.running["gw"] != "goma:v1" {
		t.Fatalf("the live container is now %q — it should never have been touched", f.running["gw"])
	}
	for _, r := range f.removed {
		if r == "gw" {
			t.Fatal("the live container was removed despite the test failing")
		}
	}
}

// The case edgegateway.SafeUpdate could not survive: the test passed (or was skipped)
// but the promoted container will not come up. Without a rollback the component is
// simply gone — which for the control plane means the panel is down and nothing can
// bring it back but a human with a shell.
func TestAFailedPromotionRollsBackToThePreviousImage(t *testing.T) {
	f := newFake()
	f.running["miabi"] = "miabi:v1"
	f.health["miabi"] = "healthy"
	// v2 starts but immediately exits — a bad migration, a bad config, a panic on boot.
	f.dies["miabi:v2"] = true

	// Test:false, as the control plane runs (a second one would migrate the live DB).
	err := Run(context.Background(), f, spec("miabi", f, "miabi:v2", false, true))
	if err == nil {
		t.Fatal("a control plane that exits on boot was accepted")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("the error does not say a rollback happened: %v", err)
	}
	if got := f.running["miabi"]; got != "miabi:v1" {
		t.Fatalf("after a failed update the container runs %q, want the previous miabi:v1 "+
			"(the panel would otherwise be DOWN)", got)
	}
}

// The test container exists to catch a bad image BEFORE it takes the live one's
// ports. "Still running" is a weak claim: a gateway with a broken config boots, stays
// up, and serves nothing. Where the container reports health, the test phase must
// demand it — otherwise the failure surfaces only after promotion, when the live
// gateway is already gone.
func TestAnUnhealthyTestContainerNeverGetsPromoted(t *testing.T) {
	f := newFake()
	f.running["gw"] = "goma:v1"
	f.health["gw"] = "healthy"
	// v2 starts and STAYS RUNNING, but never becomes healthy: it boots and serves nothing.
	f.unhealthy["goma:v2"] = true

	err := Run(context.Background(), f, spec("gw", f, "goma:v2", true, true))
	if err == nil {
		t.Fatal("an image that runs but never serves was promoted over the live gateway")
	}
	if !strings.Contains(err.Error(), "never became healthy") {
		t.Errorf("unhelpful error: %v", err)
	}
	if f.running["gw"] != "goma:v1" {
		t.Fatalf("the live container is now %q — it should never have been touched", f.running["gw"])
	}
}

// Rollback:false is honored — some callers would rather fail loudly than resurrect an
// old image over state the new one already changed.
func TestRollbackCanBeDeclined(t *testing.T) {
	f := newFake()
	f.running["x"] = "app:v1"
	f.health["x"] = "healthy"
	f.dies["app:v2"] = true

	err := Run(context.Background(), f, spec("x", f, "app:v2", false, false))
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "rolled back") {
		t.Error("rolled back despite Rollback:false")
	}
}

// When the rollback ITSELF fails, the operator is in the worst position: nothing is
// running. The error has to say so in those words, because it is the difference
// between "retry later" and "get up now".
func TestAFailedRollbackSaysTheComponentIsDown(t *testing.T) {
	f := newFake()
	f.running["miabi"] = "miabi:v1"
	f.health["miabi"] = "healthy"
	f.dies["miabi:v2"] = true
	f.unstartable["miabi:v1"] = true // the old image is gone from the host too

	err := Run(context.Background(), f, spec("miabi", f, "miabi:v2", false, true))
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), "DOWN") || !strings.Contains(err.Error(), "manual recovery") {
		t.Errorf("the error does not tell the operator the component is down: %v", err)
	}
}

// A first install has nothing to roll back to. That must not be reported as a
// rollback failure — there was simply no previous image.
func TestNoPreviousImageIsNotARollbackFailure(t *testing.T) {
	f := newFake()
	f.dies["app:v1"] = true

	err := Run(context.Background(), f, spec("app", f, "app:v1", false, true))
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "DOWN") || strings.Contains(err.Error(), "rolled back") {
		t.Errorf("a first install reported a rollback: %v", err)
	}
}

// A hard pull breaks the two cases most likely at install time: an air-gapped host,
// and an operator running a locally built or `docker load`ed image. Docker's own
// `docker run` uses what is on the host; so must we. (Caught by running install.sh
// against a locally built miabi/miabi, which failed with "not found".)
func TestEnsureImageFallsBackToTheLocalCopy(t *testing.T) {
	t.Run("registry unreachable but image is local", func(t *testing.T) {
		f := newFake()
		f.pullFails = true
		f.localImages = map[string]bool{"miabi/miabi:9.9.9-test": true}

		if err := EnsureImage(context.Background(), f, "miabi/miabi:9.9.9-test", nil, nil); err != nil {
			t.Errorf("refused an image that is already on the host: %v", err)
		}
	})

	t.Run("registry unreachable and image is absent", func(t *testing.T) {
		f := newFake()
		f.pullFails = true

		err := EnsureImage(context.Background(), f, "miabi/miabi:nope", nil, nil)
		if err == nil {
			t.Fatal("accepted an image that exists nowhere")
		}
		if !strings.Contains(err.Error(), "pull") {
			t.Errorf("the error hides the cause: %v", err)
		}
	})

	t.Run("a working pull is still preferred", func(t *testing.T) {
		f := newFake()
		if err := EnsureImage(context.Background(), f, "miabi/miabi:1.4.0", nil, nil); err != nil {
			t.Fatal(err)
		}
		if len(f.pulled) != 1 || f.pulled[0] != "miabi/miabi:1.4.0" {
			t.Errorf("did not pull when it could: %v", f.pulled)
		}
	})
}

func TestWaitHealthyRejectsAnExitedContainer(t *testing.T) {
	f := newFake()
	f.running["x"] = "app:v1"
	f.health["x"] = "exited"

	err := WaitHealthy(context.Background(), f, "x", time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("WaitHealthy accepted an exited container: %v", err)
	}
}
