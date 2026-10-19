package metrics

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type families map[string]*dto.MetricFamily

func gather(t *testing.T, recorder *Metrics) families {
	t.Helper()

	gathered, err := recorder.Registry().Gather()
	require.NoError(t, err)

	byName := make(families, len(gathered))
	for _, family := range gathered {
		byName[family.GetName()] = family
	}

	return byName
}

// gauge returns the value of the only sample of an unlabelled gauge.
func (f families) gauge(t *testing.T, name string) float64 {
	t.Helper()

	family, ok := f[name]
	require.True(t, ok, "metric family %s is missing", name)
	require.Len(t, family.GetMetric(), 1)

	return family.GetMetric()[0].GetGauge().GetValue()
}

func labelsOf(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}

	return labels
}

// sample returns the sample whose labels equal want exactly.
func (f families) sample(
	t *testing.T,
	name string,
	want map[string]string,
) *dto.Metric {
	t.Helper()

	family, ok := f[name]
	require.True(t, ok, "metric family %s is missing", name)

	for _, metric := range family.GetMetric() {
		if assert.ObjectsAreEqual(want, labelsOf(metric)) {
			return metric
		}
	}

	require.Failf(t, "no sample", "labels %v not found in %s", want, name)

	return nil
}

func TestNewRegistersRuntimeCollectors(t *testing.T) {
	t.Parallel()

	gathered := gather(t, New(Gauges{}))

	assert.Contains(t, gathered, "go_goroutines")
	assert.Contains(t, gathered, "process_cpu_seconds_total")
}

func TestRegistryIsTheOneTheRecorderWritesTo(t *testing.T) {
	t.Parallel()

	recorder := New(Gauges{})
	recorder.ObserveHTTP("GET", "GET /v1/node/info", 200, 0.1)

	_, err := recorder.Registry().Gather()
	require.NoError(t, err)

	assert.Same(t, recorder.registry, recorder.Registry())
	assert.Contains(t, gather(t, recorder), "monero_api_http_requests_total")
}

func TestGaugesAreReadAtScrapeTime(t *testing.T) {
	t.Parallel()

	var (
		open     = 1.0
		height   = 100.0
		restarts = 0.0
		synced   = 0.0
	)

	recorder := New(Gauges{
		OpenWallets:    func() float64 { return open },
		NodeHeight:     func() float64 { return height },
		NodeRestarts:   func() float64 { return restarts },
		NodeSynced:     func() float64 { return synced },
		MaxOpenWallets: 8,
	})

	first := gather(t, recorder)
	assert.InDelta(t, 1, first.gauge(t, "monero_open_wallets"), 0)
	assert.InDelta(t, 100, first.gauge(t, "monero_node_height"), 0)
	assert.InDelta(t, 0, first.gauge(t, "monero_node_restarts"), 0)
	assert.InDelta(t, 0, first.gauge(t, "monero_node_synchronized"), 0)
	assert.InDelta(t, 8, first.gauge(t, "monero_max_open_wallets"), 0)

	open, height, restarts, synced = 3, 250, 2, 1

	second := gather(t, recorder)
	assert.InDelta(t, 3, second.gauge(t, "monero_open_wallets"), 0)
	assert.InDelta(t, 250, second.gauge(t, "monero_node_height"), 0)
	assert.InDelta(t, 2, second.gauge(t, "monero_node_restarts"), 0)
	assert.InDelta(t, 1, second.gauge(t, "monero_node_synchronized"), 0)
	assert.InDelta(t, 8, second.gauge(t, "monero_max_open_wallets"), 0)
}

func TestNilGaugeFuncsAreOmitted(t *testing.T) {
	t.Parallel()

	gathered := gather(t, New(Gauges{MaxOpenWallets: 4}))

	for _, name := range []string{
		"monero_open_wallets",
		"monero_node_height",
		"monero_node_restarts",
		"monero_node_synchronized",
	} {
		assert.NotContains(t, gathered, name)
	}

	assert.InDelta(t, 4, gathered.gauge(t, "monero_max_open_wallets"), 0)
}

func TestObserveHTTPCountsPerMethodRouteAndStatus(t *testing.T) {
	t.Parallel()

	recorder := New(Gauges{})
	recorder.ObserveHTTP("GET", "GET /v1/wallets", 200, 0.25)
	recorder.ObserveHTTP("GET", "GET /v1/wallets", 200, 0.75)
	recorder.ObserveHTTP("GET", "GET /v1/wallets", 500, 0.5)
	recorder.ObserveHTTP("POST", "POST /v1/wallets", 201, 1)

	gathered := gather(t, recorder)

	const (
		requests  = "monero_api_http_requests_total"
		durations = "monero_api_http_request_duration_seconds"
	)

	assert.Len(t, gathered[requests].GetMetric(), 3)

	counterCases := []struct {
		labels map[string]string
		want   float64
	}{
		{
			labels: map[string]string{
				"method": "GET", "route": "GET /v1/wallets", "status": "200",
			},
			want: 2,
		},
		{
			labels: map[string]string{
				"method": "GET", "route": "GET /v1/wallets", "status": "500",
			},
			want: 1,
		},
		{
			labels: map[string]string{
				"method": "POST", "route": "POST /v1/wallets", "status": "201",
			},
			want: 1,
		},
	}

	for _, tc := range counterCases {
		got := gathered.sample(t, requests, tc.labels).GetCounter().GetValue()
		assert.InDelta(t, tc.want, got, 0, tc.labels)
	}

	get := gathered.sample(t, durations, map[string]string{
		"method": "GET", "route": "GET /v1/wallets",
	}).GetHistogram()
	assert.EqualValues(t, 3, get.GetSampleCount())
	assert.InDelta(t, 1.5, get.GetSampleSum(), 1e-9)

	post := gathered.sample(t, durations, map[string]string{
		"method": "POST", "route": "POST /v1/wallets",
	}).GetHistogram()
	assert.EqualValues(t, 1, post.GetSampleCount())
	assert.InDelta(t, 1, post.GetSampleSum(), 1e-9)
}

func TestObserveMCPToolCountsPerToolAndOutcome(t *testing.T) {
	t.Parallel()

	recorder := New(Gauges{})
	recorder.ObserveMCPTool("monero_node_info", OutcomeSuccess, 0.1)
	recorder.ObserveMCPTool("monero_node_info", OutcomeSuccess, 0.3)
	recorder.ObserveMCPTool("monero_node_info", OutcomeError, 0.2)
	recorder.ObserveMCPTool("monero_transfer", OutcomeError, 2)

	gathered := gather(t, recorder)

	const (
		calls     = "monero_api_mcp_tool_calls_total"
		durations = "monero_api_mcp_tool_call_duration_seconds"
	)

	assert.Len(t, gathered[calls].GetMetric(), 3)

	counterCases := []struct {
		labels map[string]string
		want   float64
	}{
		{
			labels: map[string]string{
				"tool": "monero_node_info", "outcome": "success",
			},
			want: 2,
		},
		{
			labels: map[string]string{
				"tool": "monero_node_info", "outcome": "error",
			},
			want: 1,
		},
		{
			labels: map[string]string{
				"tool": "monero_transfer", "outcome": "error",
			},
			want: 1,
		},
	}

	for _, tc := range counterCases {
		got := gathered.sample(t, calls, tc.labels).GetCounter().GetValue()
		assert.InDelta(t, tc.want, got, 0, tc.labels)
	}

	info := gathered.sample(t, durations, map[string]string{
		"tool": "monero_node_info",
	}).GetHistogram()
	assert.EqualValues(t, 3, info.GetSampleCount())
	assert.InDelta(t, 0.6, info.GetSampleSum(), 1e-9)
}

func TestOutcomeLabelValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "success", OutcomeSuccess)
	assert.Equal(t, "error", OutcomeError)
}

func TestHTTPInFlightGoesUpAndDown(t *testing.T) {
	t.Parallel()

	recorder := New(Gauges{})
	inFlight := func() float64 {
		return gather(t, recorder).
			gauge(t, "monero_api_http_requests_in_flight")
	}

	assert.InDelta(t, 0, inFlight(), 0)

	recorder.HTTPInFlight().Inc()
	recorder.HTTPInFlight().Inc()
	assert.InDelta(t, 2, inFlight(), 0)

	recorder.HTTPInFlight().Dec()
	assert.InDelta(t, 1, inFlight(), 0)
}

func TestRecordersDoNotShareState(t *testing.T) {
	t.Parallel()

	first := New(Gauges{})
	second := New(Gauges{})

	first.ObserveHTTP("GET", "GET /v1/node/info", 200, 0.1)
	first.HTTPInFlight().Inc()

	gathered := gather(t, second)

	assert.NotContains(t, gathered, "monero_api_http_requests_total")
	assert.InDelta(
		t, 0, gathered.gauge(t, "monero_api_http_requests_in_flight"), 0,
	)
}

func TestSeriesCarryOnlyBoundedLabels(t *testing.T) {
	t.Parallel()

	recorder := New(Gauges{})
	recorder.ObserveHTTP("GET", "GET /v1/node/info", 200, 0.1)
	recorder.ObserveMCPTool("monero_node_info", OutcomeSuccess, 0.1)

	gathered := gather(t, recorder)

	wantLabelNames := map[string][]string{
		"monero_api_http_requests_total": {
			"method", "route", "status",
		},
		"monero_api_http_request_duration_seconds": {
			"method", "route",
		},
		"monero_api_mcp_tool_calls_total": {
			"outcome", "tool",
		},
		"monero_api_mcp_tool_call_duration_seconds": {
			"tool",
		},
	}

	for name, want := range wantLabelNames {
		family, ok := gathered[name]
		require.True(t, ok, name)

		got := make([]string, 0, len(want))
		for label := range labelsOf(family.GetMetric()[0]) {
			got = append(got, label)
		}

		assert.ElementsMatch(t, want, got, name)
	}
}
