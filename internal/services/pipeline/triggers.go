// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package pipeline

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Scheduler registers and unregisters a pipeline's `on.schedule` cron. A cron
// adapter implements it at wiring time; nil disables scheduling (e.g. in tests).
type Scheduler interface {
	Schedule(pipelineID uint, cronExpr string) error
	Unschedule(pipelineID uint)
}

// SetScheduler installs the cron adapter and (re)registers every enabled
// pipeline that declares a schedule. Called once at startup.
func (s *Service) SetScheduler(sc Scheduler) {
	s.scheduler = sc
	s.RegisterSchedules()
}

// RegisterSchedules (re)registers the cron entry for every enabled pipeline that
// declares `on.schedule`. Idempotent — safe to call on boot.
func (s *Service) RegisterSchedules() {
	if s.scheduler == nil {
		return
	}
	defs, err := s.repo.ListEnabled()
	if err != nil {
		return
	}
	for i := range defs {
		s.applySchedule(&defs[i])
	}
}

// applySchedule (un)registers a single pipeline's cron based on its current spec
// and enabled flag.
func (s *Service) applySchedule(p *models.PipelineDefinition) {
	if s.scheduler == nil {
		return
	}
	s.scheduler.Unschedule(p.ID)
	if !p.Enabled {
		return
	}
	spec, err := ParseSpec([]byte(p.Spec))
	if err != nil || strings.TrimSpace(spec.On.Schedule) == "" {
		return
	}
	_ = s.scheduler.Schedule(p.ID, spec.On.Schedule)
}

// TriggerScheduled fires a run for a scheduled pipeline (cron callback). It
// resolves the pipeline across workspaces, since the cron loop has no request
// scope.
func (s *Service) TriggerScheduled(pipelineID uint) (*models.PipelineRun, error) {
	p, err := s.repo.FindByID(pipelineID)
	if err != nil {
		return nil, ErrNotFound
	}
	// A repo-owned pipeline's scheduled run builds the ref it was adopted from;
	// SourceRef is empty for a manual pipeline, leaving the branch unset as before.
	return s.Trigger(p.WorkspaceID, p.ID, TriggerInput{Trigger: "schedule", Branch: p.SourceRef})
}

// VerifyWebhook checks an inbound push webhook's signature against the pipeline's
// secret: GitHub's X-Hub-Signature-256 HMAC scheme, or GitLab's bare-token style.
func (s *Service) VerifyWebhook(p *models.PipelineDefinition, signature string, body []byte) bool {
	signature = strings.TrimSpace(signature)
	if signature == "" || p.WebhookSecret == "" {
		return false
	}
	if signature == p.WebhookSecret { // GitLab X-Gitlab-Token
		return true
	}
	mac := hmac.New(sha256.New, []byte(p.WebhookSecret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// pushPayload is the subset of a GitHub/GitLab push event we read: the pushed
// ref and the head commit.
type pushPayload struct {
	Ref         string `json:"ref"`          // refs/heads/<branch>
	After       string `json:"after"`        // GitHub head sha
	CheckoutSHA string `json:"checkout_sha"` // GitLab head sha
	HeadCommit  struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
}

func parsePush(body []byte) (branch, commit, message string) {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", ""
	}
	branch = strings.TrimPrefix(p.Ref, "refs/heads/")
	switch {
	case p.HeadCommit.ID != "":
		commit = p.HeadCommit.ID
	case p.After != "":
		commit = p.After
	case p.CheckoutSHA != "":
		commit = p.CheckoutSHA
	}
	return branch, commit, p.HeadCommit.Message
}

// TriggerPush verifies a push webhook and fires the pipeline when the pushed
// branch matches its `on.push.branches`. fired reports whether a run started
// (false = signature ok but the trigger does not apply to this push).
func (s *Service) TriggerPush(workspaceID, pipelineID uint, signature string, body []byte) (run *models.PipelineRun, fired bool, err error) {
	p, err := s.Get(workspaceID, pipelineID)
	if err != nil {
		return nil, false, err
	}
	if !s.VerifyWebhook(p, signature, body) {
		return nil, false, ErrUnauthorized
	}
	spec, err := ParseSpec([]byte(p.Spec))
	if err != nil {
		return nil, false, ErrInvalidSpec
	}
	branch, commit, message := parsePush(body)
	if spec.On.Push == nil || !spec.On.FiresOnBranch(branch) {
		return nil, false, nil // not an error; this push just doesn't apply
	}
	run, err = s.Trigger(workspaceID, pipelineID, TriggerInput{
		Trigger: "push", Branch: branch, Commit: commit, CommitMessage: message,
	})
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}
