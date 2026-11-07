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

// SizeCatalog lists the database sizes a platform offers. Satisfied by the size repository.
type SizeCatalog interface {
	List() ([]models.DatabaseSize, error)
}

// SetSizeCatalog wires database sizes (Enterprise). Without it no request may name a size.
func (s *Service) SetSizeCatalog(c SizeCatalog) { s.sizes = c }

// ErrSizesUnavailable refuses a named size where database sizes are not licensed.
var ErrSizesUnavailable = errors.New("database sizes need an Enterprise license with database sizes")

type SizeOffer struct {
	Sizes     []models.DatabaseSize `json:"sizes"`
	DefaultID uint                  `json:"default_id,omitempty"`
	Bound     bool                  `json:"bound"`
}

// SizesFor lists the sizes a workspace may give a database. Empty where sizes are not licensed.
func (s *Service) SizesFor(workspaceID uint) (SizeOffer, error) {
	if s.sizes == nil || !s.quota.DatabaseSizesLicensed() {
		return SizeOffer{Sizes: []models.DatabaseSize{}}, nil
	}
	all, err := s.sizes.List()
	if err != nil {
		return SizeOffer{}, err
	}
	allowed, enforced := s.quota.EffectiveDatabaseSizes(workspaceID)
	return offerFrom(all, allowed, enforced), nil
}

// An unenforced, empty, or fully-deleted binding offers the whole catalog: binding to none would refuse every database.
func offerFrom(all []models.DatabaseSize, allowed []uint, enforced bool) SizeOffer {
	if !enforced || len(allowed) == 0 {
		return SizeOffer{Sizes: all}
	}
	byID := make(map[uint]models.DatabaseSize, len(all))
	for _, sz := range all {
		byID[sz.ID] = sz
	}
	offer := SizeOffer{Bound: true}
	for _, id := range allowed {
		if sz, ok := byID[id]; ok {
			offer.Sizes = append(offer.Sizes, sz)
		}
	}
	if len(offer.Sizes) == 0 {
		return SizeOffer{Sizes: all}
	}
	offer.DefaultID = offer.Sizes[0].ID
	return offer
}

// On resize, a bound workspace asking for no limits is refused rather than read as the default.
func (s *Service) resolveSize(workspaceID uint, spec engineSpec, req Resources, resize bool) (Resources, error) {
	licensed := s.sizes != nil && s.quota.DatabaseSizesLicensed()
	switch {
	case !licensed && req.Size != "":
		return req, ErrSizesUnavailable
	case !licensed:
		return req, nil
	}
	offer, err := s.SizesFor(workspaceID)
	if err != nil {
		return req, err
	}
	return pickSize(offer, spec, req, resize)
}

// A bound workspace gets the smallest size covering the requested numbers, so plain-number manifests keep working.
func pickSize(offer SizeOffer, spec engineSpec, req Resources, resize bool) (Resources, error) {
	if req.Size != "" {
		if req.MemoryBytes != 0 || req.NanoCPUs != 0 {
			return req, fmt.Errorf("%w: name a size, or set memory and CPU, not both", ErrInvalidResources)
		}
		for _, sz := range offer.Sizes {
			if sz.Name == req.Size {
				return fromSize(sz), nil
			}
		}
		return req, fmt.Errorf("%w: size %q is not offered to this workspace; it is offered %s", ErrInvalidResources, req.Size, sizeNames(offer.Sizes))
	}
	if !offer.Bound {
		return req, nil
	}
	minMemory, minCPUs := spec.minMemory(), spec.minNanoCPUs()
	if req.MemoryBytes == 0 && req.NanoCPUs == 0 {
		if resize {
			return req, fmt.Errorf("%w: this workspace's plan offers the database sizes %s; pick one", ErrInvalidResources, sizeNames(offer.Sizes))
		}
		if def := offer.Sizes[0]; def.MemoryBytes >= minMemory && def.NanoCPUs >= minCPUs {
			return fromSize(def), nil
		}
	}
	memory, cpus := max(req.MemoryBytes, minMemory), max(req.NanoCPUs, minCPUs)
	var best *models.DatabaseSize
	for i := range offer.Sizes {
		sz := &offer.Sizes[i]
		if sz.MemoryBytes < memory || sz.NanoCPUs < cpus {
			continue
		}
		if best == nil || sz.MemoryBytes < best.MemoryBytes || (sz.MemoryBytes == best.MemoryBytes && sz.NanoCPUs < best.NanoCPUs) {
			best = sz
		}
	}
	if best == nil {
		return req, fmt.Errorf("%w: no database size this workspace's plan offers has %s; it offers %s",
			ErrInvalidResources, describeRequest(memory, cpus), sizeNames(offer.Sizes))
	}
	return fromSize(*best), nil
}

func fromSize(sz models.DatabaseSize) Resources {
	return Resources{MemoryBytes: sz.MemoryBytes, NanoCPUs: sz.NanoCPUs, Size: sz.Name}
}

func sizeNames(sizes []models.DatabaseSize) string {
	names := make([]string, len(sizes))
	for i, sz := range sizes {
		names[i] = sz.Name
	}
	return strings.Join(names, ", ")
}

func describeRequest(memoryBytes, nanoCPUs int64) string {
	out := fmt.Sprintf("%d MB of memory", memoryBytes/mebibyte)
	if nanoCPUs > 0 {
		out += " and " + formatCores(nanoCPUs) + " CPU"
	}
	return out
}
