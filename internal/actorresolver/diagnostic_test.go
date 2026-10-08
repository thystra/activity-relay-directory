package actorresolver

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

func TestClassifyNetworkFailureWithoutErrorDisclosure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ProbeDiagnostic
	}{
		{"NXDOMAIN", &net.DNSError{IsNotFound: true, Err: "sensitive DNS detail"}, ProbeDiagnostic{Stage: "dns", Code: "nxdomain"}},
		{"DNS temporary", &net.DNSError{IsTemporary: true}, ProbeDiagnostic{Stage: "dns", Code: "temporary"}},
		{"DNS timeout", &net.DNSError{IsTimeout: true}, ProbeDiagnostic{Stage: "dns", Code: "timeout"}},
		{"no address", ErrDNSNoAddress, ProbeDiagnostic{Stage: "dns", Code: "no_address"}},
		{"blocked", ErrDNSPolicy, ProbeDiagnostic{Stage: "dns", Code: "policy"}},
		{"TLS certificate", x509.UnknownAuthorityError{}, ProbeDiagnostic{Stage: "tls", Code: "certificate"}},
		{"refused", syscall.ECONNREFUSED, ProbeDiagnostic{Stage: "connect", Code: "refused"}},
		{"redirect", errors.Join(ErrNetworkTarget, ErrRedirectRejected), ProbeDiagnostic{Stage: "redirect", Code: "rejected"}},
		{"other", errors.New("secret private transport message"), ProbeDiagnostic{Stage: "network", Code: "error"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyNetworkFailure(c.err)
			if got != c.want || !got.Valid() {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

// Both the legacy key resolver and the diagnostic probe share this fetch path.
// Detailed probes retain classified evidence, but neither caller may receive
// arbitrary transport error text or the URL included in http.Client errors.
func TestDetailedActorFailureKeepsDiagnosisButRedactsTransportError(t *testing.T) {
	for _, test := range []struct {
		name              string
		transportError    error
		wantDiagnostic    ProbeDiagnostic
		wantNetworkTarget bool
		wantDeadline      bool
	}{
		{
			name:           "unclassified transport error",
			transportError: errors.New("sensitive transport detail"),
			wantDiagnostic: ProbeDiagnostic{Stage: "network", Code: "error"},
		},
		{
			name:           "DNS name not found",
			transportError: &net.DNSError{IsNotFound: true, Err: "sensitive DNS detail"},
			wantDiagnostic: ProbeDiagnostic{Stage: "dns", Code: "nxdomain"},
		},
		{
			name:           "TLS certificate rejection",
			transportError: errors.Join(x509.UnknownAuthorityError{}, errors.New("sensitive TLS detail")),
			wantDiagnostic: ProbeDiagnostic{Stage: "tls", Code: "certificate"},
		},
		{
			name:           "request deadline",
			transportError: errors.Join(context.DeadlineExceeded, errors.New("sensitive timeout detail")),
			wantDiagnostic: ProbeDiagnostic{Stage: "network", Code: "timeout"},
			wantDeadline:   true,
		},
		{
			name:              "redirect policy rejection",
			transportError:    errors.Join(ErrNetworkTarget, ErrRedirectRejected, errors.New("sensitive redirect detail")),
			wantDiagnostic:    ProbeDiagnostic{Stage: "redirect", Code: "rejected"},
			wantNetworkTarget: true,
		},
		{
			name:              "target policy rejection",
			transportError:    errors.Join(ErrNetworkTarget, errors.New("sensitive policy detail")),
			wantDiagnostic:    ProbeDiagnostic{Stage: "policy", Code: "prohibited_target"},
			wantNetworkTarget: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := newTestResolver(t, func(*http.Request) (*http.Response, error) {
				return nil, test.transportError
			})
			result, diagnostic, err := resolver.ProbeActorDetailed(context.Background(), testActorURL)
			if result != (ActorProbeResult{}) || diagnostic != test.wantDiagnostic ||
				!errors.Is(err, ErrActorFetch) || errors.Is(err, ErrNetworkTarget) != test.wantNetworkTarget ||
				errors.Is(err, context.DeadlineExceeded) != test.wantDeadline {
				t.Fatalf("ProbeActorDetailed() = %#v, %#v, %v", result, diagnostic, err)
			}
			if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), testActorURL) {
				t.Fatalf("transport details leaked into caller-visible error: %v", err)
			}
		})
	}
}

func TestDetailedActorHTTPStatusAndContentEvidence(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			resolver := newTestResolver(t, func(*http.Request) (*http.Response, error) {
				return actorResponse(status, "", nil), nil
			})
			actor, diagnostic, err := resolver.ProbeActorDetailed(context.Background(), "https://relay.example/actor")
			if actor != (ActorProbeResult{}) || !errors.Is(err, ErrActorFetch) ||
				diagnostic != (ProbeDiagnostic{Stage: "actor", Code: "http_status", HTTPStatus: status}) {
				t.Fatalf("actor=%#v diagnostic=%#v err=%v", actor, diagnostic, err)
			}
		})
	}
}

func TestDetailedInboxOPTIONSRemainsNonMutating(t *testing.T) {
	for _, c := range []struct {
		status int
		result InboxProbeResult
	}{
		{http.StatusNoContent, InboxProbeResponsive},
		{http.StatusMethodNotAllowed, InboxProbeMethodRejected},
		{http.StatusNotImplemented, InboxProbeMethodRejected},
		{http.StatusNotFound, InboxProbeUnreachable},
		{http.StatusGone, InboxProbeUnreachable},
	} {
		resolver := newTestResolver(t, func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodOptions {
				t.Fatalf("unexpected method %s", req.Method)
			}
			return actorResponse(c.status, "", nil), nil
		})
		result, diagnostic, err := resolver.ProbeInboxDetailed(context.Background(), "https://relay.example/inbox")
		if err != nil || result != c.result || diagnostic != (ProbeDiagnostic{Stage: "inbox", Code: "http_status", HTTPStatus: c.status}) {
			t.Fatalf("status=%d: result=%q diagnostic=%#v err=%v", c.status, result, diagnostic, err)
		}
	}
}
