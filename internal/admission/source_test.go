package admission

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestSourceResolverUsesDirectPeerAndIgnoresSpoofedHeaders(t *testing.T) {
	resolver, err := NewSourceResolver(nil)
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}
	request := &http.Request{
		RemoteAddr: "192.168.50.24:42000",
		Header: http.Header{
			"Forwarded":       {"for=203.0.113.10"},
			"X-Forwarded-For": {"203.0.113.10"},
			"X-Real-Ip":       {"203.0.113.10"},
		},
	}

	got, err := resolver.Source(request)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	want := netip.MustParseAddr("192.168.50.24")
	if got != want {
		t.Fatalf("Source() = %v, want %v", got, want)
	}
}

func TestSourceResolverUsesOverwrittenIdentityFromTrustedPeer(t *testing.T) {
	resolver, err := NewSourceResolver([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("2001:db8:ffff::5/128"),
	})
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		realIP     string
		want       netip.Addr
	}{
		{
			name:       "IPv4 proxy and LAN client",
			remoteAddr: "127.0.0.1:8081",
			realIP:     "10.20.30.40",
			want:       netip.MustParseAddr("10.20.30.40"),
		},
		{
			name:       "IPv6 proxy and public client",
			remoteAddr: "[2001:db8:ffff::5]:8081",
			realIP:     "2001:db8:1234::8",
			want:       netip.MustParseAddr("2001:db8:1234::8"),
		},
		{
			name:       "mapped client is canonicalized",
			remoteAddr: "127.0.0.1:8081",
			realIP:     "::ffff:192.168.5.6",
			want:       netip.MustParseAddr("192.168.5.6"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &http.Request{
				RemoteAddr: test.remoteAddr,
				Header:     http.Header{"X-Real-Ip": {test.realIP}},
			}
			got, err := resolver.Source(request)
			if err != nil {
				t.Fatalf("Source() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Source() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSourceResolverFailsClosedForInvalidTrustedPeerHeader(t *testing.T) {
	resolver, err := NewSourceResolver([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
	})
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}

	tests := []struct {
		name   string
		header http.Header
	}{
		{name: "missing", header: http.Header{}},
		{name: "empty", header: http.Header{"X-Real-Ip": {""}}},
		{name: "malformed", header: http.Header{"X-Real-Ip": {"not-an-address"}}},
		{name: "address with port", header: http.Header{"X-Real-Ip": {"192.0.2.5:4000"}}},
		{name: "comma chain", header: http.Header{"X-Real-Ip": {"192.0.2.5, 192.0.2.6"}}},
		{name: "repeated", header: http.Header{"X-Real-Ip": {"192.0.2.5", "192.0.2.6"}}},
		{name: "unspecified", header: http.Header{"X-Real-Ip": {"0.0.0.0"}}},
		{name: "multicast", header: http.Header{"X-Real-Ip": {"ff02::1"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &http.Request{
				RemoteAddr: "127.0.0.1:8081",
				Header:     test.header,
			}
			_, err := resolver.Source(request)
			if !errors.Is(err, ErrSourceIdentity) {
				t.Fatalf("Source() error = %v, want ErrSourceIdentity", err)
			}
		})
	}
}

func TestSourceResolverRejectsInvalidTransportAndConfiguration(t *testing.T) {
	resolver, err := NewSourceResolver(nil)
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}
	for _, remoteAddr := range []string{"", "192.0.2.1", "name.example:80", "0.0.0.0:80", "[ff02::1]:80"} {
		_, err := resolver.Source(&http.Request{RemoteAddr: remoteAddr, Header: http.Header{}})
		if !errors.Is(err, ErrSourceIdentity) {
			t.Errorf("Source(%q) error = %v, want ErrSourceIdentity", remoteAddr, err)
		}
		if err != nil && remoteAddr != "" && strings.Contains(err.Error(), remoteAddr) {
			t.Errorf("Source(%q) error disclosed input: %v", remoteAddr, err)
		}
	}
	if _, err := resolver.Source(nil); !errors.Is(err, ErrSourceIdentity) {
		t.Fatalf("Source(nil) error = %v, want ErrSourceIdentity", err)
	}
	var nilResolver *SourceResolver
	if _, err := nilResolver.Source(&http.Request{}); !errors.Is(err, ErrSourceIdentity) {
		t.Fatalf("nil resolver error = %v, want ErrSourceIdentity", err)
	}

	invalidPrefixes := [][]netip.Prefix{
		{{}},
		{netip.MustParsePrefix("0.0.0.0/32")},
		{netip.MustParsePrefix("ff00::/8")},
	}
	for _, prefixes := range invalidPrefixes {
		_, err := NewSourceResolver(prefixes)
		if !errors.Is(err, ErrSourceConfiguration) {
			t.Errorf("NewSourceResolver(%v) error = %v, want ErrSourceConfiguration", prefixes, err)
		}
	}
}

func TestSourceResolverMasksAndDeduplicatesTrustedPrefixes(t *testing.T) {
	resolver, err := NewSourceResolver([]netip.Prefix{
		netip.PrefixFrom(netip.MustParseAddr("192.168.10.9"), 24),
		netip.MustParsePrefix("192.168.10.0/24"),
		netip.MustParsePrefix("::ffff:192.168.10.9/120"),
	})
	if err != nil {
		t.Fatalf("NewSourceResolver() error = %v", err)
	}
	if len(resolver.trustedProxies) != 1 || resolver.trustedProxies[0].String() != "192.168.10.0/24" {
		t.Fatalf("trusted proxies = %v, want one masked prefix", resolver.trustedProxies)
	}
	if _, err := NewSourceResolver([]netip.Prefix{
		netip.MustParsePrefix("::ffff:0:0/80"),
	}); !errors.Is(err, ErrSourceConfiguration) {
		t.Fatalf("broad mapped prefix error = %v, want ErrSourceConfiguration", err)
	}
}
