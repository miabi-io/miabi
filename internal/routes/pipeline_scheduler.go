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

package routes

import (
	"fmt"

	cronpkg "github.com/miabi-io/miabi/internal/cron"
	"github.com/miabi-io/miabi/internal/services/pipeline"
)

// Keyed "pipeline:<id>" so it shares the one cron loop.
type pipelineCronScheduler struct {
	cron *cronpkg.Manager
	svc  *pipeline.Service
}

func (p pipelineCronScheduler) Schedule(pipelineID uint, cronExpr string) error {
	return p.cron.RegisterTask("pipeline", pipelineID, fmt.Sprintf("Pipeline #%d schedule", pipelineID), cronExpr, func() error {
		_, err := p.svc.TriggerScheduled(pipelineID)
		return err
	})
}

func (p pipelineCronScheduler) Unschedule(pipelineID uint) {
	p.cron.UnregisterTask("pipeline", pipelineID)
}
