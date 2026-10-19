// Package commonerrors re-exports the shared sentinel errors that now live in
// github.com/psyb0t/ctxerrors/commerr. Prefer importing commerr directly; these
// aliases keep existing imports working and reference the SAME error values, so
// errors.Is holds across both import paths.
package commonerrors

import "github.com/psyb0t/ctxerrors/commerr"

var (
	// Configuration & Environment errors
	ErrEnvVarNotSet              = commerr.ErrEnvVarNotSet
	ErrRequiredConfigValueNotSet = commerr.ErrRequiredConfigValueNotSet
	ErrEmptyMigrationsPath       = commerr.ErrEmptyMigrationsPath

	// File & Path errors
	ErrFileInvalid           = commerr.ErrFileInvalid
	ErrFileNotFound          = commerr.ErrFileNotFound
	ErrPathIsRequired        = commerr.ErrPathIsRequired
	ErrCouldNotDownloadFiles = commerr.ErrCouldNotDownloadFiles

	// Validation & Input errors
	ErrInvalidArgument  = commerr.ErrInvalidArgument
	ErrInvalidValue     = commerr.ErrInvalidValue
	ErrTargetNotPointer = commerr.ErrTargetNotPointer
	ErrCouldNotDecode   = commerr.ErrCouldNotDecode

	// Field & Data errors
	ErrNilOutput                      = commerr.ErrNilOutput
	ErrNilField                       = commerr.ErrNilField
	ErrRequiredFieldNotSet            = commerr.ErrRequiredFieldNotSet
	ErrRequiredLLMResponseFieldNotSet = commerr.ErrRequiredLLMResponseFieldNotSet
	ErrAlreadyExists                  = commerr.ErrAlreadyExists

	// Job & Process errors
	ErrJobFailed                 = commerr.ErrJobFailed
	ErrUnexpectedNumberOfResults = commerr.ErrUnexpectedNumberOfResults
	ErrNotFound                  = commerr.ErrNotFound

	// Operation errors
	ErrFetchFailed     = commerr.ErrFetchFailed
	ErrParseFailed     = commerr.ErrParseFailed
	ErrWriteFailed     = commerr.ErrWriteFailed
	ErrPublishFailed   = commerr.ErrPublishFailed
	ErrSubscribeFailed = commerr.ErrSubscribeFailed
	ErrDownloadFailed  = commerr.ErrDownloadFailed
	ErrUploadFailed    = commerr.ErrUploadFailed
	ErrUpsertFailed    = commerr.ErrUpsertFailed
	ErrDeleteFailed    = commerr.ErrDeleteFailed
	ErrConnectFailed   = commerr.ErrConnectFailed
	ErrBrowseFailed    = commerr.ErrBrowseFailed
	ErrSeedFailed      = commerr.ErrSeedFailed
	ErrMigrationFailed = commerr.ErrMigrationFailed
	ErrUnmarshalFailed = commerr.ErrUnmarshalFailed
	ErrMarshalFailed   = commerr.ErrMarshalFailed

	// Process State errors
	ErrUnavailable  = commerr.ErrUnavailable
	ErrFailed       = commerr.ErrFailed
	ErrTimeout      = commerr.ErrTimeout
	ErrTerminated   = commerr.ErrTerminated
	ErrKilled       = commerr.ErrKilled
	ErrClosing      = commerr.ErrClosing
	ErrShuttingDown = commerr.ErrShuttingDown
	ErrCancelled    = commerr.ErrCancelled

	// Access & rate-limit errors
	ErrNotAuthenticated = commerr.ErrNotAuthenticated
	ErrRateLimited      = commerr.ErrRateLimited
)
