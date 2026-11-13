// Package exitcode freezes Scry's process exit codes and machine reason codes.
//
// The table is part of the K0 contract (scry.status/v0). Changing a value is a
// breaking change; adding a reason is additive and must also be added to
// schema/scry.status.v0.json (a test keeps the two in step).
package exitcode

import (
	"errors"
	"fmt"
)

// Code is a process exit code.
type Code int

// Exit codes. 0-2 describe an observed cluster; 3-7 mean the cluster could not
// be observed (the status document is still emitted); 8 is usage, with no
// document.
const (
	Green     Code = 0 // observed, GREEN (includes declared-dark)
	Yellow    Code = 1 // observed, YELLOW
	Red       Code = 2 // observed, RED; also a wait timeout
	Config    Code = 3 // config, refused auth kind, missing context
	Auth      Code = 4 // 401 / 403
	TLS       Code = 5 // verify, expiry, name
	Transport Code = 6 // connected, then reset or silent
	Internal  Code = 7 // internal bug or allowlist refusal
	Usage     Code = 8 // bad flags or arguments; no document
)

// Reason is a machine-readable reason code (snake_case).
type Reason string

// Reason codes. The order is the order of the enum in the status schema.
const (
	ExpectedDark       Reason = "expected_dark"
	ApiserverDown      Reason = "apiserver_down"
	HTTPUnresponsive   Reason = "http_unresponsive"
	ReadyzFailed       Reason = "readyz_failed"
	NoNodes            Reason = "no_nodes"
	NodeNotReady       Reason = "node_not_ready"
	NetworkUnavailable Reason = "network_unavailable"
	Timeout            Reason = "timeout"
	WaitExpectedDark   Reason = "wait_expected_dark"
	AuthRejected       Reason = "auth_rejected"
	RBACDenied         Reason = "rbac_denied"
	TLSVerifyFailed    Reason = "tls_verify_failed"
	TLSCertExpired     Reason = "tls_cert_expired"
	KubeconfigMissing  Reason = "kubeconfig_missing"
	KubeconfigInvalid  Reason = "kubeconfig_invalid"
	ExecAuthRefused    Reason = "exec_auth_refused"
	AuthProviderRefuse Reason = "auth_provider_refused"
	InsecureTLSRefused Reason = "insecure_tls_refused"
	ConfigInvalid      Reason = "config_invalid"
	AllowlistDenied    Reason = "allowlist_denied"
	InternalError      Reason = "internal"
)

var codes = map[Reason]Code{
	WaitExpectedDark:   Config,
	KubeconfigMissing:  Config,
	KubeconfigInvalid:  Config,
	ExecAuthRefused:    Config,
	AuthProviderRefuse: Config,
	InsecureTLSRefused: Config,
	ConfigInvalid:      Config,
	AuthRejected:       Auth,
	RBACDenied:         Auth,
	TLSVerifyFailed:    TLS,
	TLSCertExpired:     TLS,
	HTTPUnresponsive:   Transport,
	AllowlistDenied:    Internal,
	InternalError:      Internal,
}

// all lists every reason in schema order.
var all = []Reason{
	ExpectedDark, ApiserverDown, HTTPUnresponsive, ReadyzFailed, NoNodes,
	NodeNotReady, NetworkUnavailable, Timeout, WaitExpectedDark, AuthRejected,
	RBACDenied, TLSVerifyFailed, TLSCertExpired, KubeconfigMissing,
	KubeconfigInvalid, ExecAuthRefused, AuthProviderRefuse, InsecureTLSRefused,
	ConfigInvalid, AllowlistDenied, InternalError,
}

// All returns every reason code, in schema order.
func All() []Reason { return append([]Reason(nil), all...) }

// ForReason returns the exit code a reason carries when it is the cause of an
// unobserved document. ok is false for reasons that only decorate an observed
// document (expected_dark, apiserver_down, readyz_failed, no_nodes,
// node_not_ready, network_unavailable, timeout).
func ForReason(r Reason) (c Code, ok bool) {
	c, ok = codes[r]
	return c, ok
}

// Error is a failure that carries its exit code and reason. Message must be
// safe to print: never a kubeconfig path, server URL, token or file contents.
type Error struct {
	Code      Code
	Reason    Reason
	Message   string
	Available []string // e.g. context names, for a missing --context
	Err       error    // underlying cause; not printed
}

// New builds an Error for reason r. The code comes from the frozen table; a
// reason that carries no code is an internal error.
func New(r Reason, format string, args ...any) *Error {
	c, ok := ForReason(r)
	if !ok {
		c = Internal
	}
	return &Error{Code: c, Reason: r, Message: fmt.Sprintf(format, args...)}
}

// Wrap is New with an underlying cause.
func Wrap(r Reason, cause error, format string, args ...any) *Error {
	e := New(r, format, args...)
	e.Err = cause
	return e
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrExecAuth) match any Error with the same reason.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Reason == e.Reason
}

// Sentinels for errors.Is.
var (
	ErrExecAuth     = &Error{Code: Config, Reason: ExecAuthRefused}
	ErrAuthProvider = &Error{Code: Config, Reason: AuthProviderRefuse}
	ErrInsecureTLS  = &Error{Code: Config, Reason: InsecureTLSRefused}
	ErrAllowlist    = &Error{Code: Internal, Reason: AllowlistDenied}
)

// Of extracts the typed error from err, if any.
func Of(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// CodeOf returns the exit code for err: its own code if typed, else Internal.
func CodeOf(err error) Code {
	if err == nil {
		return Green
	}
	if e, ok := Of(err); ok {
		return e.Code
	}
	return Internal
}
