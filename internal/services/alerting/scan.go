// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package alerting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Scan cadence and TLS thresholds.
const (
	scanInterval       = 30 * time.Minute
	certExpiryWarn     = 14 * 24 * time.Hour
	certExpiryCritical = 3 * 24 * time.Hour
)

// CertLister is the scanner's view of certificates (the certificate repo).
type CertLister interface {
	// ListExpiringBefore returns certs whose NotAfter is before cutoff (all workspaces).
	ListExpiringBefore(cutoff time.Time) ([]models.Certificate, error)
	// ListByStatus returns certs in a given status (e.g. "failed").
	ListByStatus(status string) ([]models.Certificate, error)
}

// SetCertLister enables the TLS-expiry / issuance-failure scan.
func (e *Engine) SetCertLister(c CertLister) { e.certs = c }

// ScanLoop runs the periodic, self-contained condition scans (time-based catalog
// rules that aren't event-driven: TLS expiry today; backup-overdue and quota
// scans slot in the same way). Returns when ctx is cancelled.
func (e *Engine) ScanLoop(ctx context.Context) {
	// A scan on start so a fresh control plane surfaces existing conditions
	// immediately rather than after the first interval.
	e.scan(ctx)
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.scan(ctx)
		}
	}
}

func (e *Engine) scan(ctx context.Context) {
	if e.certs != nil {
		e.scanCerts(ctx)
	}
	if e.volumes != nil {
		e.scanVolumes(ctx)
	}
	if e.quota != nil {
		e.scanQuotas(ctx)
	}
}

// --- Disk / volume usage ----------------------------------------------------

const (
	diskWarnRatio     = 0.85
	diskCriticalRatio = 0.95
)

// VolumeUsageLister is the scanner's view of volumes (the volume repo). Usage is
// the last measured on-disk size (docker system df), compared to the declared
// capacity.
type VolumeUsageLister interface {
	ListAll() ([]models.Volume, error)
}

// SetVolumeLister enables the disk-usage scan.
func (e *Engine) SetVolumeLister(v VolumeUsageLister) { e.volumes = v }

// scanVolumes fires "disk near full" (warning ≥ 85%, critical ≥ 95%) for volumes
// with a declared capacity and a measured usage, and resolves when usage drops
// back below the warning threshold (or capacity was raised).
func (e *Engine) scanVolumes(ctx context.Context) {
	vols, err := e.volumes.ListAll()
	if err != nil {
		logger.Warn("alerting: disk scan failed", "error", err)
		return
	}
	fired := map[string]bool{}
	for i := range vols {
		v := &vols[i]
		if v.SizeBytes <= 0 || v.UsedBytes <= 0 || v.UsedMeasuredAt == nil {
			continue // unlimited or never measured — nothing to compare
		}
		ratio := float64(v.UsedBytes) / float64(v.SizeBytes)
		if ratio < diskWarnRatio {
			continue
		}
		sev := models.AlertWarning
		if ratio >= diskCriticalRatio {
			sev = models.AlertCritical
		}
		key := signalDedup("disk_near", fmt.Sprintf("volume:%d", v.ID))
		fired[key] = true
		e.doFire(ctx, v.WorkspaceID, intent{
			kind: fire, ruleKey: "disk_near", dedupKey: key,
			category: models.CategoryStorage, severity: sev,
			subjectType: "volume", subjectRef: fmt.Sprintf("volume:%d", v.ID),
			subjectLink: fmt.Sprintf("/volumes/%d", v.ID), minRole: models.WorkspaceRoleDeveloper,
			title: fmt.Sprintf("Volume %d%% full — %s", int(ratio*100), volName(v)),
			body:  "The volume is near capacity. Resize it or prune data.",
		})
	}
	e.resolveStale(ctx, models.CategoryStorage, "disk_near:", fired)
}

func volName(v *models.Volume) string {
	if v.DisplayName != "" {
		return v.DisplayName
	}
	return v.Name
}

// --- Quotas -----------------------------------------------------------------

// QuotaBreach is one workspace resource at/over the near-limit threshold.
type QuotaBreach struct {
	WorkspaceID uint
	Resource    string // human label, e.g. "apps"
	Used        int
	Limit       int
}

// QuotaLister reports workspace resources at/over a usage fraction (0..1).
type QuotaLister interface {
	NearQuota(threshold float64) ([]QuotaBreach, error)
}

// SetQuotaLister enables the quota near-limit scan.
func (e *Engine) SetQuotaLister(q QuotaLister) { e.quota = q }

const quotaNearThreshold = 0.9

// scanQuotas fires "approaching quota" (warning) for workspace resources past 90%
// of their plan limit, and resolves when usage drops back below.
func (e *Engine) scanQuotas(ctx context.Context) {
	breaches, err := e.quota.NearQuota(quotaNearThreshold)
	if err != nil {
		logger.Warn("alerting: quota scan failed", "error", err)
		return
	}
	fired := map[string]bool{}
	for _, b := range breaches {
		ref := fmt.Sprintf("quota:%s", b.Resource)
		key := signalDedup("quota_near", ref)
		fired[key] = true
		e.doFire(ctx, b.WorkspaceID, intent{
			kind: fire, ruleKey: "quota_near", dedupKey: key,
			category: models.CategoryQuota, severity: models.AlertWarning,
			subjectType: "quota", subjectRef: ref, subjectLink: "/workspaces",
			minRole: models.WorkspaceRoleAdmin,
			title:   fmt.Sprintf("Approaching %s quota", b.Resource),
			body:    fmt.Sprintf("Using %d of %d %s. Upgrade the plan or free resources.", b.Used, b.Limit, b.Resource),
		})
	}
	e.resolveStale(ctx, models.CategoryQuota, "quota_near:", fired)
}

// resolveStale resolves every active alert of a category whose dedup key has the
// given prefix and is not in the current fired set — the generic "condition no
// longer holds" reconciliation used by all scans.
func (e *Engine) resolveStale(ctx context.Context, category models.AlertCategory, prefix string, fired map[string]bool) {
	active, err := e.alerts.ListActiveByCategory(category)
	if err != nil {
		return
	}
	for i := range active {
		a := &active[i]
		if strings.HasPrefix(a.DedupKey, prefix) && !fired[a.DedupKey] {
			e.doResolve(ctx, a.WorkspaceID, a.DedupKey)
		}
	}
}

// scanCerts reconciles TLS alerts against the current certificate state: it fires
// "expiring" (warning < 14d, critical < 3d) and "issuance failed" alerts, and
// resolves any active TLS alert whose condition no longer holds (the cert was
// renewed or re-issued). Reconciliation via set-difference means a renewed cert
// auto-resolves without the producer tracking prior state.
func (e *Engine) scanCerts(ctx context.Context) {
	now := e.now().UTC()

	expiring, err := e.certs.ListExpiringBefore(now.Add(certExpiryWarn))
	if err != nil {
		logger.Warn("alerting: cert expiry scan failed", "error", err)
		return
	}
	failed, err := e.certs.ListByStatus(models.CertStatusFailed)
	if err != nil {
		logger.Warn("alerting: cert failure scan failed", "error", err)
		return
	}

	firedExpiring := map[string]bool{}
	for i := range expiring {
		c := &expiring[i]
		sev := models.AlertWarning
		if c.NotAfter.Sub(now) < certExpiryCritical {
			sev = models.AlertCritical
		}
		key := signalDedup("cert_expiring", certRef(c.ID))
		firedExpiring[key] = true
		e.doFire(ctx, c.WorkspaceID, certIntent(c, "cert_expiring", key, sev,
			fmt.Sprintf("TLS certificate expiring — %s", certName(c)),
			fmt.Sprintf("Expires %s. Check DNS and renewal.", c.NotAfter.Format("2006-01-02"))))
	}

	firedFailed := map[string]bool{}
	for i := range failed {
		c := &failed[i]
		key := signalDedup("cert_failed", certRef(c.ID))
		firedFailed[key] = true
		body := "Issuance or renewal failed."
		if c.LastError != "" {
			body = c.LastError
		}
		e.doFire(ctx, c.WorkspaceID, certIntent(c, "cert_failed", key, models.AlertCritical,
			fmt.Sprintf("Certificate issuance failed — %s", certName(c)), body))
	}

	// Auto-resolve: any active TLS alert whose condition is no longer present.
	active, err := e.alerts.ListActiveByCategory(models.CategoryTLS)
	if err != nil {
		return
	}
	for i := range active {
		a := &active[i]
		switch {
		case strings.HasPrefix(a.DedupKey, "cert_expiring:") && !firedExpiring[a.DedupKey]:
			e.doResolve(ctx, a.WorkspaceID, a.DedupKey)
		case strings.HasPrefix(a.DedupKey, "cert_failed:") && !firedFailed[a.DedupKey]:
			e.doResolve(ctx, a.WorkspaceID, a.DedupKey)
		}
	}
}

func certRef(id uint) string  { return fmt.Sprintf("cert:%d", id) }
func certLink(id uint) string { return fmt.Sprintf("/certificates/%d", id) }

func certName(c *models.Certificate) string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	if c.CommonName != "" {
		return c.CommonName
	}
	return c.Name
}

func certIntent(c *models.Certificate, ruleKey, dedupKey string, sev models.AlertSeverity, title, body string) intent {
	return intent{
		kind:        fire,
		ruleKey:     ruleKey,
		dedupKey:    dedupKey,
		category:    models.CategoryTLS,
		severity:    sev,
		subjectType: "certificate",
		subjectRef:  certRef(c.ID),
		subjectLink: certLink(c.ID),
		minRole:     models.WorkspaceRoleDeveloper,
		title:       title,
		body:        body,
	}
}
