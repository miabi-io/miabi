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

package pipeline

import (
	"encoding/json"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/webhooksig"
)

// nil disables scheduling (e.g. in tests).
type Scheduler interface {
	Schedule(pipelineID uint, cronExpr string) error
	Unschedule(pipelineID uint)
}

// SetScheduler is called once at startup and registers every enabled scheduled pipeline.
func (s *Service) SetScheduler(sc Scheduler) {
	s.scheduler = sc
	s.RegisterSchedules()
}

// RegisterSchedules is idempotent, safe to call on boot.
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

// TriggerScheduled resolves across workspaces since the cron loop has no request scope.
func (s *Service) TriggerScheduled(pipelineID uint) (*models.PipelineRun, error) {
	p, err := s.repo.FindByID(pipelineID)
	if err != nil {
		return nil, ErrNotFound
	}
	if s.restricted != nil {
		if err := s.restricted(p.WorkspaceID); err != nil {
			return nil, err
		}
	}
	return s.Trigger(p.WorkspaceID, p.ID, TriggerInput{Trigger: "schedule", Branch: p.SourceRef})
}

// VerifyWebhook checks an inbound push webhook against the pipeline's secret.
func (s *Service) VerifyWebhook(p *models.PipelineDefinition, signature string, body []byte) bool {
	return webhooksig.Verify(p.WebhookSecret, signature, body)
}

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

// TriggerPush returns fired=false when the signature is valid but the branch filter does not match.
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
	if s.restricted != nil {
		if err := s.restricted(workspaceID); err != nil {
			// Acknowledged, not failed: the provider would otherwise retry a push that cannot run.
			logger.Info("pipeline push ignored: workspace restricted", "pipeline", pipelineID, "reason", err)
			return nil, false, nil
		}
	}
	run, err = s.Trigger(workspaceID, pipelineID, TriggerInput{
		Trigger: "push", Branch: branch, Commit: commit, CommitMessage: message,
	})
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}
