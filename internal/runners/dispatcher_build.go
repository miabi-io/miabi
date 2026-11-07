// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package runners

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/runner"
	"github.com/miabi-io/runner/proto"
)

// BuildInputs describes a standalone git-source app build, not a pipeline run.
type BuildInputs struct {
	DeploymentID     uint // lease/credential id (globally unique; leased under LeaseKindBuild)
	DeploymentNumber int  // per-app sequential number (v1, v2, …); the image tag
	WorkspaceID      uint
	WorkspaceName    string
	AppID            uint
	AppName          string
	SourceURL        string // git remote the runner clones (may embed a credential)
	Commit           string
	Ref              string
	Branch           string
	Repository       string // fully-qualified push target (registry/ns/app)
	Registry         string // registry host
	Build            *proto.BuildConfig
	RequiredLabels   []string // runner selection constraints (arch/…); usually empty
}

// RunBuild persists nothing and leases under LeaseKindBuild so it never collides with a pipeline run.
func (d *Dispatcher) RunBuild(ctx context.Context, in BuildInputs, subjectUserID uint, onLog func(string)) (string, error) {
	rn, err := d.runners.SelectRunner(runner.Job{WorkspaceID: in.WorkspaceID, RequiredLabels: in.RequiredLabels})
	if err != nil {
		return "", err // ErrNoRunner → caller queues
	}

	deadline := d.deadline()
	var creds *JobCredentials
	if d.minter != nil {
		creds, err = d.minter.Mint(subjectUserID, in.WorkspaceID, &in.AppID, in.DeploymentID, deadline)
		if err != nil {
			return "", fmt.Errorf("mint job credentials: %w", err)
		}
		defer d.minter.Revoke(creds) // dead the moment the build is terminal
	}
	spec, mask := buildOnlyJobSpec(in, creds, deadline)

	if _, err := d.runners.Lease(rn.ID, models.LeaseKindBuild, in.DeploymentID, nil, deadline); err != nil {
		return "", fmt.Errorf("lease runner %d: %w", rn.ID, err)
	}
	defer func() { _ = d.runners.ReleaseRun(models.LeaseKindBuild, in.DeploymentID) }()

	sess, ok := d.sessions.Session(rn.ID)
	if !ok {
		return "", ErrRunnerOffline
	}
	stream, err := sess.OpenStream()
	if err != nil {
		return "", fmt.Errorf("open runner stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	// processBuildFrames blocks on Decode, so trip the read deadline on cancel for the timeout to take effect.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.SetReadDeadline(time.Now())
		case <-stopWatch:
		}
	}()

	if err := proto.WriteJob(stream, spec); err != nil {
		return "", fmt.Errorf("send build job: %w", err)
	}
	logger.Info("dispatched build to runner", "deployment", in.DeploymentID, "app", in.AppID, "runner", rn.Name)

	return d.processBuildFrames(ctx, stream, mask, onLog)
}

// RunID carries the per-app deployment number (the image tag); the global deployment id keys lease and credentials.
func buildOnlyJobSpec(in BuildInputs, creds *JobCredentials, deadline time.Time) (proto.JobSpec, []string) {
	env := []string{
		kv("MIABI_WORKSPACE_NAME", in.WorkspaceName),
		kv("MIABI_WORKSPACE_ID", utos(in.WorkspaceID)),
		kv("MIABI_COMMIT", in.Commit),
	}
	env = appendKV(env, "MIABI_APP_NAME", in.AppName)
	env = append(env, kv("MIABI_APP_ID", utos(in.AppID)))
	env = appendKV(env, "MIABI_REF", in.Ref)
	env = appendKV(env, "MIABI_BRANCH", in.Branch)
	env = appendKV(env, "MIABI_REGISTRY", in.Registry)
	env = appendKV(env, "MIABI_IMAGE_REPOSITORY", in.Repository)
	if creds != nil {
		env = appendKV(env, "MIABI_REGISTRY_USER", creds.RegistryUser)
		env = appendKV(env, "MIABI_REGISTRY_TOKEN", creds.RegistryToken)
		env = appendKV(env, "MIABI_JOB_TOKEN", creds.JobToken)
	}

	spec := proto.JobSpec{
		RunID:       uint(in.DeploymentNumber),
		WorkspaceID: in.WorkspaceID,
		Workspace:   in.WorkspaceName,
		AppID:       &in.AppID,
		App:         in.AppName,
		Commit:      in.Commit,
		Ref:         in.Ref,
		Branch:      in.Branch,
		SourceURL:   in.SourceURL,
		Repository:  in.Repository,
		Registry:    in.Registry,
		Steps:       []proto.StepSpec{{Ordinal: 0, Name: "build", Uses: "build", Build: in.Build}},
		Env:         env,
		Deadline:    deadline,
	}
	var secrets []string
	if creds != nil {
		secrets = creds.Secrets()
	}
	return spec, append(secrets, sourceURLSecrets(in.SourceURL)...)
}

// Persists nothing: the deploy worker owns the deployment.
func (d *Dispatcher) processBuildFrames(ctx context.Context, r io.Reader, mask []string, onLog func(string)) (string, error) {
	dec := json.NewDecoder(r)
	var digest string
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var f proto.Frame
		if err := dec.Decode(&f); err != nil {
			return "", fmt.Errorf("runner stream ended before completion: %w", err)
		}
		switch f.Type {
		case proto.FrameLog:
			if onLog != nil {
				onLog(redact(f.Line, mask))
			}
		case proto.FrameResult:
			digest = f.Digest // the build step pushed this digest
		case proto.FrameError:
			return "", fmt.Errorf("build failed: %s", f.Error)
		case proto.FrameDone:
			if mapStatus(f.Status) != models.PipelineRunSucceeded {
				return "", fmt.Errorf("build did not succeed (%s)", f.Status)
			}
			if digest == "" {
				return "", fmt.Errorf("build succeeded but reported no image digest")
			}
			return digest, nil
		}
	}
}
