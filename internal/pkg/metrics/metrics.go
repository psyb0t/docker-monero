// Package metrics owns the Prometheus registry this process serves on its
// internal metrics listener.
//
// Labels are bounded: routes are OpenAPI path templates, tools are MCP tool
// names, outcomes are a fixed set. Nothing request-specific (wallet names,
// addresses, amounts) ever becomes a label.
package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metric namespaces, one per subsystem.
const (
	namespaceHTTP   = "monero_api_http"
	namespaceMCP    = "monero_api_mcp"
	namespaceMonero = "monero"
)

// Outcome classes for MCP tool calls.
const (
	OutcomeSuccess = "success"
	OutcomeError   = "error"
)

// Label names.
const (
	labelMethod  = "method"
	labelRoute   = "route"
	labelStatus  = "status"
	labelTool    = "tool"
	labelOutcome = "outcome"
)

// Metrics is every collector this process exposes.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight prometheus.Gauge

	mcpCalls    *prometheus.CounterVec
	mcpDuration *prometheus.HistogramVec
}

// Gauges reports live state that is read at scrape time rather than pushed.
type Gauges struct {
	OpenWallets    func() float64
	NodeHeight     func() float64
	NodeRestarts   func() float64
	NodeSynced     func() float64
	MaxOpenWallets float64
}

// New builds a fresh registry with the process and Go runtime collectors.
func New(gauges Gauges) *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)

	recorder := &Metrics{
		registry: registry,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespaceHTTP,
			Name:      "requests_total",
			Help:      "HTTP requests by method, route template, and status.",
		}, []string{labelMethod, labelRoute, labelStatus}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespaceHTTP,
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration by method and route template.",
			Buckets:   prometheus.DefBuckets,
		}, []string{labelMethod, labelRoute}),
		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespaceHTTP,
			Name:      "requests_in_flight",
			Help:      "HTTP requests currently being served.",
		}),
		mcpCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespaceMCP,
			Name:      "tool_calls_total",
			Help:      "MCP tool calls by tool and outcome.",
		}, []string{labelTool, labelOutcome}),
		mcpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespaceMCP,
			Name:      "tool_call_duration_seconds",
			Help:      "MCP tool call duration by tool.",
			Buckets:   prometheus.DefBuckets,
		}, []string{labelTool}),
	}

	registry.MustRegister(
		recorder.httpRequests,
		recorder.httpDuration,
		recorder.httpInFlight,
		recorder.mcpCalls,
		recorder.mcpDuration,
	)

	recorder.registerGauges(gauges)

	return recorder
}

func (m *Metrics) registerGauges(gauges Gauges) {
	funcs := []struct {
		name  string
		help  string
		value func() float64
	}{
		{"open_wallets", "Wallets currently open.", gauges.OpenWallets},
		{
			"node_height",
			"Height of the node's chain at the last scrape.",
			gauges.NodeHeight,
		},
		{
			"node_restarts",
			"Times monerod crashed and was restarted.",
			gauges.NodeRestarts,
		},
		{
			"node_synchronized",
			"1 when monerod reports itself synchronized.",
			gauges.NodeSynced,
		},
	}

	for _, gauge := range funcs {
		if gauge.value == nil {
			continue
		}

		m.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespaceMonero,
			Name:      gauge.name,
			Help:      gauge.help,
		}, gauge.value))
	}

	maxOpen := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespaceMonero,
		Name:      "max_open_wallets",
		Help:      "Configured open-wallet ceiling.",
	})
	maxOpen.Set(gauges.MaxOpenWallets)
	m.registry.MustRegister(maxOpen)
}

// Registry is the registry the metrics listener serves.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// HTTPInFlight tracks requests being served.
func (m *Metrics) HTTPInFlight() prometheus.Gauge {
	return m.httpInFlight
}

// ObserveHTTP records one completed request.
func (m *Metrics) ObserveHTTP(
	method, route string,
	status int,
	seconds float64,
) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(seconds)
}

// ObserveMCPTool records one tool call.
func (m *Metrics) ObserveMCPTool(tool, outcome string, seconds float64) {
	m.mcpCalls.WithLabelValues(tool, outcome).Inc()
	m.mcpDuration.WithLabelValues(tool).Observe(seconds)
}
