package readonly

import (
	"errors"
	"fmt"
)

// Error classes of a request (ADM-03): the label of the metrics, the
// last_error_class of an account or a cached object (WP-929).
const (
	ClassNetwork          = "network"
	ClassTimeout          = "timeout"
	ClassAuth             = "auth"
	ClassRateLimited      = "rate_limited"
	ClassServer           = "server"
	ClassClient           = "client"
	ClassDecode           = "decode"
	ClassCircuitOpen      = "circuit_open"
	ClassMethodNotAllowed = "method_not_allowed"
	ClassCredential       = "credential"
	// ClassCanceled: the caller ended the read (its context was canceled
	// or passed its own deadline); no outcome of ADMP, the circuit breaker
	// does not count it.
	ClassCanceled = "canceled"
)

// classOK is the class label of a successful request in the metrics.
const classOK = "ok"

// Classes are the error classes in a fixed order.
var Classes = []string{ClassNetwork, ClassTimeout, ClassAuth, ClassRateLimited, ClassServer, ClassClient,
	ClassDecode, ClassCircuitOpen, ClassMethodNotAllowed, ClassCredential, ClassCanceled}

// Sentinels of the read-only guard and of the client. Their texts name no
// URL, token, secret or answer.
var (
	// ErrMethodNotAllowed: a request with another method than GET (on the
	// base URL) or the configured login method (on the auth URL), or a
	// GET with a body; refused before the network.
	ErrMethodNotAllowed = errors.New("admp: method not allowed by the read-only transport")
	// ErrDestinationNotAllowed: a request outside the base URL (another
	// scheme, host or port, a path outside its prefix, also a redirect
	// hop) or another URL than the auth URL; refused before the network.
	ErrDestinationNotAllowed = errors.New("admp: destination not allowed by the read-only transport")
	// ErrInvalidPath: a path of a read that is absolute, holds a dot
	// segment or leaves the prefix of the base URL.
	ErrInvalidPath = errors.New("admp: invalid path")
	// ErrCircuitOpen: the account's circuit breaker is open.
	ErrCircuitOpen = errors.New("admp: circuit open")
	// ErrInvalidConfig: New was given an unusable configuration.
	ErrInvalidConfig = errors.New("admp: invalid client configuration")
	// ErrNoAuthenticator: a login without an authenticator (the concrete
	// login procedure follows the documentation, A-55).
	ErrNoAuthenticator = errors.New("admp: no authenticator")
)

// Error is the failure of a request: its class and, when ADMP answered,
// the HTTP status. It never carries the answer, a token or a secret.
type Error struct {
	Class  string
	Status int
	// sentinel is one of the sentinels above (errors.Is), or nil.
	sentinel error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("admp: request failed (class %s, HTTP %d)", e.Class, e.Status)
	}
	return fmt.Sprintf("admp: request failed (class %s)", e.Class)
}

// Unwrap returns the sentinel of the failure.
func (e *Error) Unwrap() error { return e.sentinel }

// ClassOf returns the class of err: the class of an *Error, of a sentinel,
// timeout for an ended context, network otherwise; empty for nil.
func ClassOf(err error) string {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Class
	case errors.Is(err, ErrMethodNotAllowed), errors.Is(err, ErrDestinationNotAllowed), errors.Is(err, ErrInvalidPath):
		return ClassMethodNotAllowed
	case errors.Is(err, ErrCircuitOpen):
		return ClassCircuitOpen
	default:
		return ClassNetwork
	}
}

func classErr(class string, status int, sentinel error) *Error {
	return &Error{Class: class, Status: status, sentinel: sentinel}
}
