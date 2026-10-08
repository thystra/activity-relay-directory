package actorresolver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"syscall"
)

// ProbeDiagnostic is an intentionally small, non-sensitive description of a
// network observation. Never store or publish raw resolver/transport errors,
// remote response bodies, or IP addresses.
type ProbeDiagnostic struct {
	Stage      string
	Code       string
	HTTPStatus int
}

func (d ProbeDiagnostic) Valid() bool {
	switch d.Stage {
	case "":
		return d.Code == "" && d.HTTPStatus == 0
	case "dns":
		switch d.Code {
		case "nxdomain", "no_address", "temporary", "timeout", "policy", "error":
			return d.HTTPStatus == 0
		}
	case "connect":
		switch d.Code {
		case "refused", "timeout", "failed":
			return d.HTTPStatus == 0
		}
	case "tls":
		switch d.Code {
		case "certificate", "handshake":
			return d.HTTPStatus == 0
		}
	case "policy":
		return d.Code == "prohibited_target" && d.HTTPStatus == 0
	case "redirect":
		return d.Code == "rejected" && d.HTTPStatus == 0
	case "actor":
		switch d.Code {
		case "http_status":
			return d.HTTPStatus >= 100 && d.HTTPStatus <= 599
		case "content_type", "invalid_document", "invalid_response":
			return d.HTTPStatus == 0
		}
	case "inbox":
		switch d.Code {
		case "http_status":
			return d.HTTPStatus >= 100 && d.HTTPStatus <= 599
		case "invalid_response":
			return d.HTTPStatus == 0
		}
	case "network":
		return (d.Code == "timeout" || d.Code == "error") && d.HTTPStatus == 0
	}
	return false
}

// safeNetworkFailure drops untrusted network error text while preserving a
// structured, enumerable diagnosis. Only policy denials carry the
// ErrNetworkTarget sentinel: ordinary outages must remain distinguishable
// from prohibited targets for discovery candidate retention.
type safeNetworkFailure struct{ diagnostic ProbeDiagnostic }

func (f *safeNetworkFailure) Error() string { return "ActivityPub network operation failed" }
func (f *safeNetworkFailure) Unwrap() error {
	if (f.diagnostic.Stage == "dns" && f.diagnostic.Code == "policy") ||
		(f.diagnostic.Stage == "policy" && f.diagnostic.Code == "prohibited_target") {
		return ErrNetworkTarget
	}
	return nil
}

// ClassifyNetworkFailure never exposes an error's text. DNS evidence is kept
// distinct from TLS and TCP failures, and policy rejection isn't an outage.
func ClassifyNetworkFailure(err error) ProbeDiagnostic {
	if err == nil {
		return ProbeDiagnostic{}
	}
	if errors.Is(err, ErrRedirectRejected) {
		return ProbeDiagnostic{Stage: "redirect", Code: "rejected"}
	}
	var safe *safeNetworkFailure
	if errors.As(err, &safe) {
		return safe.diagnostic
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		switch {
		case dns.IsNotFound:
			return ProbeDiagnostic{Stage: "dns", Code: "nxdomain"}
		case dns.IsTimeout:
			return ProbeDiagnostic{Stage: "dns", Code: "timeout"}
		case dns.IsTemporary:
			return ProbeDiagnostic{Stage: "dns", Code: "temporary"}
		default:
			return ProbeDiagnostic{Stage: "dns", Code: "error"}
		}
	}
	if errors.Is(err, ErrDNSNoAddress) {
		return ProbeDiagnostic{Stage: "dns", Code: "no_address"}
	}
	if errors.Is(err, ErrDNSPolicy) {
		return ProbeDiagnostic{Stage: "dns", Code: "policy"}
	}
	var certVerify *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	if errors.As(err, &certVerify) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &certificate) {
		return ProbeDiagnostic{Stage: "tls", Code: "certificate"}
	}
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &recordHeader) {
		return ProbeDiagnostic{Stage: "tls", Code: "handshake"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeDiagnostic{Stage: "network", Code: "timeout"}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ProbeDiagnostic{Stage: "connect", Code: "refused"}
	}
	var op *net.OpError
	if errors.As(err, &op) {
		if op.Timeout() {
			return ProbeDiagnostic{Stage: "connect", Code: "timeout"}
		}
		if op.Op == "dial" {
			return ProbeDiagnostic{Stage: "connect", Code: "failed"}
		}
	}
	if errors.Is(err, ErrNetworkTarget) {
		return ProbeDiagnostic{Stage: "policy", Code: "prohibited_target"}
	}
	return ProbeDiagnostic{Stage: "network", Code: "error"}
}

func HTTPProbeDiagnostic(stage string, status int) ProbeDiagnostic {
	if status >= http.StatusContinue && status <= 599 {
		return ProbeDiagnostic{Stage: stage, Code: "http_status", HTTPStatus: status}
	}
	return ProbeDiagnostic{Stage: stage, Code: "invalid_response"}
}
