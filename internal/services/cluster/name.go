// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"errors"
	"strings"

	"github.com/jkaninda/logger"
)

// maxClusterNameLen keeps the name to something a badge can hold.
const maxClusterNameLen = 40

// ErrNameTooLong is returned for a name that would not fit where it is shown.
var ErrNameTooLong = errors.New("the cluster name is too long (max 40 characters)")

// Name returns the default cluster's display name, or "" when unnamed.
func (s *Service) Name() string {
	if s.store == nil {
		return ""
	}
	def, err := s.store.FindDefault()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(def.DisplayName)
}

// SetName labels the default cluster. An empty name clears it.
func (s *Service) SetName(name string) error {
	if s.store == nil {
		return errors.New("the cluster store is not wired")
	}
	name = strings.TrimSpace(name)
	if len(name) > maxClusterNameLen {
		return ErrNameTooLong
	}
	def, err := s.store.FindDefault()
	if err != nil {
		return err
	}
	if err := s.store.UpdateColumns(def.ID, map[string]any{"display_name": name}); err != nil {
		return err
	}
	logger.Info("cluster renamed", "name", name)
	return nil
}
