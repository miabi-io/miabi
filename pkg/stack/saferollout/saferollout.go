// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

// Package saferollout tests a new image under a throwaway name before promoting, with rollback on failure.
package saferollout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/pkg/stack/docker"
)

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

// A bad image almost always exits on boot, so surviving the window is a strong signal.
const DefaultObserve = 25 * time.Second

const DefaultHealthWait = 90 * time.Second

// Spec describes one rollout.
type Spec struct {
	// Name is the live container. Image is the new image, already resolved.
	Name  string
	Image string

	// Given the name so the test container can skip publishing ports the live one still holds.
	Build func(name, image string) docker.RunSpec

	// Skip for containers that can't run a second copy on shared state (control plane, Postgres).
	Test    bool
	Observe time.Duration

	// HealthWait bounds the post-promotion check. Zero uses DefaultHealthWait.
	HealthWait time.Duration

	// The exception is a new image that already made a one-way change to shared state.
	Rollback bool

	// OnPhase is called as each phase begins, and once with PhaseFailed on error.
	OnPhase func(phase string, cause error)
}

// TestName is the throwaway container a Test rollout starts.
func (s Spec) TestName() string { return s.Name + "-test" }

// Not atomic: there is a short downtime window; rolling back an image does not roll back a migration.
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
		// A gateway can run without serving, so demand the healthcheck before it takes the live ports.
		if c.Health != "" {
			if err := WaitHealthy(ctx, dc, sp.TestName(), healthWait); err != nil {
				return fail(fmt.Errorf("the new image never became healthy (%w) — %s left untouched",
					err, sp.Name))
			}
		}
		_ = dc.RemoveContainer(ctx, sp.TestName(), true)
	}

	// An absent container just means there is nothing to roll back to.
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

// Accepts a local image so air-gapped and pre-loaded installs work, like `docker run`.
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

// The error says what the rollback did, since "failed" and "failed with nothing running" differ greatly.
func rollback(ctx context.Context, dc docker.Client, sp Spec, prevImage string, cause error, phase func(string, error)) error {
	if !sp.Rollback || prevImage == "" || prevImage == sp.Image {
		return cause
	}
	phase(PhaseRollingBack, cause)

	// ctx may already be cancelled, and abandoning the rollback would leave the component down.
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

// Without a healthcheck, healthy means staying up for a settle period, which a crash-looping container fails.
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
