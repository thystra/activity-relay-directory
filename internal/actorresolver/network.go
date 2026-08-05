package actorresolver

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	maximumDNSAddresses       = 16
	maximumRedirects          = 3
	requestTimeout            = 10 * time.Second
	dialTimeout               = 5 * time.Second
	tlsHandshakeTimeout       = 5 * time.Second
	responseHeaderTimeout     = 5 * time.Second
	maximumResponseHeaderByte = 32 * 1024
)

type lookupNetIPFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type safeDialer struct {
	lookup lookupNetIPFunc
	dial   dialContextFunc
}

func newSafeHTTPClient() *http.Client {
	resolver := &net.Resolver{PreferGo: true}
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	safe := safeDialer{
		lookup: resolver.LookupNetIP,
		dial:   dialer.DialContext,
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            safe.DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maximumResponseHeaderByte,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       requestTimeout,
		CheckRedirect: checkActorRedirect,
	}
}

func checkActorRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil || len(via) > maximumRedirects {
		return ErrNetworkTarget
	}
	return validateActorFetchURL(request.URL)
}

func validateActorFetchURL(target *url.URL) error {
	if target == nil {
		return ErrNetworkTarget
	}
	raw := target.String()
	canonical, err := v1.NormalizeRelayActorURL(raw)
	if err != nil || canonical != raw {
		return ErrNetworkTarget
	}
	if address, err := netip.ParseAddr(target.Hostname()); err == nil &&
		!isPublicNetworkAddress(address) {
		return ErrNetworkTarget
	}
	return nil
}

func (safe safeDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	if ctx == nil || safe.lookup == nil || safe.dial == nil {
		return nil, ErrNetworkTarget
	}
	host, port, err := net.SplitHostPort(address)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || portErr != nil || portNumber == 0 ||
		strconv.FormatUint(portNumber, 10) != port {
		return nil, ErrNetworkTarget
	}

	addresses, err := safe.approvedAddresses(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, approved := range addresses {
		if network == "tcp4" && !approved.Is4() {
			continue
		}
		if network == "tcp6" && !approved.Is6() {
			continue
		}
		connection, dialErr := safe.dial(
			ctx,
			network,
			net.JoinHostPort(approved.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}
		if ctx.Err() != nil {
			return nil, errors.Join(ErrNetworkTarget, ctx.Err())
		}
	}
	return nil, ErrNetworkTarget
}

func (safe safeDialer) approvedAddresses(
	ctx context.Context,
	host string,
) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !isPublicNetworkAddress(address) {
			return nil, ErrNetworkTarget
		}
		return []netip.Addr{address}, nil
	}

	addresses, err := safe.lookup(ctx, "ip", host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(ErrNetworkTarget, ctx.Err())
		}
		return nil, ErrNetworkTarget
	}
	if len(addresses) == 0 || len(addresses) > maximumDNSAddresses {
		return nil, ErrNetworkTarget
	}

	approved := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicNetworkAddress(address) {
			return nil, ErrNetworkTarget
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		approved = append(approved, address)
	}
	if len(approved) == 0 {
		return nil, ErrNetworkTarget
	}
	return approved, nil
}

var prohibitedNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var allocatedGlobalIPv6Prefix = netip.MustParsePrefix("2000::/3")

func isPublicNetworkAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if address.Is6() && !allocatedGlobalIPv6Prefix.Contains(address) {
		return false
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() ||
		address.IsLoopback() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prohibited := range prohibitedNetworkPrefixes {
		if prohibited.Contains(address) {
			return false
		}
	}
	return !strings.Contains(address.String(), "%")
}
