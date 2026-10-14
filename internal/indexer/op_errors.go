package indexer

import (
	"errors"
	"fmt"
)

// opValidationError marks a deterministic on-chain-data failure of an
// operation: assertion-style validation failures (the class legacy turns
// into an `error` notification or a silent skip -- CommunityOp.validate only
// catches AssertionError, follow/reblog/payment validators return None) and
// malformed envelopes. Introduced for community ops (PR#403) and reused as
// the single data-error marker for block processing (P1-1): the block
// processor logs these with a Warn (block/op identification included), skips
// the op, and continues with the rest of the block.
type opValidationError struct {
	err error
}

func (e *opValidationError) Error() string { return e.err.Error() }
func (e *opValidationError) Unwrap() error { return e.err }

// opValidationErr builds an opValidationError carrying the legacy assertion
// message (mirrors the `assert cond, 'message'` payloads).
func opValidationErr(format string, args ...interface{}) error {
	return &opValidationError{err: fmt.Errorf(format, args...)}
}

// IsInfraError classifies an error escaping an operation handler for
// block-level handling, mirroring legacy blocks.process_multi semantics
// (P1-1):
//
//   - data errors (errors carrying *opValidationError): the chain data itself
//     is bad or fails validation; retrying cannot help. The caller logs a
//     Warn with the block/op identification, skips the op, and continues
//     processing the block.
//   - infrastructure errors (everything else: GORM/DB failures, context
//     cancellation, timeouts, RPC failures): the whole block must fail so the
//     sync layer's retry loop re-processes it -- the block transaction rolls
//     back and no operation is lost. Swallowing these would commit a block
//     whose earlier ops were applied while later ones silently vanished.
//
// A nil error is not an infrastructure error.
func IsInfraError(err error) bool {
	if err == nil {
		return false
	}
	var dataErr *opValidationError
	return !errors.As(err, &dataErr)
}
