// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DNSRecord purposes — why Miabi created a managed record.
const (
	DNSRecordPurposeVerification = "verification" // _miabi-challenge ownership TXT
	DNSRecordPurposeAppAddress   = "app-address"  // app A/AAAA/CNAME
)

// DNSRecord is the ledger of records Miabi created through a connected provider, so reconcile
// and cleanup only ever touch records Miabi owns. A record at a managed name that is not in
// this ledger is treated as a conflict and never overwritten.
type DNSRecord struct {
	ID       uint `json:"id" gorm:"primaryKey"`
	DomainID uint `json:"domain_id" gorm:"index:idx_dnsrec_domain_key,unique;not null"`
	// Name+Type form the managed key within a domain (one ledgered record per
	// name+type). Name is a FQDN.
	Name string `json:"name" gorm:"index:idx_dnsrec_domain_key,unique;not null"`
	Type string `json:"type" gorm:"index:idx_dnsrec_domain_key,unique;not null"` // TXT | A | AAAA | CNAME

	Value   string `json:"value"`
	Purpose string `json:"purpose" gorm:"not null"` // verification | app-address
	// AppID is set for app-address records so they are cleaned up when the app /
	// route is removed.
	AppID *uint `json:"app_id,omitempty" gorm:"index"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
