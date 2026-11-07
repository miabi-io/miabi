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

package models

import "time"

// DNSRecord purposes — why Miabi created a managed record.
const (
	DNSRecordPurposeVerification = "verification" // _miabi-challenge ownership TXT
	DNSRecordPurposeAppAddress   = "app-address"  // app A/AAAA/CNAME
)

// DNSRecord ledgers records Miabi created; a record at a managed name not in it is a conflict and never overwritten.
type DNSRecord struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	DomainID uint   `json:"domain_id" gorm:"index:idx_dnsrec_domain_key,unique;not null"`
	Name     string `json:"name" gorm:"index:idx_dnsrec_domain_key,unique;not null"`
	Type     string `json:"type" gorm:"index:idx_dnsrec_domain_key,unique;not null"` // TXT | A | AAAA | CNAME

	Value   string `json:"value"`
	Purpose string `json:"purpose" gorm:"not null"` // verification | app-address
	// Set for app-address records so they are cleaned up when the app or route is removed.
	AppID *uint `json:"app_id,omitempty" gorm:"index"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
