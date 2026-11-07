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

package registryserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/config"
)

var (
	// The platform pull credential covers every namespace, so a foreign namespace is a tenant boundary violation.
	ErrForeignNamespace = errors.New("image belongs to another workspace's registry namespace")
	// A free namespace could be claimed by a rename later, silently turning a broken ref into a foreign one.
	ErrUnknownNamespace = errors.New("image names an unknown workspace registry namespace")
	ErrHostInvalid      = config.ErrRegistryHostInvalid
)

// The rule lives in config because boot applies it before this service exists.
func NormalizeHost(raw string) (string, error) { return config.NormalizeRegistryHost(raw) }

// An internal ref with no namespace returns internal=true and an empty namespace; the caller must refuse it.
func splitInternalRef(host, ref string) (namespace, rest string, internal bool) {
	host = strings.TrimSpace(host)
	ref = strings.TrimSpace(ref)
	if host == "" || ref == "" {
		return "", "", false
	}
	body, found := strings.CutPrefix(ref, host+"/")
	if !found {
		return "", "", false
	}
	ns, rest, found := strings.Cut(body, "/")
	if !found || ns == "" || rest == "" {
		return "", body, true
	}
	return ns, rest, true
}

// IsInternalRef says nothing about who may pull; use ResolveImageRef for that.
func (s *Service) IsInternalRef(ref string) bool {
	_, _, internal := splitInternalRef(s.RegistryHost(), ref)
	return internal
}

// ResolveImageRef allows internal refs only for the owning workspace, rewritten to ws_<id> to survive renames.
func (s *Service) ResolveImageRef(workspaceID uint, ref string) (string, error) {
	host := s.RegistryHost()
	ns, rest, internal := splitInternalRef(host, ref)
	if !internal {
		return ref, nil
	}
	if ns == "" {
		return "", fmt.Errorf("%w: %q — expected %s/<workspace>/<image>", ErrUnknownNamespace, ref, host)
	}
	ws, err := s.resolveNamespace(ns)
	if err != nil || ws == nil {
		return "", fmt.Errorf("%w: %q in %q", ErrUnknownNamespace, ns, ref)
	}
	if ws.ID != workspaceID {
		return "", fmt.Errorf("%w: %q is in workspace %q", ErrForeignNamespace, ref, ws.Name)
	}
	return host + "/" + Namespace(ws.ID) + "/" + rest, nil
}

// ValidateImageRef refuses foreign refs at write time, where the user can act, rather than at deploy on a node.
func (s *Service) ValidateImageRef(workspaceID uint, ref string) error {
	_, err := s.ResolveImageRef(workspaceID, ref)
	return err
}
