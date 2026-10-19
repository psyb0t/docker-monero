// Package amount converts Monero amounts between atomic units and XMR.
//
// One XMR is 10^12 atomic units (piconero). Amounts stay integers end to end
// and only become a decimal string at the presentation boundary, so no value
// ever passes through a float.
package amount

import (
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Decimals is the number of fractional digits in one XMR.
const Decimals = 12

// AtomicPerXMR is the number of atomic units in one XMR.
const AtomicPerXMR uint64 = 1_000_000_000_000

// FormatXMR renders atomic units as an XMR decimal string with all twelve
// fractional digits, for example 1500000000000 becomes "1.500000000000".
func FormatXMR(atomic uint64) string {
	whole := atomic / AtomicPerXMR
	fraction := atomic % AtomicPerXMR

	fractionText := strconv.FormatUint(fraction, 10)
	padding := strings.Repeat("0", Decimals-len(fractionText))

	return strconv.FormatUint(whole, 10) + "." + padding + fractionText
}

// FormatAtomic renders atomic units as a base-10 string. JSON numbers lose
// precision above 2^53, so amounts always travel as strings.
func FormatAtomic(atomic uint64) string {
	return strconv.FormatUint(atomic, 10)
}

// ParseAtomic parses a base-10 atomic-unit string. It rejects signs,
// whitespace, decimal points, and empty input.
func ParseAtomic(text string) (uint64, error) {
	if text == "" {
		return 0, ctxerrors.Wrap(commerr.ErrInvalidValue, "amount is empty")
	}

	for _, char := range text {
		if char < '0' || char > '9' {
			return 0, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"amount %q must contain only digits", text,
			)
		}
	}

	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, ctxerrors.Wrapf(
			commerr.ErrInvalidValue, "amount %q is out of range", text,
		)
	}

	return value, nil
}
