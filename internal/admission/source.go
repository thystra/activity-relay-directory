// Package admission provides bounded, in-memory request admission primitives
// for explicitly enabled lifecycle handlers.
package admission

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

var (
	// ErrSourceConfiguration reports an invalid trusted-proxy configuration.
	// It never includes a supplied address or prefix.
	ErrSourceConfiguration = errors.New("source identity configuration is invalid")
	// ErrSourceIdentity reports a missing or malformed transport source.
	// It never includes a supplied address or header value.
	ErrSourceIdentity = errors.New("request source identity is invalid")
)

// SourceResolver derives a canonical client address from an HTTP request.
// Forwarded identity is accepted only from an explicitly trusted proxy peer.
type SourceResolver struct {
	trustedProxies []netip.Prefix
}

// NewSourceResolver constructs a resolver from exact trusted-proxy networks.
// An empty list trusts no proxy. Callers should prefer host prefixes (/32 and
// /128) over broad LAN ranges.
func NewSourceResolver(trustedProxies []netip.Prefix) (*SourceResolver, error) {
	normalized := make([]netip.Prefix, 0, len(trustedProxies))
	for _, prefix := range trustedProxies {
		if !prefix.IsValid() {
			return nil, ErrSourceConfiguration
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, ErrSourceConfiguration
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefix = prefix.Masked()
		if !validSourceAddress(prefix.Addr()) {
			return nil, ErrSourceConfiguration
		}
		if slices.Contains(normalized, prefix) {
			continue
		}
		normalized = append(normalized, prefix)
	}

	return &SourceResolver{trustedProxies: normalized}, nil
}

// Source returns the direct peer unless that peer is trusted. A trusted peer
// must overwrite X-Real-IP with exactly one canonical client address. The
// appendable Forwarded and X-Forwarded-For fields are deliberately ignored.
func (resolver *SourceResolver) Source(request *http.Request) (netip.Addr, error) {
	if resolver == nil || request == nil {
		return netip.Addr{}, ErrSourceIdentity
	}

	peer, err := parseRemoteAddress(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	if !resolver.trusts(peer) {
		return peer, nil
	}

	values := request.Header.Values("X-Real-IP")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return netip.Addr{}, ErrSourceIdentity
	}
	client, err := netip.ParseAddr(strings.TrimSpace(values[0]))
	if err != nil {
		return netip.Addr{}, ErrSourceIdentity
	}
	client = client.Unmap().WithZone("")
	if !validSourceAddress(client) {
		return netip.Addr{}, ErrSourceIdentity
	}
	return client, nil
}

func (resolver *SourceResolver) trusts(address netip.Addr) bool {
	for _, prefix := range resolver.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseRemoteAddress(remoteAddress string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return netip.Addr{}, ErrSourceIdentity
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, ErrSourceIdentity
	}
	address = address.Unmap().WithZone("")
	if !validSourceAddress(address) {
		return netip.Addr{}, ErrSourceIdentity
	}
	return address, nil
}

func validSourceAddress(address netip.Addr) bool {
	return address.IsValid() && !address.IsUnspecified() && !address.IsMulticast()
}
