package kube

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// outcome is the verdict for a request that produced no HTTP response.
//
// The mapping is the D1/D2 wire contract, and it is what keeps Scry from ever
// disagreeing with Simplex: result is down only when the TCP connect itself
// failed, up when the peer answered anything at all (including a certificate
// failure), and unknown when the connect completed but nothing usable came
// back. A connect failure is data, not a tool failure: expected-dark decides
// whether it is GREEN or RED, so failure stays nil.
type outcome struct {
	result  report.Result
	failure *exitcode.Error
}

func classifyTransportError(err error, trace *dialTrace) outcome {
	// A refusal from our own allowlist is a bug, and it arrives typed.
	if e, ok := exitcode.Of(err); ok {
		return outcome{result: report.Unknown, failure: e}
	}
	if out, ok := classifyTLS(err); ok {
		return out
	}
	if connectFailed(err) {
		return outcome{result: report.Down}
	}
	if trace != nil && trace.started.Load() && !trace.completed.Load() {
		// The connect never completed and the chain carries no dial error
		// (a deadline can arrive without one): still a connect failure.
		return outcome{result: report.Down}
	}
	return outcome{
		result:  report.Unknown,
		failure: exitcode.Wrap(exitcode.HTTPUnresponsive, err, "connected, then no HTTP response before the deadline"),
	}
}

// connectFailed reports whether the TCP connect failed. Both signals are
// structured values from the standard library, never string matches.
func connectFailed(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// classifyTLS reports a certificate or handshake failure. The peer answered, so
// the apiserver is up; the exit code is 5.
func classifyTLS(err error) (outcome, bool) {
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		if certificateExpired(verifyErr.Err) {
			return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSCertExpired, err,
				"the apiserver certificate has expired")}, true
		}
		return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSVerifyFailed, err,
			"the apiserver certificate did not verify against the kubeconfig CA")}, true
	}
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &invalidErr) {
		if invalidErr.Reason == x509.Expired {
			return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSCertExpired, err,
				"the apiserver certificate has expired")}, true
		}
		return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSVerifyFailed, err,
			"the apiserver certificate did not verify against the kubeconfig CA")}, true
	}
	var authorityErr x509.UnknownAuthorityError
	if errors.As(err, &authorityErr) {
		return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSVerifyFailed, err,
			"the apiserver certificate did not verify against the kubeconfig CA")}, true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSVerifyFailed, err,
			"the apiserver certificate is not valid for the configured host")}, true
	}
	// A plaintext answer on a TLS port (or any other record-level handshake
	// failure): the peer is listening, the transport is wrong.
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return outcome{result: report.Up, failure: exitcode.Wrap(exitcode.TLSVerifyFailed, err,
			"the apiserver did not complete a TLS handshake")}, true
	}
	return outcome{}, false
}

func certificateExpired(err error) bool {
	var invalidErr x509.CertificateInvalidError
	if !errors.As(err, &invalidErr) {
		return false
	}
	return invalidErr.Reason == x509.Expired
}
