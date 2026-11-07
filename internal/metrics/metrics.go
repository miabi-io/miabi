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

package metrics

import (
	"net/http"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var buildInfo = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "miabi_build_info",
		Help: "Miabi build information; value is always 1.",
	},
	[]string{"version", "commit"},
)

// Lets operators alert before exhaustion (enlarge MIABI_NETWORK_POOL_CIDR).
var (
	subnetPoolUsed = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "miabi_network_subnet_pool_used",
		Help: "Number of network subnets allocated or reserved from the pool.",
	})
	subnetPoolTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "miabi_network_subnet_pool_total",
		Help: "Total number of subnets the network pool can hold.",
	})
)

var (
	gpuDevicesTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "miabi_gpu_devices_total",
		Help: "Number of physical GPU devices discovered across all nodes.",
	})
	gpuDevicesEnabled = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "miabi_gpu_devices_enabled",
		Help: "Number of GPU devices enabled (offered to workloads).",
	})
	gpuAllocated = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "miabi_gpu_allocated",
		Help: "GPU units currently held by running applications.",
	})
)

// So a mistaken second control plane shows as a standby, not a process silently running no jobs.
var leaderHeld = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "miabi_leader",
	Help: "1 while this process holds the named leader lease, 0 while it stands by.",
}, []string{"lease"})

func init() {
	prometheus.MustRegister(buildInfo, subnetPoolUsed, subnetPoolTotal, gpuDevicesTotal, gpuDevicesEnabled, gpuAllocated,
		analyticsIngested, analyticsRejected, leaderHeld,
		controlManagerSweep, controlManagerDrift, controlManagerUnobserved, controlManagerBlocked,
		controlManagerActions)
}

// SetLeader records whether this process holds the named leader lease.
func SetLeader(lease string, held bool) {
	v := 0.0
	if held {
		v = 1
	}
	leaderHeld.WithLabelValues(lease).Set(v)
}

// These tell whether observe mode is quiet enough to act on its findings.
var (
	controlManagerSweep = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "miabi_control_manager_sweep_duration_seconds",
		Help:    "Duration of a control manager sweep.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 11),
	})
	controlManagerDrift = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "miabi_control_manager_drift_items",
		Help: "Workloads the last control manager sweep confirmed missing.",
	}, []string{"class", "kind"})
	controlManagerUnobserved = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "miabi_control_manager_unobserved",
		Help: "Nodes and clusters the last control manager sweep could not observe.",
	}, []string{"scope"})
	controlManagerBlocked = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "miabi_control_manager_blocked_apps",
		Help: "Apps that must not be redeployed because the volume holding their data is gone.",
	})
	controlManagerActions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "miabi_control_manager_actions_total",
		Help: "Actions the control manager took on drift, by action and outcome.",
	}, []string{"action", "result"})
)

// Shows whether the control manager is helping or thrashing.
func ControlManagerAction(action, result string) {
	controlManagerActions.WithLabelValues(action, result).Inc()
}

// ObserveControlManagerSweep records how long a control manager sweep took.
func ObserveControlManagerSweep(d time.Duration) {
	controlManagerSweep.Observe(d.Seconds())
}

type ControlManagerDrift struct {
	MissingContainers  int
	MissingServices    int
	MissingVolumes     int
	ReplacedVolumes    int
	MissingGateways    int
	BlockedApps        int
	UnobservedNodes    int
	UnobservedClusters int
}

// SetControlManagerDrift records the last control manager sweep.
func SetControlManagerDrift(d ControlManagerDrift) {
	controlManagerDrift.WithLabelValues("missing", "container").Set(float64(d.MissingContainers))
	controlManagerDrift.WithLabelValues("missing", "service").Set(float64(d.MissingServices))
	controlManagerDrift.WithLabelValues("missing", "volume").Set(float64(d.MissingVolumes))
	controlManagerDrift.WithLabelValues("replaced", "volume").Set(float64(d.ReplacedVolumes))
	controlManagerDrift.WithLabelValues("missing", "gateway").Set(float64(d.MissingGateways))
	controlManagerBlocked.Set(float64(d.BlockedApps))
	controlManagerUnobserved.WithLabelValues("node").Set(float64(d.UnobservedNodes))
	controlManagerUnobserved.WithLabelValues("cluster").Set(float64(d.UnobservedClusters))
}

// SetBuildInfo records the running build's version and commit.
func SetBuildInfo(version, commit string) {
	buildInfo.WithLabelValues(version, commit).Set(1)
}

// SetSubnetPoolUsage records network-pool utilization for the /metrics scrape.
func SetSubnetPoolUsage(used, total int) {
	subnetPoolUsed.Set(float64(used))
	subnetPoolTotal.Set(float64(total))
}

func SetGPUStats(total, enabled, allocated int) {
	gpuDevicesTotal.Set(float64(total))
	gpuDevicesEnabled.Set(float64(enabled))
	gpuAllocated.Set(float64(allocated))
}

// A node reporting routes it does not serve is a compromise signal, so rejects are counted per node.
var (
	analyticsIngested = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "miabi_analytics_ingested_events_total",
		Help: "Gateway request events accepted from edge nodes.",
	}, []string{"node"})
	analyticsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "miabi_analytics_rejected_events_total",
		Help: "Events dropped because the node does not serve the route they claim.",
	}, []string{"node"})
)

// IngestAccepted records events accepted from a node.
func IngestAccepted(node string, n int) {
	if n > 0 {
		analyticsIngested.WithLabelValues(node).Add(float64(n))
	}
}

// IngestRejected records events dropped for an unowned route.
func IngestRejected(node string, n int) {
	if n > 0 {
		analyticsRejected.WithLabelValues(node).Add(float64(n))
	}
}

// Handler returns the Prometheus scrape handler.
func Handler() okapi.HandlerFunc {
	h := promhttp.Handler()
	return func(c *okapi.Context) error {
		h.ServeHTTP(c.Response().(http.ResponseWriter), c.Request())
		return nil
	}
}
