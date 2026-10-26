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

package billing

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

var planKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var (
	ErrPlanKey        = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "the plan key is 1-64 lowercase letters, digits, dots, dashes or underscores")
	ErrPlanNameTaken  = coded(http.StatusConflict, "PLAN_NAME_TAKEN", "another plan already uses this name")
	ErrPlanSystem     = coded(http.StatusConflict, "PLAN_SYSTEM", "the platform's system plan cannot be managed by a billing console")
	ErrPlanNotManaged = coded(http.StatusNotFound, "BILLING_PLAN_NOT_FOUND", "no console-managed plan with this key")
)

// PlanPush is PUT /billing/plans/{key}: a console plan template.
type PlanPush struct {
	Name         string           `json:"name" required:"true"`
	Limits       map[string]int64 `json:"limits"`
	Capabilities map[string]bool  `json:"capabilities"`
	// Settings carries the other console-owned fields (models.PlanConsoleSettings).
	Settings map[string]any `json:"settings"`
}

// PlanPushResult is the answer to a plan push.
type PlanPushResult struct {
	Key     string `json:"key"`
	Plan    Plan   `json:"plan"`
	Created bool   `json:"created"`
	// Adopted reports that an existing hand-made plan with this name was taken over.
	Adopted bool `json:"adopted"`
	// Ignored lists fields this Miabi version does not know or never syncs.
	Ignored []string `json:"ignored"`
}

// PlanDryRun reports the workspaces on a plan that a push would put over a limit.
type PlanDryRun struct {
	Plan       string            `json:"plan"`
	Ignored    []string          `json:"ignored"`
	Workspaces []DryRunWorkspace `json:"workspaces"`
}

// PlanSchema lists the plan fields this Miabi accepts from a console.
type PlanSchema struct {
	Limits       []string `json:"limits"`
	Capabilities []string `json:"capabilities"`
	Settings     []string `json:"settings"`
}

// PlanSchema derives the accepted fields from the plan model, so a new limit is offered without
// touching this code.
func (s *Service) PlanSchema() (PlanSchema, error) {
	p, err := ToPlan(&models.Plan{})
	if err != nil {
		return PlanSchema{}, err
	}
	out := PlanSchema{Limits: []string{}, Capabilities: []string{}, Settings: append([]string{}, models.PlanConsoleSettings...)}
	for k := range p.Limits {
		out.Limits = append(out.Limits, k)
	}
	for k := range p.Capabilities {
		out.Capabilities = append(out.Capabilities, k)
	}
	sort.Strings(out.Limits)
	sort.Strings(out.Capabilities)
	return out, nil
}

// PushPlan creates or updates the plan the console addresses by key, adopting a hand-made plan of
// the same name. Instance-local fields are never touched. With dryRun nothing is written.
func (s *Service) PushPlan(key string, in PlanPush, dryRun bool) (*PlanPushResult, *PlanDryRun, error) {
	key = strings.TrimSpace(key)
	if !planKeyPattern.MatchString(key) {
		return nil, nil, ErrPlanKey
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, nil, coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "name is required")
	}
	var res *PlanPushResult
	var dry *PlanDryRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		p, created, adopted, err := planForKey(tx, key, in.Name)
		if err != nil {
			return err
		}
		ignored, err := applyPlanPush(p, in)
		if err != nil {
			return err
		}
		if dryRun {
			dry, err = s.planDryRun(tx, p, ignored)
			return err
		}
		p.ManagedBy, p.ExternalKey = models.ManagedByConsole, &key
		if created {
			err = tx.Create(p).Error
		} else {
			err = tx.Save(p).Error
		}
		if err != nil {
			if isUniqueViolation(err) {
				return ErrPlanNameTaken
			}
			return err
		}
		out, err := ToPlan(p)
		if err != nil {
			return err
		}
		res = &PlanPushResult{Key: key, Plan: out, Created: created, Adopted: adopted, Ignored: ignored}
		return nil
	})
	return res, dry, err
}

// planForKey finds the plan a push applies to: the one holding the key, else an unmanaged plan
// of the same name (adopted), else a new one.
func planForKey(tx *gorm.DB, key, name string) (p *models.Plan, created, adopted bool, err error) {
	var byKey models.Plan
	switch err := tx.Where("external_key = ?", key).First(&byKey).Error; {
	case err == nil:
		if byKey.Name != name {
			var n int64
			if err := tx.Model(&models.Plan{}).Where("name = ? AND id <> ?", name, byKey.ID).Count(&n).Error; err != nil {
				return nil, false, false, err
			}
			if n > 0 {
				return nil, false, false, ErrPlanNameTaken
			}
		}
		return &byKey, false, false, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, false, err
	}
	var byName models.Plan
	switch err := tx.Where("name = ?", name).First(&byName).Error; {
	case err == nil:
		if byName.System {
			return nil, false, false, ErrPlanSystem
		}
		if byName.ConsoleManaged() || byName.ExternalKey != nil {
			return nil, false, false, ErrPlanNameTaken
		}
		return &byName, false, true, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, false, err
	}
	return &models.Plan{IsActive: true}, true, false, nil
}

// applyPlanPush writes the pushed console fields onto p through its JSON form, returning the
// fields it did not apply: unknown to this version, of the wrong type, or instance-local.
func applyPlanPush(p *models.Plan, in PlanPush) ([]string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	set := map[string]any{"name": in.Name}
	ignored := []string{}
	for k, v := range in.Limits {
		if _, ok := fields[k].(float64); ok && strings.HasPrefix(k, "max_") {
			set[k] = v
		} else {
			ignored = append(ignored, k)
		}
	}
	for k, v := range in.Capabilities {
		if _, ok := fields[k].(bool); ok && strings.HasPrefix(k, "allow_") {
			set[k] = v
		} else {
			ignored = append(ignored, k)
		}
	}
	for k, v := range in.Settings {
		if _, known := fields[k]; known && models.IsPlanConsoleField(k) && !strings.HasPrefix(k, "max_") && !strings.HasPrefix(k, "allow_") && k != "name" {
			set[k] = v
		} else {
			ignored = append(ignored, k)
		}
	}
	sort.Strings(ignored)
	rawSet, err := json.Marshal(set)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rawSet, p); err != nil {
		return nil, coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "a plan setting has the wrong type: "+err.Error())
	}
	return ignored, nil
}

// planDryRun reports, for each workspace on the plan, the limits the pushed values would break.
func (s *Service) planDryRun(tx *gorm.DB, p *models.Plan, ignored []string) (*PlanDryRun, error) {
	out := &PlanDryRun{Plan: p.Name, Ignored: ignored, Workspaces: []DryRunWorkspace{}}
	if p.ID == 0 {
		return out, nil
	}
	cp, err := ToPlan(p)
	if err != nil {
		return nil, err
	}
	var wss []models.Workspace
	if err := tx.Where("plan_id = ?", p.ID).Order("id").Find(&wss).Error; err != nil {
		return nil, err
	}
	for _, ws := range wss {
		dw := DryRunWorkspace{UID: ws.UID, Name: ws.Name, Violations: []Violation{}}
		if s.deps.Meter != nil {
			for res, used := range s.deps.Meter.Measure(ws.ID) {
				if lim, ok := cp.Limits["max_"+res]; ok && lim >= 0 && used > float64(lim) {
					dw.Violations = append(dw.Violations, Violation{Resource: "max_" + res, Used: used, Limit: float64(lim)})
				}
			}
			sort.Slice(dw.Violations, func(i, j int) bool { return dw.Violations[i].Resource < dw.Violations[j].Resource })
		}
		out.Workspaces = append(out.Workspaces, dw)
	}
	return out, nil
}

// RemovePlan withdraws a console plan: deactivated while workspaces still use it, deleted otherwise.
// It reports whether the row was deleted.
func (s *Service) RemovePlan(key string) (bool, error) {
	var p models.Plan
	if err := s.db.Where("external_key = ?", strings.TrimSpace(key)).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrPlanNotManaged
		}
		return false, err
	}
	var n int64
	if err := s.db.Model(&models.Workspace{}).Where("plan_id = ?", p.ID).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 || p.IsDefault {
		return false, s.db.Model(&p).Update("is_active", false).Error
	}
	return true, s.db.Delete(&p).Error
}
