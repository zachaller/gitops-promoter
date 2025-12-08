/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package promotionstatus

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// requestsTotal counts the total number of API requests.
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "requests_total",
			Help:      "Total number of API requests to the aggregated API server.",
		},
		[]string{"operation", "namespace", "status"},
	)

	// requestDuration measures the duration of API requests.
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "request_duration_seconds",
			Help:      "Duration of API requests to the aggregated API server.",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to ~16s
		},
		[]string{"operation", "namespace"},
	)

	// cacheHits counts cache hits.
	cacheHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "cache_hits_total",
			Help:      "Total number of cache hits.",
		},
		[]string{"namespace"},
	)

	// cacheMisses counts cache misses.
	cacheMisses = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "cache_misses_total",
			Help:      "Total number of cache misses.",
		},
		[]string{"namespace"},
	)

	// cacheSize tracks the current size of the cache.
	cacheSize = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "cache_size",
			Help:      "Current number of entries in the cache.",
		},
	)

	// aggregationDuration measures the time to aggregate data from multiple sources.
	aggregationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "aggregation_duration_seconds",
			Help:      "Duration to aggregate data from underlying resources.",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 12), // 1ms to ~2s
		},
		[]string{"namespace", "promotion_strategy"},
	)

	// resourceFetchErrors counts errors when fetching underlying resources.
	resourceFetchErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "promoter",
			Subsystem: "aggregated_api",
			Name:      "resource_fetch_errors_total",
			Help:      "Total number of errors when fetching underlying resources.",
		},
		[]string{"resource_type", "namespace"},
	)
)

func init() {
	// Register metrics with the controller-runtime metrics registry
	metrics.Registry.MustRegister(
		requestsTotal,
		requestDuration,
		cacheHits,
		cacheMisses,
		cacheSize,
		aggregationDuration,
		resourceFetchErrors,
	)
}

// RecordRequest records metrics for an API request.
func RecordRequest(operation, namespace, status string, durationSeconds float64) {
	requestsTotal.WithLabelValues(operation, namespace, status).Inc()
	requestDuration.WithLabelValues(operation, namespace).Observe(durationSeconds)
}

// RecordCacheHit records a cache hit.
func RecordCacheHit(namespace string) {
	cacheHits.WithLabelValues(namespace).Inc()
}

// RecordCacheMiss records a cache miss.
func RecordCacheMiss(namespace string) {
	cacheMisses.WithLabelValues(namespace).Inc()
}

// RecordCacheSize records the current cache size.
func RecordCacheSize(size int) {
	cacheSize.Set(float64(size))
}

// RecordAggregationDuration records the time to aggregate data.
func RecordAggregationDuration(namespace, promotionStrategy string, durationSeconds float64) {
	aggregationDuration.WithLabelValues(namespace, promotionStrategy).Observe(durationSeconds)
}

// RecordResourceFetchError records an error when fetching a resource.
func RecordResourceFetchError(resourceType, namespace string) {
	resourceFetchErrors.WithLabelValues(resourceType, namespace).Inc()
}
