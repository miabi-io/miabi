// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package analytics turns Goma Gateway's per-request event stream into
// minute-bucketed rollups (models.AnalyticsRollup) and answers the Traffic,
// Performance and Web Analytics queries over them. It holds no per-request rows
// and no PII: latency is a histogram, uniques are a HyperLogLog sketch, and the
// visitor id is a daily-salted hash produced at the edge (never an IP).
package analytics

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/miabi-io/miabi/internal/models"
)

// Event is one request as emitted by Goma onto the analytics stream. Field tags
// mirror the gateway's AnalyticsEvent JSON.
type Event struct {
	Ts           int64  `json:"ts"` // unix millis
	Gateway      string `json:"gw"`
	Route        string `json:"name"` // "mb-ws<workspaceID>-<slug>"
	Host         string `json:"host"`
	Method       string `json:"method"`
	Status       int    `json:"status"`
	Path         string `json:"path"`
	PathTemplate string `json:"path_template"`
	ReqBytes     int64  `json:"req_bytes"`
	RespBytes    int64  `json:"resp_bytes"`
	DurationMs   int64  `json:"duration_ms"`
	UpstreamMs   int64  `json:"upstream_ms"`
	VID          string `json:"vid"`
	Country      string `json:"country"`
	UA           string `json:"ua"`
	RefererHost  string `json:"referer_host"`
}

// LatencyBoundsMs are the histogram bucket upper bounds (milliseconds). A value
// is counted in the first bucket whose bound it does not exceed; anything above
// the last bound lands in a trailing overflow bucket (index len(bounds)).
var LatencyBoundsMs = []int64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// histLen is the histogram length (bounds + one overflow bucket).
func histLen() int { return len(LatencyBoundsMs) + 1 }

// topKCap bounds each categorical count map so a bucket can't grow unbounded on
// a high-cardinality dimension (paths, referrers).
const topKCap = 200

// bucketIndex returns the histogram index for a latency in ms.
func bucketIndex(ms int64) int {
	for i, b := range LatencyBoundsMs {
		if ms <= b {
			return i
		}
	}
	return len(LatencyBoundsMs) // overflow
}

// Percentile estimates the p-th percentile (0..1) latency in ms from a histogram,
// returning each bucket's upper bound (the overflow bucket returns the last bound
// as a floor). Approximate by design — bucketed, cheap, and mergeable.
func Percentile(hist []int64, p float64) float64 {
	var total int64
	for _, c := range hist {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := p * float64(total)
	var cum int64
	for i, c := range hist {
		cum += c
		if float64(cum) >= target {
			if i < len(LatencyBoundsMs) {
				return float64(LatencyBoundsMs[i])
			}
			return float64(LatencyBoundsMs[len(LatencyBoundsMs)-1])
		}
	}
	return float64(LatencyBoundsMs[len(LatencyBoundsMs)-1])
}

// topKAdd adds n to key in m, keeping m bounded: once at cap, a new key only
// displaces the current minimum (approximate top-K, fine for a dashboard).
func topKAdd(m map[string]int64, key string, n int64) {
	if key == "" || n == 0 {
		return
	}
	if _, ok := m[key]; ok || len(m) < topKCap {
		m[key] += n
		return
	}
	// At cap and key is new: evict the smallest if this would beat it.
	var minK string
	var minV int64 = 1<<63 - 1
	for k, v := range m {
		if v < minV {
			minK, minV = k, v
		}
	}
	if n > minV {
		delete(m, minK)
		m[key] = n
	}
}

func mergeTopK(dst, src map[string]int64) {
	for k, v := range src {
		topKAdd(dst, k, v)
	}
}

// classifyUA derives a coarse browser family, OS and device from a User-Agent
// string with cheap substring matching — enough for the analytics breakdowns,
// no dependency, no fingerprinting beyond family. bot is true for common crawlers.
func classifyUA(ua string) (family, os, device string, bot bool) {
	u := strings.ToLower(ua)
	if u == "" {
		return "Unknown", "Unknown", "unknown", false
	}
	switch {
	case containsAny(u, "bot", "crawler", "spider", "slurp", "bingpreview", "headless", "curl", "wget", "python-requests", "go-http-client"):
		return "Bot", osFamily(u), "bot", true
	}
	switch {
	case strings.Contains(u, "edg/"), strings.Contains(u, "edga/"), strings.Contains(u, "edgios/"):
		family = "Edge"
	case strings.Contains(u, "opr/"), strings.Contains(u, "opera"):
		family = "Opera"
	case strings.Contains(u, "firefox"), strings.Contains(u, "fxios"):
		family = "Firefox"
	case strings.Contains(u, "chrome"), strings.Contains(u, "crios"):
		family = "Chrome"
	case strings.Contains(u, "safari"):
		family = "Safari"
	default:
		family = "Other"
	}
	os = osFamily(u)
	device = "desktop"
	if containsAny(u, "mobile", "iphone", "android", "ipod") {
		device = "mobile"
	} else if containsAny(u, "ipad", "tablet") {
		device = "tablet"
	}
	return family, os, device, false
}

func osFamily(u string) string {
	switch {
	case strings.Contains(u, "windows"):
		return "Windows"
	case containsAny(u, "iphone", "ipad", "ipod", "ios"):
		return "iOS"
	case strings.Contains(u, "mac os"), strings.Contains(u, "macintosh"):
		return "macOS"
	case strings.Contains(u, "android"):
		return "Android"
	case strings.Contains(u, "linux"):
		return "Linux"
	default:
		return "Other"
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// WorkspaceIDFromRoute parses the workspace id out of a Goma route name of the
// form "mb-ws<id>-<slug>". Returns 0 when the name isn't in that form (a route
// Miabi doesn't own — e.g. the platform gateway's own route).
func WorkspaceIDFromRoute(name string) uint {
	const prefix = "mb-ws"
	if !strings.HasPrefix(name, prefix) {
		return 0
	}
	rest := name[len(prefix):]
	i := strings.IndexByte(rest, '-')
	if i <= 0 {
		return 0
	}
	n, err := strconv.ParseUint(rest[:i], 10, 64)
	if err != nil {
		return 0
	}
	return uint(n)
}

// bucketAgg is a live in-memory rollup plus its HLL sketch (kept separate because
// the model stores the serialized bytes).
type bucketAgg struct {
	r   *models.AnalyticsRollup
	hll *hyperloglog.Sketch
}

// Aggregator accumulates events into per-(workspace, app, route, minute) buckets
// in memory, and hands over "closed" buckets (older than a grace window) as
// models.AnalyticsRollup for persistence. Safe for concurrent Ingest/Flush.
type Aggregator struct {
	mu      sync.Mutex
	buckets map[string]*bucketAgg
}

func NewAggregator() *Aggregator {
	return &Aggregator{buckets: map[string]*bucketAgg{}}
}

func minuteOf(tsMillis int64) time.Time {
	return time.UnixMilli(tsMillis).UTC().Truncate(time.Minute)
}

func key(ws, app uint, route string, bucket time.Time) string {
	return strconv.FormatUint(uint64(ws), 10) + "|" + strconv.FormatUint(uint64(app), 10) + "|" + route + "|" + strconv.FormatInt(bucket.Unix(), 10)
}

// Ingest folds one event into its bucket. ws/app are resolved by the caller
// (workspace from the route name, app via a route lookup); an unresolved app is 0.
func (a *Aggregator) Ingest(e *Event, ws, app uint) {
	bucket := minuteOf(e.Ts)
	if e.Ts == 0 {
		bucket = time.Now().UTC().Truncate(time.Minute)
	}
	k := key(ws, app, e.Route, bucket)

	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.buckets[k]
	if b == nil {
		b = &bucketAgg{
			r: &models.AnalyticsRollup{
				WorkspaceID: ws, ApplicationID: app, RouteName: e.Route, Bucket: bucket,
				DurationHist: make([]int64, histLen()), UpstreamHist: make([]int64, histLen()),
				TopPaths: map[string]int64{}, TopReferrers: map[string]int64{},
				TopCountries: map[string]int64{}, TopUAFamilies: map[string]int64{},
				TopOS: map[string]int64{}, TopDevice: map[string]int64{}, TopMethods: map[string]int64{},
			},
			hll: hyperloglog.New(),
		}
		a.buckets[k] = b
	}
	r := b.r
	r.Requests++
	r.BytesIn += e.ReqBytes
	r.BytesOut += e.RespBytes
	switch {
	case e.Status >= 500:
		r.Status5xx++
	case e.Status >= 400:
		r.Status4xx++
	case e.Status >= 300:
		r.Status3xx++
	default:
		r.Status2xx++
	}
	r.DurationHist[bucketIndex(e.DurationMs)]++
	r.UpstreamHist[bucketIndex(e.UpstreamMs)]++
	r.DurationSum += e.DurationMs
	r.UpstreamSum += e.UpstreamMs
	if e.VID != "" {
		b.hll.Insert([]byte(e.VID))
	}
	if p := e.PathTemplate; p != "" {
		topKAdd(r.TopPaths, p, 1)
	} else if e.Path != "" {
		topKAdd(r.TopPaths, e.Path, 1)
	}
	topKAdd(r.TopReferrers, e.RefererHost, 1)
	topKAdd(r.TopCountries, e.Country, 1)
	topKAdd(r.TopMethods, e.Method, 1)
	fam, os, device, bot := classifyUA(e.UA)
	if fam != "" {
		topKAdd(r.TopUAFamilies, fam, 1)
	}
	topKAdd(r.TopOS, os, 1)
	topKAdd(r.TopDevice, device, 1)
	if bot {
		r.BotRequests++
	}
}

// Flush removes and returns every bucket strictly older than `before`,
// serializing its HLL sketch into VisitorsHLL. Call with now-grace so an
// in-progress minute keeps accumulating.
func (a *Aggregator) Flush(before time.Time) []*models.AnalyticsRollup {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*models.AnalyticsRollup
	for k, b := range a.buckets {
		if b.r.Bucket.Before(before) {
			if data, err := b.hll.MarshalBinary(); err == nil {
				b.r.VisitorsHLL = data
			}
			out = append(out, b.r)
			delete(a.buckets, k)
		}
	}
	return out
}

// Pending reports how many open buckets are held (for metrics/tests).
func (a *Aggregator) Pending() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.buckets)
}

// Merge adds src into dst: counters, element-wise histograms, sums, top-K maps,
// and the HLL sketch. Used by the repository to combine a flushed bucket with an
// existing row (multi-consumer / re-flush safe).
func Merge(dst, src *models.AnalyticsRollup) {
	dst.Requests += src.Requests
	dst.BytesIn += src.BytesIn
	dst.BytesOut += src.BytesOut
	dst.Status2xx += src.Status2xx
	dst.Status3xx += src.Status3xx
	dst.Status4xx += src.Status4xx
	dst.Status5xx += src.Status5xx
	dst.BotRequests += src.BotRequests
	dst.DurationSum += src.DurationSum
	dst.UpstreamSum += src.UpstreamSum
	dst.DurationHist = addHist(dst.DurationHist, src.DurationHist)
	dst.UpstreamHist = addHist(dst.UpstreamHist, src.UpstreamHist)
	dst.TopPaths = ensureMap(dst.TopPaths)
	dst.TopReferrers = ensureMap(dst.TopReferrers)
	dst.TopCountries = ensureMap(dst.TopCountries)
	dst.TopUAFamilies = ensureMap(dst.TopUAFamilies)
	dst.TopOS = ensureMap(dst.TopOS)
	dst.TopDevice = ensureMap(dst.TopDevice)
	dst.TopMethods = ensureMap(dst.TopMethods)
	mergeTopK(dst.TopPaths, src.TopPaths)
	mergeTopK(dst.TopReferrers, src.TopReferrers)
	mergeTopK(dst.TopCountries, src.TopCountries)
	mergeTopK(dst.TopUAFamilies, src.TopUAFamilies)
	mergeTopK(dst.TopOS, src.TopOS)
	mergeTopK(dst.TopDevice, src.TopDevice)
	mergeTopK(dst.TopMethods, src.TopMethods)
	dst.VisitorsHLL = mergeHLL(dst.VisitorsHLL, src.VisitorsHLL)
}

func addHist(dst, src []int64) []int64 {
	if len(dst) < histLen() {
		grown := make([]int64, histLen())
		copy(grown, dst)
		dst = grown
	}
	for i := range src {
		if i < len(dst) {
			dst[i] += src[i]
		}
	}
	return dst
}

func ensureMap(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

// mergeHLL merges two serialized sketches, returning the serialized result.
func mergeHLL(a, b []byte) []byte {
	sa := sketchFrom(a)
	if b != nil {
		if sb := sketchFrom(b); sb != nil {
			_ = sa.Merge(sb)
		}
	}
	data, err := sa.MarshalBinary()
	if err != nil {
		return a
	}
	return data
}

// sketchFrom deserializes a sketch, or returns a fresh one on nil/error.
func sketchFrom(data []byte) *hyperloglog.Sketch {
	s := hyperloglog.New()
	if len(data) > 0 {
		_ = s.UnmarshalBinary(data)
	}
	return s
}

// UniquesOf returns the estimated unique visitors from a serialized sketch.
func UniquesOf(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	return int64(sketchFrom(data).Estimate())
}

// MergeUniques merges several serialized sketches and returns the estimate — the
// unique-visitor count over a range is the merge of its buckets' sketches.
func MergeUniques(sketches [][]byte) int64 {
	acc := hyperloglog.New()
	for _, s := range sketches {
		if len(s) == 0 {
			continue
		}
		_ = acc.Merge(sketchFrom(s))
	}
	return int64(acc.Estimate())
}
