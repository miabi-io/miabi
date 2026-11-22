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

package application

import (
	"github.com/miabi-io/miabi/internal/canaryroute"
	"github.com/miabi-io/miabi/internal/models"
)

const (
	CanaryPreviewStable = canaryroute.Stable
	CanaryPreviewCanary = canaryroute.Canary
	CanaryPreviewSplit  = canaryroute.Split
)

type (
	CanaryPreviewRequest = canaryroute.Request
	CanaryPreviewRule    = canaryroute.RuleResult
	CanaryPreview        = canaryroute.Preview
)

// PreviewCanaryRouting projects the configured weight when no canary runs yet, so rules can be tried before a deploy.
func PreviewCanaryRouting(app *models.Application, req CanaryPreviewRequest) CanaryPreview {
	return canaryroute.Evaluate(app.CanaryMatch, app.CanaryExclusive, effectiveCanaryWeight(app), req)
}
