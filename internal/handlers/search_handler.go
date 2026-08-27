// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/search"
)

type SearchHandler struct {
	svc *search.Service
}

func NewSearchHandler(svc *search.Service) *SearchHandler {
	return &SearchHandler{svc: svc}
}

func (h *SearchHandler) Search(c *okapi.Context) error {
	res, err := h.svc.Search(middlewares.WorkspaceID(c), c.Query("q"), queryInt(c, "limit", search.DefaultLimit))
	if err != nil {
		return c.AbortInternalServerError("search failed", err)
	}
	return ok(c, res)
}
