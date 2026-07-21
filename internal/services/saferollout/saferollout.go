// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package saferollout replaces a running container with a new image without a
// blind cutover.
//
// It generalizes the pattern edgegateway has used for node gateways — start the
// new image under a throwaway name, watch it, only then promote it — and adds the
// half that was missing: a rollback. edgegateway.SafeUpdate removed the live
// container *before* starting its replacement, so a promotion that failed left
// nothing running. That is survivable for one node's gateway; it is not survivable
// for the control plane or the platform database, which is what this package now
// rolls out too.
package saferollout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
)

// Phases reported through OnPhase, in order. "failed" is terminal and carries the
// cause; "rolled-back" means the previous image is running again.
const (
	PhasePulling     = "pulling"
	PhaseTesting     = "testing"
	PhaseObserving   = "observing"
	PhasePromoting   = "promoting"
	PhaseVerifying   = "verifying"
	PhaseRollingBack = "rolling-back"
	PhaseRolledBack  = "rolled-back"
	PhaseFailed      = "failed"
	PhaseDone        = "done"
)

// DefaultObserve is how long a test container must stay up before its image is
// trusted. A bad image or config almost always exits on boot, so simply still
// being alive after the window is a strong signal.
const DefaultObserve = 25 * time.Second

// DefaultHealthWait bounds how long the promoted container may take to become
// healthy (or, with no healthcheck, to stay running).
const DefaultHealthWait = 90 * time.Second

// Spec describes one rollout.
type Spec struct {
	// Name is the live container. Image is the new image, already resolved.
	Name  string
	Image string

	// Build returns the run spec for the container. It is called with the name to
	// create and the image to run, so the caller can vary the spec between the test
	// container and the live one — chiefly by NOT publishing ports on the test
	// container, since the live one still holds them.
	Build func(name, image string) docker.RunSpec

	// Test runs the new image under Name+"-test" first and watches it for Observe
	// before promoting. Skip it for containers that cannot safely run a second copy
	// against the same state — a second control plane would run migrations against
	// the live database, and a second Postgres cannot open the same data directory.
	// Those rely on Rollback instead.
	Test    bool
	Observe time.Duration

	// HealthWait bounds the post-promotion check. Zero uses DefaultHealthWait.
	HealthWait time.Duration

	// Rollback restores the previous image if the promoted container never becomes
	// healthy. Almost always wanted. The exception is a container whose new image has
	// already made a one-way change to shared state — see the migration caveat on
	// Run.
	Rollback bool

	// OnPhase is called as each phase begins, and once with PhaseFailed on error.
	OnPhase func(phase string, cause error)
}

// TestName is the throwaway container a Test rollout starts.
func (s Spec) TestName() string { return s.Name + "-test" }

// Run performs the rollout. It is not atomic and does not pretend to be: the
// window between removing the old container and the new one becoming healthy is
// real downtime for that component. What it guarantees is that the window is short
// and that it ends with *something* running — the new image if it works, the old
// one if it does not.
//
// Migration caveat: rolling an image back does NOT roll back a schema migration
// the new image already applied. For the control plane that means a rollback
// leaves an older binary against a newer schema. Miabi's migrations are additive
// (GORM AutoMigrate plus forward-only upgrade steps), so this is survivable, but
// it is the reason a control-plane rollback is a recovery mechanism and not an
// "undo".
func Run(ctx context.Context, dc docker.Client, sp Spec) error {
	if sp.Name == "" || sp.Image == "" || sp.Build == nil {
		return errors.New("saferollout: Name, Image and Build are required")
	}
	phase := sp.OnPhase
	if phase == nil {
		phase = func(string, error) {}
	}
	observe := sp.Observe
	if observe <= 0 {
		observe = DefaultObserve
	}
	healthWait := sp.HealthWait
	if healthWait <= 0 {
		healthWait = DefaultHealthWait
	}

	fail := func(err error) error {
		// context.Background(): the cleanup must still run when ctx is what died.
		_ = dc.RemoveContainer(context.Background(), sp.TestName(), true)
		phase(PhaseFailed, err)
		return err
	}

	phase(PhasePulling, nil)
	if err := EnsureImage(ctx, dc, sp.Image, nil, nil); err != nil {
		return fail(err)
	}

	if sp.Test {
		phase(PhaseTesting, nil)
		_ = dc.RemoveContainer(ctx, sp.TestName(), true) // a leftover from a killed run
		if _, err := dc.RunContainer(ctx, sp.Build(sp.TestName(), sp.Image)); err != nil {
			return fail(fmt.Errorf("start test container: %w", err))
		}

		phase(PhaseObserving, nil)
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(observe):
		}
		c, err := dc.InspectContainer(ctx, sp.TestName())
		if err != nil {
			return fail(fmt.Errorf("inspect test container: %w", err))
		}
		if c.State != "running" {
			return fail(fmt.Errorf("the new image did not stay up (state %q, status %q) — %s left untouched",
				c.State, c.Status, sp.Name))
		}
		// "Still running" is a weak claim when the container can make a stronger one.
		// A gateway that boots but cannot serve — a bad config, a bad upstream — stays
		// happily "running", so a state-only check would wave it through and the failure
		// would surface only AFTER it had taken the live container's ports. Where there
		// is a healthcheck, demand it: that is the entire reason the test container runs.
		if c.Health != "" {
			if err := WaitHealthy(ctx, dc, sp.TestName(), healthWait); err != nil {
				return fail(fmt.Errorf("the new image never became healthy (%w) — %s left untouched",
					err, sp.Name))
			}
		}
		_ = dc.RemoveContainer(ctx, sp.TestName(), true)
	}

	// Remember what is running now, so a failed promotion has something to go back
	// to. Absent (first install, or a container that vanished) is not an error — it
	// just means there is nothing to roll back to.
	prevImage := currentImage(ctx, dc, sp.Name)

	phase(PhasePromoting, nil)
	_ = dc.RemoveContainer(ctx, sp.Name, true)
	if _, err := dc.RunContainer(ctx, sp.Build(sp.Name, sp.Image)); err != nil {
		return fail(rollback(ctx, dc, sp, prevImage, fmt.Errorf("start %s: %w", sp.Name, err), phase))
	}

	phase(PhaseVerifying, nil)
	if err := WaitHealthy(ctx, dc, sp.Name, healthWait); err != nil {
		return fail(rollback(ctx, dc, sp, prevImage, err, phase))
	}

	phase(PhaseDone, nil)
	return nil
}

// EnsureImage makes an image available, preferring a fresh pull but accepting one
// that is already on the host.
//
// A hard pull is wrong for the two cases that matter most at install time. An
// air-gapped host has no registry to reach; and an operator running a locally built
// or pre-loaded image (`docker load`, a private mirror, a release candidate) has the
// image sitting right there. Docker's own `docker run` uses what is local rather than
// insisting on a round trip, and refusing to do the same turned "install my image"
// into "not found" — which is exactly how this was caught, by running install.sh
// against a locally built miabi/miabi.
//
// Pull failures are therefore only fatal when the image is ALSO absent locally. The
// warning matters: silently running a stale local copy when the operator asked for a
// newer tag would be its own kind of lie.
// notify, when non-nil, renders the "using the local copy" notice. The CLI passes its
// own printer: a structured logger line in the middle of an indented install log reads
// like a crash, and this is a routine, expected outcome on an air-gapped or
// private-registry host.
func EnsureImage(ctx context.Context, dc docker.Client, ref string, auth *docker.RegistryAuth, notify func(string, ...any)) error {
	pullErr := dc.PullImage(ctx, ref, auth)
	if pullErr == nil {
		return nil
	}
	exists, err := dc.ImageExists(ctx, ref)
	if err != nil || !exists {
		return fmt.Errorf("pull %q: %w", ref, pullErr)
	}
	if notify != nil {
		notify("could not reach the registry — using the copy of %s already on this host", ref)
	} else {
		logger.Warn("could not pull the image; using the copy already on this host",
			"image", ref, "error", pullErr)
	}
	return nil
}

// rollback restores prevImage under the live name. It returns an error describing
// both what failed and what the rollback did, because "the update failed" and "the
// update failed and you now have nothing running" are very different pages to be
// woken up for.
func rollback(ctx context.Context, dc docker.Client, sp Spec, prevImage string, cause error, phase func(string, error)) error {
	if !sp.Rollback || prevImage == "" || prevImage == sp.Image {
		return cause
	}
	phase(PhaseRollingBack, cause)

	// Background context: ctx may already be cancelled (that can be why we are here),
	// and abandoning the rollback would leave the component down.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()

	_ = dc.RemoveContainer(bg, sp.Name, true)
	if _, err := dc.RunContainer(bg, sp.Build(sp.Name, prevImage)); err != nil {
		return fmt.Errorf("%w — AND the rollback to %s failed (%v); %s is now DOWN and needs manual recovery",
			cause, prevImage, err, sp.Name)
	}
	if err := WaitHealthy(bg, dc, sp.Name, DefaultHealthWait); err != nil {
		return fmt.Errorf("%w — the rollback to %s started but did not become healthy (%v); %s needs manual recovery",
			cause, prevImage, err, sp.Name)
	}
	phase(PhaseRolledBack, cause)
	return fmt.Errorf("%w — rolled back to %s, which is running", cause, prevImage)
}

// currentImage is the image of the named container, or "" when it does not exist.
func currentImage(ctx context.Context, dc docker.Client, name string) string {
	cfg, err := dc.InspectContainerConfig(ctx, name)
	if err != nil {
		return ""
	}
	return cfg.Image
}

// WaitHealthy blocks until the container is healthy, or until timeout.
//
// "Healthy" means whatever the container can prove. With a healthcheck, it is
// Docker's verdict. Without one, the best available signal is that it is still
// running — so require it to stay running for a settle period rather than accepting
// the instant after `docker run` returns, which a crash-looping container also
// passes.
func WaitHealthy(ctx context.Context, dc docker.Client, name string, timeout time.Duration) error {
	const (
		poll   = time.Second
		settle = 5 * time.Second // no healthcheck: how long "running" must hold
	)
	deadline := time.Now().Add(timeout)
	var runningSince time.Time

	for {
		c, err := dc.InspectContainer(ctx, name)
		if err != nil {
			return fmt.Errorf("%s did not come up: %w", name, err)
		}

		switch {
		case c.State == "exited" || c.State == "dead":
			return fmt.Errorf("%s exited (%s) instead of starting", name, c.Status)
		case c.Health == "healthy":
			return nil
		case c.Health == "unhealthy":
			return fmt.Errorf("%s is unhealthy (%s)", name, c.Status)
		case c.Health == "" && c.State == "running":
			// No healthcheck to consult. Insist it holds.
			if runningSince.IsZero() {
				runningSince = time.Now()
			}
			if time.Since(runningSince) >= settle {
				return nil
			}
		default:
			runningSince = time.Time{} // starting, restarting, paused: reset the settle clock
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not become healthy within %s (state %q, health %q)",
				name, timeout, c.State, c.Health)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
