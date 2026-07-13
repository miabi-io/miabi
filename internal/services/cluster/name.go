// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"errors"
	"strings"

	"github.com/jkaninda/logger"
)

// A cluster has no name of its own. Docker Swarm identifies it by a cluster id nobody
// can read and a manager address that changes. So the UI has to say "the cluster",
// which is fine with one — and useless the moment an operator runs prod-eu-west-1 and
// prod-us-east-1 and has to remember which panel is which.
//
// This is a label and nothing more. It is deliberately NOT a step toward multi-cluster:
// one control plane drives one swarm, and modelling a second one we do not have is how
// a name turns into a month.
const clusterNameKey = "cluster_name"

// maxClusterNameLen keeps the name to something a badge can hold.
const maxClusterNameLen = 40

// ErrNameTooLong is returned for a name that would not fit where it is shown.
var ErrNameTooLong = errors.New("the cluster name is too long (max 40 characters)")

// Name returns the operator's label for this cluster, or "" when unnamed.
func (s *Service) Name() string {
	if s.tokens == nil {
		return ""
	}
	v, err := s.tokens.Get(clusterNameKey)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// SetName labels the cluster. An empty name clears it — an operator who no longer wants
// a label should be able to drop it, not be stuck with one.
func (s *Service) SetName(name string) error {
	if s.tokens == nil {
		return errors.New("the cluster name store is not wired")
	}
	name = strings.TrimSpace(name)
	if len(name) > maxClusterNameLen {
		return ErrNameTooLong
	}
	if err := s.tokens.Set(clusterNameKey, name); err != nil {
		return err
	}
	logger.Info("cluster renamed", "name", name)
	return nil
}
