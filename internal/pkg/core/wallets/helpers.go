package wallets

import (
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/amount"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
)

const (
	fullPercent = 100.0
	maxIndex    = 1 << 31
)

func validatePassword(password string) error {
	if len(password) > api.MaxPasswordLength {
		return invalid.Newf(
			"password is longer than %d characters",
			api.MaxPasswordLength,
		)
	}

	return nil
}

func accountIndexOf(value *int64) (uint32, error) {
	index, err := indexValue(value)
	if err != nil {
		return 0, ctxerrors.Wrap(ErrInvalidAccount, "accountIndex")
	}

	return index, nil
}

func indexValue(value *int64) (uint32, error) {
	if value == nil {
		return 0, nil
	}

	if *value < 0 || *value >= maxIndex {
		return 0, ErrInvalidAccount
	}

	return uint32(*value), nil
}

func nonNegative(value *int64, field string) (uint64, error) {
	if value == nil {
		return 0, nil
	}

	if *value < 0 {
		return 0, invalid.Newf("%s must not be negative", field)
	}

	return uint64(*value), nil
}

func page(limit, offset *int) (int, int, error) {
	resolvedLimit := api.DefaultListLimit
	if limit != nil {
		resolvedLimit = *limit
	}

	resolvedOffset := 0
	if offset != nil {
		resolvedOffset = *offset
	}

	if resolvedLimit < 1 || resolvedLimit > api.MaxListLimit {
		return 0, 0, invalid.Newf("limit must be 1-%d", api.MaxListLimit)
	}

	if resolvedOffset < 0 {
		return 0, 0, invalid.Newf("offset must not be negative")
	}

	return resolvedLimit, resolvedOffset, nil
}

func transferFilter(
	directions []api.TransferDirection,
	account uint32,
) (walletrpc.TransferFilter, error) {
	filter := walletrpc.TransferFilter{AccountIndex: account}

	if len(directions) == 0 {
		filter.In, filter.Out, filter.Pending = true, true, true
		filter.Failed, filter.Pool = true, true

		return filter, nil
	}

	for _, direction := range directions {
		switch direction {
		case api.TransferDirectionIn:
			filter.In = true
		case api.TransferDirectionOut:
			filter.Out = true
		case api.TransferDirectionPending:
			filter.Pending = true
		case api.TransferDirectionFailed:
			filter.Failed = true
		case api.TransferDirectionPool:
			filter.Pool = true
		default:
			return walletrpc.TransferFilter{}, invalid.Newf(
				"unknown direction %q",
				direction,
			)
		}
	}

	return filter, nil
}

func buildTransfer(
	request api.TransferRequest,
) (walletrpc.TransferRequest, error) {
	destinations, err := parseDestinations(request.Destinations)
	if err != nil {
		return walletrpc.TransferRequest{}, err
	}

	account, err := accountIndexOf(request.AccountIndex)
	if err != nil {
		return walletrpc.TransferRequest{}, err
	}

	subaddresses, err := subaddressIndices(request.SubaddressIndices)
	if err != nil {
		return walletrpc.TransferRequest{}, err
	}

	priority, err := priorityOf(request.Priority)
	if err != nil {
		return walletrpc.TransferRequest{}, err
	}

	return walletrpc.TransferRequest{
		Destinations:   destinations,
		AccountIndex:   account,
		SubaddrIndices: subaddresses,
		Priority:       priority,
		DoNotRelay:     request.DryRun != nil && *request.DryRun,
	}, nil
}

func parseDestinations(
	requested []api.Destination,
) ([]walletrpc.Destination, error) {
	count := len(requested)
	if count == 0 || count > api.MaxDestinations {
		return nil, invalid.Newf(
			"destinations must hold 1-%d entries", api.MaxDestinations,
		)
	}

	destinations := make([]walletrpc.Destination, 0, count)

	for _, destination := range requested {
		value, err := amount.ParseAtomic(destination.Amount)
		if err != nil || value == 0 {
			return nil, invalid.Newf(
				"amount %q must be a positive integer of atomic units",
				destination.Amount,
			)
		}

		if destination.Address == "" {
			return nil, ctxerrors.Wrap(
				ErrInvalidAddress, "destination address is empty",
			)
		}

		destinations = append(destinations, walletrpc.Destination{
			Amount: value, Address: destination.Address,
		})
	}

	return destinations, nil
}

// priorityOf maps the API's named priorities onto wallet2's numbers.
func priorityOf(priority *api.TransferPriority) (uint32, error) {
	if priority == nil {
		return walletrpc.PriorityDefault, nil
	}

	switch *priority {
	case api.TransferPriorityDefault:
		return walletrpc.PriorityDefault, nil
	case api.TransferPriorityLow:
		return walletrpc.PriorityUnimport, nil
	case api.TransferPriorityNormal:
		return walletrpc.PriorityNormal, nil
	case api.TransferPriorityHigh:
		return walletrpc.PriorityElevated, nil
	case api.TransferPriorityHighest:
		return walletrpc.PriorityPriority, nil
	default:
		return 0, invalid.Newf("unknown priority %q", *priority)
	}
}

func subaddressIndices(values *[]int64) ([]uint32, error) {
	if values == nil {
		return nil, nil
	}

	indices := make([]uint32, 0, len(*values))

	for i := range *values {
		index, err := indexValue(&(*values)[i])
		if err != nil {
			return nil, ctxerrors.Wrap(ErrInvalidAccount, "subaddressIndices")
		}

		indices = append(indices, index)
	}

	return indices, nil
}

// mapWalletError turns wallet-rpc's codes into the service's sentinels.
func mapWalletError(err error, action string) error {
	switch walletrpc.Code(err) {
	case walletrpc.CodeAccountIndexOutOfBound,
		walletrpc.CodeAddressIndexOutOfBound:
		return ctxerrors.Wrap(ErrInvalidAccount, action)
	case walletrpc.CodeWrongAddress:
		return ctxerrors.Wrap(ErrInvalidAddress, action)
	case walletrpc.CodeWrongKey:
		return invalid.Newf("the supplied key is not valid")
	case walletrpc.CodeNoDaemonConnection, walletrpc.CodeDaemonIsBusy:
		return ctxerrors.Wrap(
			commerr.ErrUnavailable,
			action+": node unavailable",
		)
	}

	return ctxerrors.Wrap(err, action)
}

func mapTransferError(err error) error {
	switch walletrpc.Code(err) {
	case walletrpc.CodeNotEnoughMoney, walletrpc.CodeNotEnoughUnlockedMoney:
		return ctxerrors.Wrap(ErrInsufficientFunds, "transfer")
	case walletrpc.CodeWatchOnly:
		return ctxerrors.Wrap(ErrViewOnly, "transfer")
	case walletrpc.CodeZeroDestination, walletrpc.CodeZeroAmount:
		return invalid.Newf("every destination amount must be above zero")
	case walletrpc.CodeGenericTransferError,
		walletrpc.CodeTxNotPossible,
		walletrpc.CodeTxTooLarge,
		walletrpc.CodeNotEnoughOutsToMix:
		return ctxerrors.Wrap(ErrTransferRejected, "transfer")
	}

	return mapWalletError(err, "transfer")
}

func isNotOpen(err error) bool {
	return errors.Is(err, walletpool.ErrWalletNotOpen)
}

// applySync fills the node comparison into an open wallet's status.
//
//nolint:gosec // chain heights fit int64
func applySync(
	status *api.WalletStatus,
	walletHeight uint64,
	info daemonrpc.Info,
) {
	nodeHeight := int64(info.Height)
	synchronized := info.Synchronized

	behind := int64(0)
	if info.Height > walletHeight {
		behind = int64(info.Height - walletHeight)
	}

	percent := fullPercent
	if info.Height > 0 {
		percent = min(
			fullPercent,
			float64(walletHeight)/float64(info.Height)*fullPercent,
		)
	}

	syncing := !synchronized || behind > 0

	status.NodeHeight = &nodeHeight
	status.NodeSynchronized = &synchronized
	status.BlocksBehind = &behind
	status.SyncPercent = &percent
	status.Syncing = &syncing
}

//nolint:gosec // chain heights fit int64
func toInt64Ptr(value uint64) *int64 {
	converted := int64(value)

	return &converted
}

func deref(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
