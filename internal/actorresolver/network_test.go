package actorresolver

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPublicNetworkAddressPolicy(t *testing.T) {
	for _, test := range []struct {
		address string
		public  bool
	}{
		{address: "8.8.8.8", public: true},
		{address: "1.1.1.1", public: true},
		{address: "2606:4700:4700::1111", public: true},
		{address: "0.0.0.0"},
		{address: "10.0.0.1"},
		{address: "100.64.0.1"},
		{address: "127.0.0.1"},
		{address: "169.254.169.254"},
		{address: "172.16.0.1"},
		{address: "192.0.2.1"},
		{address: "192.31.196.1"},
		{address: "192.52.193.1"},
		{address: "192.168.1.1"},
		{address: "192.175.48.1"},
		{address: "198.18.0.1"},
		{address: "198.51.100.1"},
		{address: "203.0.113.1"},
		{address: "224.0.0.1"},
		{address: "255.255.255.255"},
		{address: "::"},
		{address: "::1"},
		{address: "::ffff:127.0.0.1"},
		{address: "64:ff9b::c000:201"},
		{address: "100::1"},
		{address: "100:0:0:1::1"},
		{address: "2001::1"},
		{address: "2001:2::1"},
		{address: "2001:db8::1"},
		{address: "2002:c000:0201::1"},
		{address: "2620:4f:8000::1"},
		{address: "3fff::1"},
		{address: "400::1"},
		{address: "5f00::1"},
		{address: "fc00::1"},
		{address: "fe80::1"},
		{address: "fec0::1"},
		{address: "ff02::1"},
	} {
		address := netip.MustParseAddr(test.address)
		if got := isPublicNetworkAddress(address); got != test.public {
			t.Fatalf("isPublicNetworkAddress(%s) = %t", address, got)
		}
	}
	if isPublicNetworkAddress(netip.Addr{}) {
		t.Fatal("invalid address was accepted")
	}
}

func TestSafeDialerPinsApprovedDNSAddress(t *testing.T) {
	var dialed string
	peerConnections := make([]net.Conn, 0, 1)
	t.Cleanup(func() {
		for _, connection := range peerConnections {
			_ = connection.Close()
		}
	})
	safe := safeDialer{
		lookup: func(_ context.Context, network, host string) ([]netip.Addr, error) {
			if network != "ip" || host != "relay.example" {
				t.Fatalf("lookup = (%q, %q)", network, host)
			}
			return []netip.Addr{
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("8.8.8.8"),
			}, nil
		},
		dial: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Fatalf("dial network = %q", network)
			}
			dialed = address
			client, peer := net.Pipe()
			peerConnections = append(peerConnections, peer)
			return client, nil
		},
	}
	connection, err := safe.DialContext(context.Background(), "tcp", "relay.example:8443")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer connection.Close()
	if dialed != "8.8.8.8:8443" {
		t.Fatalf("dialed address = %q", dialed)
	}
}

func TestSafeDialerRejectsMixedOrInvalidAnswersBeforeDial(t *testing.T) {
	for _, test := range []struct {
		name      string
		host      string
		addresses []netip.Addr
		lookupErr error
	}{
		{
			name: "mixed public and private",
			host: "relay.example",
			addresses: []netip.Addr{
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("127.0.0.1"),
			},
		},
		{name: "empty answer", host: "relay.example"},
		{name: "lookup error", host: "relay.example", lookupErr: errors.New("sensitive DNS detail")},
		{name: "direct private address", host: "127.0.0.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialed := false
			safe := safeDialer{
				lookup: func(context.Context, string, string) ([]netip.Addr, error) {
					return test.addresses, test.lookupErr
				},
				dial: func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, errors.New("unexpected dial")
				},
			}
			connection, err := safe.DialContext(
				context.Background(),
				"tcp",
				net.JoinHostPort(test.host, "443"),
			)
			if connection != nil || !errors.Is(err, ErrNetworkTarget) || dialed {
				t.Fatalf("DialContext() = %#v, %v; dialed=%t", connection, err, dialed)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error disclosed DNS detail: %v", err)
			}
		})
	}
}

func TestSafeDialerBoundsDNSAnswers(t *testing.T) {
	addresses := make([]netip.Addr, maximumDNSAddresses+1)
	for index := range addresses {
		addresses[index] = netip.AddrFrom4([4]byte{8, 8, 8, byte(index + 1)})
	}
	safe := safeDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return addresses, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("oversized DNS answer reached dial")
			return nil, nil
		},
	}
	if connection, err := safe.DialContext(
		context.Background(),
		"tcp",
		"relay.example:443",
	); connection != nil || !errors.Is(err, ErrNetworkTarget) {
		t.Fatalf("DialContext() = %#v, %v", connection, err)
	}
}

func TestSafeDialerPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	safe := safeDialer{
		lookup: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			return nil, ctx.Err()
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("canceled lookup reached dial")
			return nil, nil
		},
	}
	connection, err := safe.DialContext(ctx, "tcp", "relay.example:443")
	if connection != nil || !errors.Is(err, ErrNetworkTarget) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext() = %#v, %v", connection, err)
	}
}

func TestActorRedirectPolicy(t *testing.T) {
	for _, test := range []struct {
		target string
		via    int
		valid  bool
	}{
		{target: "https://other.example/actor", via: 1, valid: true},
		{target: "https://other.example/actor", via: maximumRedirects, valid: true},
		{target: "https://other.example/actor", via: maximumRedirects + 1},
		{target: "http://other.example/actor", via: 1},
		{target: "https://other.example/actor?view=json", via: 1},
		{target: "https://other.example/actor#fragment", via: 1},
		{target: "https://127.0.0.1/actor", via: 1},
		{target: "https://[::1]/actor", via: 1},
	} {
		target, err := url.Parse(test.target)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", test.target, err)
		}
		via := make([]*http.Request, test.via)
		err = checkActorRedirect(&http.Request{URL: target}, via)
		if (err == nil) != test.valid {
			t.Fatalf("checkActorRedirect(%q, %d) = %v", test.target, test.via, err)
		}
	}
}

func TestProductionHTTPClientSecuritySettings(t *testing.T) {
	client := newSafeHTTPClient()
	if client.Timeout != requestTimeout || client.CheckRedirect == nil {
		t.Fatalf("client settings = %#v", client)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy != nil || transport.DialContext == nil ||
		transport.MaxResponseHeaderBytes != maximumResponseHeaderByte ||
		transport.TLSClientConfig == nil ||
		transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("transport settings = %#v", transport)
	}
}
