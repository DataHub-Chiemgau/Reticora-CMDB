package readonly

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// ClassPathNotAllowed: a GET on a path outside the allowlist (E-248),
	// refused before the network.
	ClassPathNotAllowed = "path_not_allowed"
	// ClassCanceled: the caller ended the read (its context was canceled
	// or passed its own deadline); no outcome of ADMP, the circuit breaker
	// does not count it.
	ClassCanceled = "canceled"
)

// classOK is the class label of a successful request in the metrics.
const classOK = "ok"

// Classes are the error classes in a fixed order.
var Classes = []string{ClassNetwork, ClassTimeout, ClassAuth, ClassRateLimited, ClassServer, ClassClient,
	ClassDecode, ClassCircuitOpen, ClassMethodNotAllowed, ClassCredential, ClassCanceled, ClassPathNotAllowed}

// Sentinels of the read-only guard and of the client. Their texts name no
// URL, token, secret or answer.
var (
	// ErrMethodNotAllowed: a request with another method than GET (on the
	// base URL) or the login method POST (on the login URL), or a GET with
	// a body; refused before the network.
	ErrMethodNotAllowed = errors.New("admp: method not allowed by the read-only transport")
	// ErrDestinationNotAllowed: a request outside the base URL (another
	// scheme, host or port, a path outside its prefix, also a redirect
	// hop) or another URL than the login URL; refused before the network.
	ErrDestinationNotAllowed = errors.New("admp: destination not allowed by the read-only transport")
	// ErrInvalidPath: a path of a read that is absolute, holds a dot
	// segment or leaves the prefix of the base URL.
	ErrInvalidPath = errors.New("admp: invalid path")
	// ErrCircuitOpen: the account's circuit breaker is open.
	ErrCircuitOpen = errors.New("admp: circuit open")
	// ErrInvalidConfig: New was given an unusable configuration.
	ErrInvalidConfig = errors.New("admp: invalid client configuration")
	// ErrPathNotAllowed: a GET on a path or with a query outside the
	// allowlist of the documented reads (E-248); refused before the
	// network.
	ErrPathNotAllowed = errors.New("admp: path not allowed by the read-only transport")
)

// Error is the failure of a request: its class and, when ADMP answered,
// the HTTP status. It never carries the answer, a token or a secret.
type Error struct {
	Class  string
	Status int
	// sentinel is one of the sentinels above (errors.Is), or nil.
	sentinel error
	// named: the answer was a 404 whose body is a NamedError of ADMP
	// ({name, message?, child?} with a non-empty name). Only such a 404 is
	// ADMP saying the object is gone; a 404 of a proxy, a gateway or a
	// wrong base path is not (WP-1075). The body itself is not kept.
	named bool
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
	case errors.Is(err, ErrPathNotAllowed):
		return ClassPathNotAllowed
	case errors.Is(err, ErrMethodNotAllowed), errors.Is(err, ErrDestinationNotAllowed), errors.Is(err, ErrInvalidPath):
		return ClassMethodNotAllowed
	case errors.Is(err, ErrCircuitOpen):
		return ClassCircuitOpen
	default:
		return ClassNetwork
	}
}

// namedError reports whether body is a NamedError of ADMP: a JSON object
// with a non-empty string member name.
func namedError(body []byte) bool {
	var v struct {
		Name *string `json:"name"`
	}
	return json.Unmarshal(body, &v) == nil && v.Name != nil && strings.TrimSpace(*v.Name) != ""
}

func classErr(class string, status int, sentinel error) *Error {
	return &Error{Class: class, Status: status, sentinel: sentinel}
}
