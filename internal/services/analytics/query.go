// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"sort"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

// Report is the combined analytics answer for a workspace (optionally one app)
// over a time range: the Traffic, Performance and Web Analytics pillars share one
// pass over the same rollups so the three dashboard tabs come from a single query.
type Report struct {
	Range       Window        `json:"range"`
	Granularity string        `json:"granularity"` // "minute" | "hour" | "day"
	Totals      Totals        `json:"totals"`
	Series      []SeriesPoint `json:"series"`
	Status      StatusBreak   `json:"status"`
	Performance Performance   `json:"performance"`
	Web         WebAnalytics  `json:"web"`
	// Compare holds the totals of the immediately preceding, equal-length window,
	// so the UI can render period-over-period deltas. Nil when not requested.
	Compare *Totals `json:"compare,omitempty"`
	// RetentionDays is the effective retention cap in days (-1 = unlimited), and
	// Exportable reports whether analytics export is entitled. Both are set by the
	// handler from the license so the UI can bound the range picker and show the
	// Enterprise upgrade hints. They are edition metadata, not computed from rows.
	RetentionDays int  `json:"retention_days"`
	Exportable    bool `json:"exportable"`
}

type Window struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// Totals are the headline traffic numbers over the whole range.
type Totals struct {
	Requests    int64   `json:"requests"`
	BytesIn     int64   `json:"bytes_in"`
	BytesOut    int64   `json:"bytes_out"`
	UniqueVisit int64   `json:"unique_visitors"`
	ErrorRate   float64 `json:"error_rate"` // (4xx+5xx)/requests, 0..1
	AvgLatency  float64 `json:"avg_latency_ms"`
	P50Latency  float64 `json:"p50_latency_ms"`
	P95Latency  float64 `json:"p95_latency_ms"`
	P99Latency  float64 `json:"p99_latency_ms"`
}

// SeriesPoint is one time bucket of the request/latency time series at the chosen
// granularity.
type SeriesPoint struct {
	T          time.Time `json:"t"`
	Requests   int64     `json:"requests"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	Errors     int64     `json:"errors"`     // 4xx+5xx (kept for compatibility)
	Errors4xx  int64     `json:"errors_4xx"` // client errors
	Errors5xx  int64     `json:"errors_5xx"` // server errors
	Uniques    int64     `json:"unique_visitors"`
	AvgLatency float64   `json:"avg_latency_ms"`
	P95Latency float64   `json:"p95_latency_ms"`
}

type StatusBreak struct {
	S2xx int64 `json:"s2xx"`
	S3xx int64 `json:"s3xx"`
	S4xx int64 `json:"s4xx"`
	S5xx int64 `json:"s5xx"`
}

// Performance covers the latency pillar: request vs upstream percentiles plus the
// slowest routes by p95.
type Performance struct {
	RequestP50  float64 `json:"request_p50_ms"`
	RequestP95  float64 `json:"request_p95_ms"`
	RequestP99  float64 `json:"request_p99_ms"`
	UpstreamP50 float64 `json:"upstream_p50_ms"`
	UpstreamP95 float64 `json:"upstream_p95_ms"`
	UpstreamP99 float64 `json:"upstream_p99_ms"`
	// Mean total, upstream (backend) and gateway overhead (total − upstream) in ms.
	AvgRequestMs  float64     `json:"avg_request_ms"`
	AvgUpstreamMs float64     `json:"avg_upstream_ms"`
	AvgOverheadMs float64     `json:"avg_overhead_ms"`
	SlowRoutes    []RouteStat `json:"slow_routes"`
}

// RouteStat is a per-route performance summary (used for the slowest-routes list).
type RouteStat struct {
	Route     string  `json:"route"`
	Requests  int64   `json:"requests"`
	P95       float64 `json:"p95_latency_ms"`
	ErrorRate float64 `json:"error_rate"`
}

// WebAnalytics is the cookieless web pillar: uniques plus the categorical
// breakdowns (paths, referrers, countries, browsers, methods).
type WebAnalytics struct {
	UniqueVisitors int64      `json:"unique_visitors"`
	BotRequests    int64      `json:"bot_requests"`
	HumanRequests  int64      `json:"human_requests"`
	TopPaths       []Category `json:"top_paths"`
	TopReferrers   []Category `json:"top_referrers"`
	TopCountries   []Category `json:"top_countries"`
	TopBrowsers    []Category `json:"top_browsers"`
	TopOS          []Category `json:"top_os"`
	TopDevices     []Category `json:"top_devices"`
	TopMethods     []Category `json:"top_methods"`
}

// Category is one labelled count in a breakdown.
type Category struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// truncFunc truncates a bucket timestamp to the report granularity.
type truncFunc func(time.Time) time.Time

// granularityFor picks a sensible bucket size for the range span so the series
// stays readable: minute up to ~3h, hour up to ~4d, day beyond.
func granularityFor(span time.Duration) (string, truncFunc) {
	switch {
	case span <= 3*time.Hour:
		return "minute", func(t time.Time) time.Time { return t.Truncate(time.Minute) }
	case span <= 4*24*time.Hour:
		return "hour", func(t time.Time) time.Time { return t.Truncate(time.Hour) }
	default:
		return "day", func(t time.Time) time.Time {
			y, m, d := t.UTC().Date()
			return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		}
	}
}

// seriesAgg accumulates one output time bucket.
type seriesAgg struct {
	requests, bytesIn, bytesOut, errors, errors4xx, errors5xx int64
	durHist                                                   []int64
	durSum                                                    int64
	sketches                                                  [][]byte
}

// BuildReport reduces the raw minute rollups of a range into the combined
// dashboard report. granularity is auto-selected from the span. It performs one
// pass for the totals/status/web breakdowns and a grouped pass for the series,
// merging latency histograms for percentiles and HLL sketches for uniques.
func BuildReport(rows []models.AnalyticsRollup, since, until time.Time) Report {
	gran, trunc := granularityFor(until.Sub(since))
	rep := Report{
		Range:       Window{Since: since, Until: until},
		Granularity: gran,
	}

	totalDur := make([]int64, histLen())
	reqPerf := make([]int64, histLen())
	upPerf := make([]int64, histLen())
	paths := map[string]int64{}
	referrers := map[string]int64{}
	countries := map[string]int64{}
	browsers := map[string]int64{}
	oses := map[string]int64{}
	devices := map[string]int64{}
	methods := map[string]int64{}
	var upstreamSum, botReqs int64
	var allSketches [][]byte
	series := map[int64]*seriesAgg{}
	routes := map[string]*RouteStat{}
	routeHist := map[string][]int64{}
	routeErr := map[string]int64{}

	for i := range rows {
		row := &rows[i]
		rep.Totals.Requests += row.Requests
		rep.Totals.BytesIn += row.BytesIn
		rep.Totals.BytesOut += row.BytesOut
		rep.Status.S2xx += row.Status2xx
		rep.Status.S3xx += row.Status3xx
		rep.Status.S4xx += row.Status4xx
		rep.Status.S5xx += row.Status5xx
		rep.Totals.AvgLatency += float64(row.DurationSum) // sum now, divide later
		upstreamSum += row.UpstreamSum
		botReqs += row.BotRequests
		totalDur = addHist(totalDur, row.DurationHist)
		reqPerf = addHist(reqPerf, row.DurationHist)
		upPerf = addHist(upPerf, row.UpstreamHist)
		mergeTopK(paths, row.TopPaths)
		mergeTopK(referrers, row.TopReferrers)
		mergeTopK(countries, row.TopCountries)
		mergeTopK(browsers, row.TopUAFamilies)
		mergeTopK(oses, row.TopOS)
		mergeTopK(devices, row.TopDevice)
		mergeTopK(methods, row.TopMethods)
		if len(row.VisitorsHLL) > 0 {
			allSketches = append(allSketches, row.VisitorsHLL)
		}

		// Time series bucket.
		key := trunc(row.Bucket).Unix()
		sp := series[key]
		if sp == nil {
			sp = &seriesAgg{durHist: make([]int64, histLen())}
			series[key] = sp
		}
		sp.requests += row.Requests
		sp.bytesIn += row.BytesIn
		sp.bytesOut += row.BytesOut
		sp.errors += row.Status4xx + row.Status5xx
		sp.errors4xx += row.Status4xx
		sp.errors5xx += row.Status5xx
		sp.durSum += row.DurationSum
		sp.durHist = addHist(sp.durHist, row.DurationHist)
		if len(row.VisitorsHLL) > 0 {
			sp.sketches = append(sp.sketches, row.VisitorsHLL)
		}

		// Per-route (for slowest-routes).
		rs := routes[row.RouteName]
		if rs == nil {
			rs = &RouteStat{Route: row.RouteName}
			routes[row.RouteName] = rs
			routeHist[row.RouteName] = make([]int64, histLen())
		}
		rs.Requests += row.Requests
		routeHist[row.RouteName] = addHist(routeHist[row.RouteName], row.DurationHist)
		routeErr[row.RouteName] += row.Status4xx + row.Status5xx
	}

	// Totals: latency percentiles + averages + error rate + uniques.
	if rep.Totals.Requests > 0 {
		rep.Totals.AvgLatency = rep.Totals.AvgLatency / float64(rep.Totals.Requests)
		rep.Totals.ErrorRate = float64(rep.Status.S4xx+rep.Status.S5xx) / float64(rep.Totals.Requests)
	} else {
		rep.Totals.AvgLatency = 0
	}
	rep.Totals.P50Latency = Percentile(totalDur, 0.50)
	rep.Totals.P95Latency = Percentile(totalDur, 0.95)
	rep.Totals.P99Latency = Percentile(totalDur, 0.99)
	rep.Totals.UniqueVisit = MergeUniques(allSketches)

	// Performance pillar.
	rep.Performance = Performance{
		RequestP50:  Percentile(reqPerf, 0.50),
		RequestP95:  Percentile(reqPerf, 0.95),
		RequestP99:  Percentile(reqPerf, 0.99),
		UpstreamP50: Percentile(upPerf, 0.50),
		UpstreamP95: Percentile(upPerf, 0.95),
		UpstreamP99: Percentile(upPerf, 0.99),
		SlowRoutes:  slowRoutes(routes, routeHist, routeErr),
	}
	if rep.Totals.Requests > 0 {
		rep.Performance.AvgRequestMs = rep.Totals.AvgLatency
		rep.Performance.AvgUpstreamMs = float64(upstreamSum) / float64(rep.Totals.Requests)
		// Gateway overhead never goes negative (clock skew / rounding on tiny values).
		if o := rep.Performance.AvgRequestMs - rep.Performance.AvgUpstreamMs; o > 0 {
			rep.Performance.AvgOverheadMs = o
		}
	}

	// Web pillar.
	rep.Web = WebAnalytics{
		UniqueVisitors: rep.Totals.UniqueVisit,
		BotRequests:    botReqs,
		HumanRequests:  rep.Totals.Requests - botReqs,
		TopPaths:       topN(paths, 15),
		TopReferrers:   topN(referrers, 15),
		TopCountries:   topN(countries, 15),
		TopBrowsers:    topN(browsers, 10),
		TopOS:          topN(oses, 10),
		TopDevices:     topN(devices, 10),
		TopMethods:     topN(methods, 10),
	}

	// Series, ordered by time and filled for the mean/p95.
	keys := make([]int64, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	rep.Series = make([]SeriesPoint, 0, len(keys))
	for _, k := range keys {
		sp := series[k]
		pt := SeriesPoint{
			T:          time.Unix(k, 0).UTC(),
			Requests:   sp.requests,
			BytesIn:    sp.bytesIn,
			BytesOut:   sp.bytesOut,
			Errors:     sp.errors,
			Errors4xx:  sp.errors4xx,
			Errors5xx:  sp.errors5xx,
			Uniques:    MergeUniques(sp.sketches),
			P95Latency: Percentile(sp.durHist, 0.95),
		}
		if sp.requests > 0 {
			pt.AvgLatency = float64(sp.durSum) / float64(sp.requests)
		}
		rep.Series = append(rep.Series, pt)
	}
	return rep
}

// slowRoutes returns the routes with the highest p95 latency (top 10), each with
// its request count and error rate.
func slowRoutes(routes map[string]*RouteStat, hist map[string][]int64, errs map[string]int64) []RouteStat {
	out := make([]RouteStat, 0, len(routes))
	for name, rs := range routes {
		rs.P95 = Percentile(hist[name], 0.95)
		if rs.Requests > 0 {
			rs.ErrorRate = float64(errs[name]) / float64(rs.Requests)
		}
		out = append(out, *rs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P95 != out[j].P95 {
			return out[i].P95 > out[j].P95
		}
		return out[i].Requests > out[j].Requests
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// topN returns the N highest-count entries of a breakdown map, descending.
func topN(m map[string]int64, n int) []Category {
	out := make([]Category, 0, len(m))
	for k, v := range m {
		out = append(out, Category{Label: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
