package v1

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidRelayURL identifies a relay actor or public base URL that does not
// satisfy the version 1 canonical syntax. Errors never include the supplied
// URL because it may contain sensitive or attacker-controlled text.
var ErrInvalidRelayURL = errors.New("invalid relay URL")

// RelayIdentity is a canonical actor and same-origin public base URL pair.
type RelayIdentity struct {
	RelayActor    string
	PublicBaseURL string
}

// NormalizeRelayIdentity canonicalizes and binds a relay actor to its public
// base origin. It performs no DNS lookup or network-target safety decision.
func NormalizeRelayIdentity(
	relayActor string,
	publicBaseURL string,
) (RelayIdentity, error) {
	actor, err := normalizeRelayURL(relayActor, actorURL)
	if err != nil {
		return RelayIdentity{}, err
	}

	base, err := normalizeRelayURL(publicBaseURL, publicBaseURLKind)
	if err != nil {
		return RelayIdentity{}, err
	}

	if actor.origin != base.origin {
		return RelayIdentity{}, invalidRelayURL(
			"relay actor and public base URL must use the same origin",
		)
	}

	return RelayIdentity{
		RelayActor:    actor.value,
		PublicBaseURL: base.value,
	}, nil
}

// NormalizeRelayActorURL returns the canonical version 1 actor URL.
func NormalizeRelayActorURL(raw string) (string, error) {
	normalized, err := normalizeRelayURL(raw, actorURL)
	if err != nil {
		return "", err
	}
	return normalized.value, nil
}

// NormalizePublicBaseURL returns the canonical origin-only public base URL.
func NormalizePublicBaseURL(raw string) (string, error) {
	normalized, err := normalizeRelayURL(raw, publicBaseURLKind)
	if err != nil {
		return "", err
	}
	return normalized.value, nil
}

type relayURLKind uint8

const (
	actorURL relayURLKind = iota
	publicBaseURLKind
)

type normalizedRelayURL struct {
	value  string
	origin string
}

func normalizeRelayURL(
	raw string,
	kind relayURLKind,
) (normalizedRelayURL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return normalizedRelayURL{}, invalidRelayURL(
			"URL must be non-empty and contain no surrounding whitespace",
		)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return normalizedRelayURL{}, invalidRelayURL("URL syntax is invalid")
	}

	if parsed.Opaque != "" || !strings.EqualFold(parsed.Scheme, "https") {
		return normalizedRelayURL{}, invalidRelayURL("URL must use HTTPS")
	}
	if parsed.User != nil {
		return normalizedRelayURL{}, invalidRelayURL("URL must not contain user information")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return normalizedRelayURL{}, invalidRelayURL("URL must contain an authority")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return normalizedRelayURL{}, invalidRelayURL("URL must not contain a query")
	}
	if strings.Contains(raw, "#") {
		return normalizedRelayURL{}, invalidRelayURL("URL must not contain a fragment")
	}

	authority, err := normalizeAuthority(parsed)
	if err != nil {
		return normalizedRelayURL{}, err
	}

	origin := "https://" + authority
	if kind == publicBaseURLKind {
		path := parsed.EscapedPath()
		if path != "" && path != "/" {
			return normalizedRelayURL{}, invalidRelayURL(
				"public base URL must not contain a path",
			)
		}
		return normalizedRelayURL{value: origin, origin: origin}, nil
	}

	path, err := normalizeActorPath(parsed.EscapedPath())
	if err != nil {
		return normalizedRelayURL{}, err
	}

	return normalizedRelayURL{
		value:  origin + path,
		origin: origin,
	}, nil
}

func normalizeAuthority(parsed *url.URL) (string, error) {
	host := parsed.Hostname()
	if strings.Contains(host, "%") {
		return "", invalidRelayURL("IPv6 zone identifiers are not permitted")
	}

	host = strings.ToLower(host)
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	if host == "" || strings.HasSuffix(host, ".") {
		return "", invalidRelayURL("hostname is invalid")
	}

	normalizedHost, err := normalizeHost(host)
	if err != nil {
		return "", err
	}

	if strings.HasSuffix(parsed.Host, ":") {
		return "", invalidRelayURL("port is empty")
	}

	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return "", invalidRelayURL("port is invalid")
		}
		if portNumber != 443 {
			normalizedHost += ":" + strconv.FormatUint(portNumber, 10)
		}
	}

	return normalizedHost, nil
}

func normalizeHost(host string) (string, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		if address.Is6() {
			return "[" + address.String() + "]", nil
		}
		return address.String(), nil
	}

	if numericHost(host) {
		return "", invalidRelayURL("numeric hostname is not a canonical IP address")
	}
	if len(host) > 253 {
		return "", invalidRelayURL("hostname is too long")
	}

	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", invalidRelayURL("hostname must be fully qualified")
	}
	for _, label := range labels {
		if !validDNSLabel(label) {
			return "", invalidRelayURL("hostname is invalid")
		}
	}
	if numericHost(labels[len(labels)-1]) {
		return "", invalidRelayURL("hostname suffix must not be numeric")
	}

	return host, nil
}

func validDNSLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, character := range label {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func numericHost(host string) bool {
	if host == "" {
		return false
	}
	for _, character := range host {
		if (character < '0' || character > '9') && character != '.' {
			return false
		}
	}
	return true
}

func normalizeActorPath(escaped string) (string, error) {
	if escaped == "" {
		return "/", nil
	}
	if escaped[0] != '/' {
		return "", invalidRelayURL("actor path must be absolute")
	}

	const upperHex = "0123456789ABCDEF"
	var normalized strings.Builder
	normalized.Grow(len(escaped))

	for index := 0; index < len(escaped); index++ {
		character := escaped[index]
		if character != '%' {
			if character == '\\' || character < 0x20 || character == 0x7f {
				return "", invalidRelayURL("actor path contains a forbidden character")
			}
			normalized.WriteByte(character)
			continue
		}

		if index+2 >= len(escaped) {
			return "", invalidRelayURL("actor path has invalid percent encoding")
		}
		high, okHigh := hexValue(escaped[index+1])
		low, okLow := hexValue(escaped[index+2])
		if !okHigh || !okLow {
			return "", invalidRelayURL("actor path has invalid percent encoding")
		}

		value := high<<4 | low
		if value == '%' || value == '/' || value == '\\' || value < 0x20 || value == 0x7f {
			return "", invalidRelayURL("actor path contains a forbidden escape")
		}
		if unreserved(value) {
			normalized.WriteByte(value)
		} else {
			normalized.WriteByte('%')
			normalized.WriteByte(upperHex[value>>4])
			normalized.WriteByte(upperHex[value&0x0f])
		}
		index += 2
	}

	path := normalized.String()
	decoded, err := url.PathUnescape(path)
	if err != nil || !utf8.ValidString(decoded) {
		return "", invalidRelayURL("actor path must contain valid UTF-8")
	}
	for _, character := range decoded {
		if unicode.IsControl(character) {
			return "", invalidRelayURL("actor path contains a control character")
		}
	}
	if strings.Contains(path, "//") {
		return "", invalidRelayURL("actor path must not contain empty segments")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return "", invalidRelayURL("actor path must not contain dot segments")
		}
	}

	return path, nil
}

func hexValue(character byte) (byte, bool) {
	switch {
	case character >= '0' && character <= '9':
		return character - '0', true
	case character >= 'a' && character <= 'f':
		return character - 'a' + 10, true
	case character >= 'A' && character <= 'F':
		return character - 'A' + 10, true
	default:
		return 0, false
	}
}

func unreserved(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') ||
		strings.ContainsRune("-._~", rune(character))
}

func invalidRelayURL(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRelayURL, reason)
}
