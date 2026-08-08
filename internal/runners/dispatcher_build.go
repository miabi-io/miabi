// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// BuildInputs describes a standalone image build — a git-source app deploy, not a pipeline run.
// The runner clones SourceURL at Commit, builds per Build, and pushes to Repository; the
// Dispatcher fills the per-job credentials and deadline.
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

// RunBuild dispatches a one-step image build to an eligible runner and drives it to completion,
// streaming log lines to onLog and returning the pushed digest. Unlike the pipeline path it
// persists nothing; the build is leased under LeaseKindBuild so it never collides with a run.
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

	// Trip the stream read deadline on ctx cancellation so a per-job timeout takes
	// effect even while the runner is silent (processBuildFrames blocks on Decode).
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

// buildOnlyJobSpec assembles a JobSpec with a single build step carrying the app's build config.
// There is no pipeline run: RunID carries the per-app deployment number, which the runner uses as
// the image tag. The globally-unique deployment id keys the lease and credentials in RunBuild.
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
	return spec, secrets
}

// processBuildFrames reads the runner's report stream for a build job: it streams redacted log
// lines to onLog and returns the pushed digest from the terminal frame. It persists nothing — the
// deploy worker owns the deployment.
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
