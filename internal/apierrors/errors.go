// Package apierrors defines the error contract for JSON-RPC handlers:
// PublicError messages are safe to return to clients; any other error is
// logged and traced but its detail is withheld from the response
// (sanitized) so internal SQL/stack details never leak.
//
// It also ports the legacy input validators (hive/server/common/helpers.py
// valid_account/valid_permlink/valid_limit) with identical semantics.
package apierrors

import (
	"fmt"
	"regexp"
)

// PublicError is an error whose message is safe for client consumption.
// Handlers return these for user-input problems and documented refusals
// (e.g. "not implemented"); the JSON-RPC layer forwards the message in
// error.data.
type PublicError string

// Error implements the error interface.
func (e PublicError) Error() string { return string(e) }

// Publicf formats a new PublicError.
func Publicf(format string, args ...interface{}) PublicError {
	return PublicError(fmt.Sprintf(format, args...))
}

// accountNameRe matches valid steem account names (legacy valid_account).
var accountNameRe = regexp.MustCompile(`^[a-z0-9\.\-]+$`)

// ValidAccount validates a steem account name (3-16 chars, lowercase
// alnum plus . and -, no leading @). Mirrors legacy valid_account; the
// returned error is a PublicError.
func ValidAccount(name string, allowEmpty bool) (string, error) {
	if name == "" {
		if !allowEmpty {
			return "", PublicError("invalid account (not specified)")
		}
		return "", nil
	}
	if len(name) < 3 || len(name) > 16 {
		return "", Publicf("invalid account name length: `%s`", name)
	}
	if name[0] == '@' {
		return "", Publicf("invalid account name char `@`")
	}
	if !accountNameRe.MatchString(name) {
		return "", Publicf("invalid account char in `%s`", name)
	}
	return name, nil
}

// ValidPermlink validates a permlink (string, ≤256 chars). Mirrors legacy
// valid_permlink.
func ValidPermlink(permlink string, allowEmpty bool) (string, error) {
	if permlink == "" {
		if !allowEmpty {
			return "", PublicError("permlink cannot be blank")
		}
		return "", nil
	}
	if len(permlink) > 256 {
		return "", PublicError("invalid permlink length")
	}
	return permlink, nil
}

// ValidLimit validates a user-provided limit: positive and within ubound.
// Mirrors legacy valid_limit.
func ValidLimit(limit int, ubound int) (int, error) {
	if limit <= 0 {
		return 0, PublicError("limit must be positive")
	}
	if limit > ubound {
		return 0, Publicf("limit exceeds max (%d > %d)", limit, ubound)
	}
	return limit, nil
}

// ClampLimit is the defensive counterpart used at query boundaries: any
// non-positive limit collapses to 0 (an empty result — never an unbounded
// table scan, which is what a negative SQL LIMIT would produce).
func ClampLimit(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
}
