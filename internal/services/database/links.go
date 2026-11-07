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

package database

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// Kinds of an app's database link.
const (
	LinkKindDatabase = "database" // a logical database the app owns
	LinkKindInstance = "instance" // a whole instance (Redis), shareable across apps
)

var (
	// ErrLinkLogicalDatabase refuses an instance link where apps connect through a scoped logical database.
	ErrLinkLogicalDatabase = errors.New("this engine is linked through one of its databases, not the instance")
	ErrInvalidEnvMap       = errors.New("invalid env mapping")
	ErrEnvConflict         = errors.New("env vars already injected by another linked database")
)

type EnvLink struct {
	Prefix string
	Map    map[string]string
	Vars   []string
}

// ResolveEnvLink refuses var names another app link already injects; skipDB/skipInst exclude the link being reattached.
func (s *Service) ResolveEnvLink(workspaceID, appID uint, engine models.DBEngine, prefix string, m map[string]string, skipDB, skipInst uint) (EnvLink, error) {
	norm, err := NormalizeEnvMap(engine, prefix, m)
	if err != nil {
		return EnvLink{}, fmt.Errorf("%w: %s", ErrInvalidEnvMap, err.Error())
	}
	keys := EnvKeys(engine, prefix, norm)
	linked, err := s.ListByApp(workspaceID, appID)
	if err != nil {
		return EnvLink{}, err
	}
	var taken []string
	for _, l := range linked {
		if (l.Kind == LinkKindDatabase && l.ID == skipDB) || (l.Kind == LinkKindInstance && l.ID == skipInst) {
			continue
		}
		taken = append(taken, l.EnvVars...)
	}
	if c := envConflicts(keys, taken); len(c) > 0 {
		return EnvLink{}, fmt.Errorf("%w: %s; set a prefix or map them to other names", ErrEnvConflict, strings.Join(c, ", "))
	}
	return EnvLink{Prefix: prefix, Map: norm, Vars: keys}, nil
}

// LinkInstance links a whole instance to an app; only engines without logical databases (Redis) link this way.
func (s *Service) LinkInstance(workspaceID, instanceID, appID uint, env EnvLink) (*models.DatabaseInstanceLink, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return nil, ErrNotFound
	}
	if models.EngineUsesLogicalDatabaseRecord(inst.Engine) {
		return nil, ErrLinkLogicalDatabase
	}
	l, err := s.repo.FindInstanceLink(workspaceID, instanceID, appID)
	if err != nil {
		l = &models.DatabaseInstanceLink{WorkspaceID: workspaceID, InstanceID: instanceID, ApplicationID: appID}
	}
	l.EnvPrefix, l.EnvMap, l.EnvVars = env.Prefix, env.Map, env.Vars
	if err := s.repo.SaveInstanceLink(l); err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Service) UnlinkInstance(workspaceID, instanceID, appID uint) (*models.DatabaseInstanceLink, error) {
	l, err := s.repo.FindInstanceLink(workspaceID, instanceID, appID)
	if err != nil {
		return nil, ErrNotFound
	}
	if err := s.repo.DeleteInstanceLink(l.ID); err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Service) InstanceConnectionForApp(workspaceID, appID, instanceID uint) (ConnectionInfo, error) {
	if _, err := s.repo.FindInstanceLink(workspaceID, instanceID, appID); err != nil {
		return ConnectionInfo{}, ErrNotFound
	}
	inst, err := s.repo.FindInWorkspace(workspaceID, instanceID)
	if err != nil {
		return ConnectionInfo{}, ErrNotFound
	}
	return s.InstanceConnection(inst)
}

// ListInstanceLinks returns every app link to an instance.
func (s *Service) ListInstanceLinks(instanceID uint) ([]models.DatabaseInstanceLink, error) {
	return s.repo.ListInstanceLinks(instanceID)
}

// InstanceLinkForApp loads an app's link to an instance.
func (s *Service) InstanceLinkForApp(workspaceID, instanceID, appID uint) (*models.DatabaseInstanceLink, error) {
	l, err := s.repo.FindInstanceLink(workspaceID, instanceID, appID)
	if err != nil {
		return nil, ErrNotFound
	}
	return l, nil
}
