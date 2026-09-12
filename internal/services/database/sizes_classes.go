// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"fmt"
	"strconv"
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

// SizeOffer is what a workspace may give a database: the sizes offered to it, the default among them, and whether
// its plan binds it to them.
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

// offerFrom narrows the catalog to a plan's sizes, in the plan's order so its first is the default. An unenforced
// or empty binding offers the whole catalog with no default, and so does a plan whose sizes were all deleted:
// binding it to none would refuse every database.
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

// resolveSize turns a request into the limits it lands on. resize marks a change to an existing instance, where
// asking for no limits in a bound workspace is refused rather than read as "the default".
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

// pickSize applies a workspace's size offer to a request. A named size must be offered. A workspace its plan binds
// to sizes gets one either way: the default when the request names nothing, else the smallest offered size
// covering the requested memory and CPU, so a template or manifest stating plain numbers keeps working.
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
	minMemory := int64(spec.minMemoryMB) * mebibyte
	if req.MemoryBytes == 0 && req.NanoCPUs == 0 {
		if resize {
			return req, fmt.Errorf("%w: this workspace's plan offers the database sizes %s; pick one", ErrInvalidResources, sizeNames(offer.Sizes))
		}
		if def := offer.Sizes[0]; def.MemoryBytes >= minMemory {
			return fromSize(def), nil
		}
	}
	memory := max(req.MemoryBytes, minMemory)
	var best *models.DatabaseSize
	for i := range offer.Sizes {
		sz := &offer.Sizes[i]
		if sz.MemoryBytes < memory || sz.NanoCPUs < req.NanoCPUs {
			continue
		}
		if best == nil || sz.MemoryBytes < best.MemoryBytes || (sz.MemoryBytes == best.MemoryBytes && sz.NanoCPUs < best.NanoCPUs) {
			best = sz
		}
	}
	if best == nil {
		return req, fmt.Errorf("%w: no database size this workspace's plan offers has %s; it offers %s",
			ErrInvalidResources, describeRequest(memory, req.NanoCPUs), sizeNames(offer.Sizes))
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
		out += " and " + strconv.FormatFloat(float64(nanoCPUs)/nanosPerCore, 'f', -1, 64) + " CPU"
	}
	return out
}
