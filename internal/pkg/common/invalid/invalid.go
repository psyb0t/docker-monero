// Package invalid carries a validation failure whose message is safe to show
// a client.
//
// Errors wrapped with ctxerrors render their call sites, which must never
// reach a response. A transport can pull the original text back out of an
// invalid.Error however deeply it was wrapped, and fall back to a generic
// message for everything else.
package invalid

import (
	"errors"
	"fmt"

	"github.com/psyb0t/ctxerrors/commerr"
)

// Error is a caller mistake described in client-safe words.
type Error struct {
	message string
}

// Newf builds an Error from a format string.
func Newf(format string, args ...any) error {
	return &Error{message: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string {
	return e.message
}

// Unwrap places every Error in the commerr.ErrInvalidArgument class.
func (e *Error) Unwrap() error {
	return commerr.ErrInvalidArgument
}

// Message returns the client-safe text of the first Error in err's chain.
func Message(err error) (string, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}

	return target.message, true
}
