package server

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/metrics"
)

// Paths the router owns outside the generated contract.
const (
	// APIBasePath is the URL major. It matches servers.url in api/api.yml.
	APIBasePath = "/v1"

	// apiMountPattern hands every /v1 request to the generated router,
	// which matches the operation itself.
	apiMountPattern = APIBasePath + "/{path...}"

	// HealthPath and ReadyPath are unauthenticated: a probe that needs a
	// credential cannot tell you the process is alive.
	HealthPath = "/healthz"
	ReadyPath  = "/ready"

	// MCPPath is the Streamable HTTP endpoint. The trailing-slash form is
	// registered as an exact pattern too, so neither form redirects; a
	// redirected POST loses its body.
	MCPPath      = "/mcp"
	MCPSlashPath = "/mcp/{$}"

	readyProbeTimeout = 2 * time.Second
	healthKeyStatus   = "status"
	routeUnmatched    = "unmatched"
)

// RouterConfig is everything the router needs.
type RouterConfig struct {
	APIToken        string
	MaxRequestBytes int64
	Server          *Server
	MCPHandler      http.Handler
	Metrics         *metrics.Metrics

	// Ready reports whether monerod answers. It must be cheap.
	Ready func(ctx context.Context) bool
}

// Router assembles the public listener.
type Router struct {
	config       RouterConfig
	shuttingDown atomic.Bool
}

// NewRouter builds the router.
func NewRouter(config RouterConfig) *Router {
	return &Router{config: config}
}

// BeginShutdown makes readiness fail while in-flight requests finish.
func (r *Router) BeginShutdown() {
	r.shuttingDown.Store(true)
}

// Serbewr returns the serbewr router: probes open, the API and MCP behind
// the bearer token and the body limit.
func (r *Router) Serbewr() *serbewr.Router {
	protected := []middleware.Middleware{
		r.observe,
		middleware.BearerAuth(
			middleware.WithBearerAuthTokens(r.config.APIToken),
		),
		BodyLimit(r.config.MaxRequestBytes),
	}

	routes := []serbewr.RouteConfig{
		{Path: apiMountPattern, Handler: r.apiHandler().ServeHTTP},
	}
	if r.config.MCPHandler != nil {
		routes = append(routes,
			serbewr.RouteConfig{
				Path: MCPPath, Handler: r.config.MCPHandler.ServeHTTP,
			},
			serbewr.RouteConfig{
				Path: MCPSlashPath, Handler: r.config.MCPHandler.ServeHTTP,
			},
		)
	}

	return &serbewr.Router{
		GlobalMiddlewares: []middleware.Middleware{
			middleware.RequestID(),
			middleware.Logger(middleware.WithIncludeQuery(false)),
			middleware.Recovery(),
			middleware.SecurityHeaders(),
		},
		Groups: []serbewr.GroupConfig{
			{
				Routes: []serbewr.RouteConfig{
					{
						Method:  http.MethodGet,
						Path:    HealthPath,
						Handler: r.health,
					},
					{Method: http.MethodGet, Path: ReadyPath, Handler: r.ready},
				},
			},
			{
				Middlewares: protected,
				Routes:      routes,
			},
		},
	}
}

// apiHandler builds the generated router. The strict handler's default
// error hooks write text/plain and echo internal error text, so both are
// replaced to keep every reply in the aichteeteapee envelope.
func (r *Router) apiHandler() http.Handler {
	strict := api.NewStrictHandlerWithOptions(
		r.config.Server, nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  writeRequestError,
			ResponseErrorHandlerFunc: writeResponseError,
		},
	)

	mux := http.NewServeMux()
	handler := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseURL:          APIBasePath,
		BaseRouter:       mux,
		ErrorHandlerFunc: writeRequestError,
	})

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, pattern := mux.Handler(req); pattern == "" {
			w = &routingErrorWriter{ResponseWriter: w}
		}

		handler.ServeHTTP(w, req)
	})
}

// routingErrorWriter replaces only ServeMux's unmatched 404/405 replies.
// ServeMux still supplies the Allow header for method mismatches.
type routingErrorWriter struct {
	http.ResponseWriter

	discardBody bool
}

func (w *routingErrorWriter) WriteHeader(status int) {
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		w.ResponseWriter.WriteHeader(status)

		return
	}

	w.discardBody = true

	response := aichteeteapee.ErrorResponseNotFound
	if status == http.StatusMethodNotAllowed {
		response = aichteeteapee.ErrorResponseMethodNotAllowed
	}

	aichteeteapee.WriteJSON(w.ResponseWriter, status, response)
}

func (w *routingErrorWriter) Write(body []byte) (int, error) {
	if w.discardBody {
		return len(body), nil
	}

	written, err := w.ResponseWriter.Write(body)

	return written, err //nolint:wrapcheck // net/http inspects it unchanged
}

// observe records request metrics against a bounded route label.
func (r *Router) observe(next http.Handler) http.Handler {
	if r.config.Metrics == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.config.Metrics.HTTPInFlight().Inc()
		defer r.config.Metrics.HTTPInFlight().Dec()

		recorder := &statusRecorder{ResponseWriter: w}
		startedAt := time.Now()

		next.ServeHTTP(recorder, req)

		r.config.Metrics.ObserveHTTP(
			req.Method,
			routeTemplate(req),
			recorder.statusOrOK(),
			time.Since(startedAt).Seconds(),
		)
	})
}

func (r *Router) health(w http.ResponseWriter, _ *http.Request) {
	aichteeteapee.WriteJSON(
		w,
		http.StatusOK,
		map[string]string{healthKeyStatus: "ok"},
	)
}

func (r *Router) ready(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), readyProbeTimeout)
	defer cancel()

	if r.shuttingDown.Load() ||
		(r.config.Ready != nil && !r.config.Ready(ctx)) {
		aichteeteapee.WriteJSON(w, http.StatusServiceUnavailable,
			map[string]string{healthKeyStatus: "not_ready"})

		return
	}

	aichteeteapee.WriteJSON(
		w,
		http.StatusOK,
		map[string]string{healthKeyStatus: "ready"},
	)
}

// writeRequestError answers a request the generated binder refused.
func writeRequestError(w http.ResponseWriter, req *http.Request, err error) {
	ctxscope.GetLogger(req.Context()).
		Debug("request rejected at the binder", "err", err)

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		aichteeteapee.WriteJSON(
			w,
			http.StatusRequestEntityTooLarge,
			aichteeteapee.ErrorResponse{
				Code:    aichteeteapee.ErrorCodePayloadTooLarge,
				Message: "the request body is too large",
			},
		)

		return
	}

	aichteeteapee.WriteJSON(
		w,
		http.StatusBadRequest,
		aichteeteapee.ErrorResponse{
			Code:    aichteeteapee.ErrorCodeBadRequest,
			Message: "the request could not be decoded",
		},
	)
}

// writeResponseError answers a failure after the handler returned. The
// cause is logged, never echoed.
func writeResponseError(w http.ResponseWriter, req *http.Request, err error) {
	ctxscope.GetLogger(req.Context()).
		Error("writing the response failed", "err", err)

	aichteeteapee.WriteJSON(w, http.StatusInternalServerError,
		aichteeteapee.ErrorResponseInternalServerError)
}

// BodyLimit caps the request body before anything decodes it.
func BodyLimit(maxBytes int64) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Body != nil {
				req.Body = http.MaxBytesReader(w, req.Body, maxBytes)
			}

			next.ServeHTTP(w, req)
		})
	}
}

// routeTemplate is the matched pattern, a bounded metric label.
func routeTemplate(req *http.Request) string {
	if req.Pattern == MCPPath || req.Pattern == MCPSlashPath {
		return MCPPath
	}

	if req.Pattern == "" {
		return routeUnmatched
	}

	return req.Pattern
}

// statusRecorder captures the status code for metrics.
type statusRecorder struct {
	http.ResponseWriter

	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}

	written, err := r.ResponseWriter.Write(body)
	if err != nil {
		return written, err //nolint:wrapcheck // net/http inspects it unchanged
	}

	return written, nil
}

// Flush keeps the MCP transport's SSE responses streaming through the
// wrapper.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) statusOrOK() int {
	if r.status == 0 {
		return http.StatusOK
	}

	return r.status
}
