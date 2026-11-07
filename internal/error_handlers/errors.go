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

package errorhandlers

import (
	"errors"
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/dto"
)

func CustomErrorHandler() okapi.ErrorHandler {
	return func(c *okapi.Context, code int, message string, err error) error {
		var errStr string
		errCode := httpStatusToCode(code)
		if err != nil {
			errStr = err.Error()
			var coder interface{ Code() string }
			if errors.As(err, &coder) {
				if cc := coder.Code(); cc != "" {
					errCode = cc
				}
			}
			// AbortWithError passes only the status text as message, so promote the real reason.
			if message == "" || message == http.StatusText(code) {
				message = errStr
				// Prefer a curated Message() for the human message; `error` keeps the raw detail.
				var msgr interface{ Message() string }
				if errors.As(err, &msgr) {
					if m := msgr.Message(); m != "" {
						message = m
					}
				}
			}
		}
		if message == "" {
			message = http.StatusText(code)
		}
		return c.JSON(code, dto.ErrorResponseBody{
			Success: false,
			Data:    nil,
			Error: &dto.ErrorInfo{
				StatusCode: code,
				Code:       errCode,
				Message:    message,
				Error:      errStr,
			},
		})
	}
}

func httpStatusToCode(status int) string {
	switch status {
	case 400:
		return "BAD_REQUEST"
	case 401:
		return "UNAUTHORIZED"
	case 402:
		return "PAYMENT_REQUIRED"
	case 403:
		return "FORBIDDEN"
	case 404:
		return "NOT_FOUND"
	case 405:
		return "METHOD_NOT_ALLOWED"
	case 409:
		return "CONFLICT"
	case 422:
		return "UNPROCESSABLE_ENTITY"
	case 429:
		return "TOO_MANY_REQUESTS"
	case 500:
		return "INTERNAL_SERVER_ERROR"
	case 502:
		return "BAD_GATEWAY"
	case 503:
		return "SERVICE_UNAVAILABLE"
	default:
		return "ERROR"
	}
}
