// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metrics exposes Prometheus collectors and the /metrics handler.
// Feature packages increment counters via OnX callback hooks.
package metrics

import (
	"net/http"

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

// Network subnet pool utilization: how many pool subnets are allocated/reserved
// vs the pool's total capacity. Lets operators alert before exhaustion (enlarge
// MIABI_NETWORK_POOL_CIDR).
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

// GPU inventory + allocation: how many devices are discovered vs enabled for
// workloads, and how many GPU units running apps currently hold. Lets operators
// see fleet GPU capacity and utilization.
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

func init() {
	prometheus.MustRegister(buildInfo, subnetPoolUsed, subnetPoolTotal, gpuDevicesTotal, gpuDevicesEnabled, gpuAllocated)
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

// SetGPUStats records fleet GPU inventory and live allocation for the /metrics
// scrape: total discovered devices, admin-enabled devices, and GPU units held by
// running apps.
func SetGPUStats(total, enabled, allocated int) {
	gpuDevicesTotal.Set(float64(total))
	gpuDevicesEnabled.Set(float64(enabled))
	gpuAllocated.Set(float64(allocated))
}

// Handler returns the Prometheus scrape handler.
func Handler() okapi.HandlerFunc {
	h := promhttp.Handler()
	return func(c *okapi.Context) error {
		h.ServeHTTP(c.Response().(http.ResponseWriter), c.Request())
		return nil
	}
}
