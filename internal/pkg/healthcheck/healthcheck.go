// Package healthcheck probes the running service's readiness endpoint. The
// container image has no curl or wget, so its HEALTHCHECK runs the binary
// itself with the healthcheck command.
package healthcheck

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Probe defaults.
const (
	DefaultTimeout = 3 * time.Second
	readyPath      = "/ready"
	wildcardIPv4   = "0.0.0.0"
	wildcardIPv6   = "::"
	loopback       = "127.0.0.1"
)

// Check calls /ready on the API listener at listenAddress and fails unless it
// answers 200. A wildcard bind address is probed on loopback.
func Check(ctx context.Context, listenAddress string) error {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"listen address %q",
			listenAddress,
		)
	}

	if host == "" || host == wildcardIPv4 || host == wildcardIPv6 {
		host = loopback
	}

	url := "http://" + net.JoinHostPort(host, port) + readyPath

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ctxerrors.Wrap(err, "build readiness request")
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ctxerrors.Wrap(err, "call readiness endpoint")
	}
	defer response.Body.Close() //nolint:errcheck // body unused

	if response.StatusCode != http.StatusOK {
		return ctxerrors.Wrapf(
			commerr.ErrNotReady, "readiness returned %d", response.StatusCode,
		)
	}

	return nil
}
