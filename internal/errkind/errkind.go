// Package errkind names the categories usage telemetry files a failed ox
// command under, and attaches one to an error without changing its message.
//
// An error ox creates declares its category where it is created:
//
//	return errkind.Errorf(errkind.NotLoggedIn, "not authenticated — run 'ox login' first")
//
// postHogErrorKind in cmd/ox derives the category of any other error from
// the standard library's errors and the exit code.
package errkind

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is the category of a failure. Telemetry sends it in place of the
// error's message, which can carry paths and other user input.
type Kind string

const (
	Interrupted        Kind = "interrupted"         // the command was interrupted or canceled
	Auth               Kind = "auth"                // credentials were rejected
	NotLoggedIn        Kind = "not_logged_in"       // no usable credentials: run ox login
	NotInitialized     Kind = "not_initialized"     // the repository is not set up: run ox init
	VersionUnsupported Kind = "version_unsupported" // the server no longer accepts this ox version
	Daemon             Kind = "daemon"              // the ox daemon is down or not answering
	Timeout            Kind = "timeout"             // a deadline passed
	Network            Kind = "network"             // the network failed
	Usage              Kind = "usage"               // the command was invoked wrong
	ChecksFailed       Kind = "checks_failed"       // ox doctor found a failed check
	Other              Kind = "other"               // none of the above
)

type kindError struct {
	kind   Kind
	detail string
	err    error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

// Errorf is fmt.Errorf with kind attached; the message is unchanged. Its
// detail is the format's first line: ox's own wording, before any value is
// filled in.
func Errorf(kind Kind, format string, a ...any) error {
	detail, _, _ := strings.Cut(format, "\n")
	return &kindError{kind: kind, detail: detail, err: fmt.Errorf(format, a...)}
}

// WithDetail attaches kind and detail to err. Telemetry sends detail, so it
// must be built only from names ox defines, never from input or the
// environment.
func WithDetail(kind Kind, detail string, err error) error {
	return &kindError{kind: kind, detail: detail, err: err}
}

// HTTPStatus files a failed HTTP response: a server rejecting the
// credentials (401, 403) is Auth, any other status Other.
func HTTPStatus(code int) Kind {
	if code == 401 || code == 403 {
		return Auth
	}
	return Other
}

// Of returns the kind errors.As finds in err's chain, or "" when no error in
// the chain has one.
func Of(err error) Kind {
	if k := find(err); k != nil {
		return k.kind
	}
	return ""
}

// DetailOf returns the detail of the error Of reads, or "".
func DetailOf(err error) string {
	if k := find(err); k != nil {
		return k.detail
	}
	return ""
}

func find(err error) *kindError {
	var k *kindError
	if errors.As(err, &k) {
		return k
	}
	return nil
}
