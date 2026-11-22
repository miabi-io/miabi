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

package analytics

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// Shared with the Flow tab's banner, so the warning and the automatic pause agree.
const (
	RegressionMinRequests = 50
	RegressionMargin      = 0.01
)

type VariantComparison struct {
	CanaryRequests int64   `json:"canary_requests"`
	StableRequests int64   `json:"stable_requests"`
	CanaryRate     float64 `json:"canary_rate"`
	StableRate     float64 `json:"stable_rate"`
	// Regressed needs enough traffic on both sides and a full point of difference, so one 500 on a quiet canary is not a verdict.
	Regressed bool `json:"regressed"`
}

func CompareVariants(rows []models.AnalyticsRollup) VariantComparison {
	var c VariantComparison
	var c5, s5 int64
	for i := range rows {
		switch rows[i].Variant {
		case VariantCanary:
			c.CanaryRequests += rows[i].Requests
			c5 += rows[i].Status5xx
		case VariantStable:
			c.StableRequests += rows[i].Requests
			s5 += rows[i].Status5xx
		}
	}
	if c.CanaryRequests > 0 {
		c.CanaryRate = float64(c5) / float64(c.CanaryRequests)
	}
	if c.StableRequests > 0 {
		c.StableRate = float64(s5) / float64(c.StableRequests)
	}
	c.Regressed = c.CanaryRequests >= RegressionMinRequests && c.StableRequests >= RegressionMinRequests &&
		c.CanaryRate-c.StableRate >= RegressionMargin
	return c
}

type rollupRanger interface {
	Range(workspaceID uint, appID *uint, since, until time.Time) ([]models.AnalyticsRollup, error)
}

// CanaryGuard reads an app's recent traffic for the canary ramp; implements worker.CanaryRegressionChecker.
type CanaryGuard struct{ rollups rollupRanger }

func NewCanaryGuard(r rollupRanger) *CanaryGuard { return &CanaryGuard{rollups: r} }

func (g *CanaryGuard) CanaryRegression(app *models.Application, since time.Time) (VariantComparison, error) {
	id := app.ID
	rows, err := g.rollups.Range(app.WorkspaceID, &id, since, time.Now().UTC())
	if err != nil {
		return VariantComparison{}, err
	}
	return CompareVariants(rows), nil
}
