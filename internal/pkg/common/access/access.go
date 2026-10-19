// Package access holds the deployment-wide write switch's sentinel, shared
// by every service that refuses writes in read-only mode.
package access

import (
	"fmt"

	"github.com/psyb0t/ctxerrors/commerr"
)

// ErrReadOnly is returned for any write while DOCKER_MONERO_READ_ONLY is on.
var ErrReadOnly = fmt.Errorf(
	"the service runs in read-only mode: %w", commerr.ErrPermissionDenied,
)
