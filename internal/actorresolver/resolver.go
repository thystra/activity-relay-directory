// Package actorresolver provides bounded, SSRF-resistant ActivityPub actor,
// endpoint-probe, and RSA signing-key resolution.
package actorresolver

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	v1 "github.com/thystra/activity-relay-directory/internal/protocol/v1"
)

const (
	maximumKeyIDBytes       = 2048
	maximumKeyFragmentBytes = 128
	maximumActorBodyBytes   = 256 * 1024
	maximumActorJSONDepth   = 32
	maximumJSONEntries      = 4096
	maximumActorTypes       = 8
	maximumPublicKeys       = 8
	maximumPublicKeyPEM     = 16 * 1024
	minimumRSAKeyBits       = 2048
	maximumRSAKeyBits       = 8192
	maximumUserAgentBytes   = 256
	activityStreamsProfile  = "https://www.w3.org/ns/activitystreams"
	activityStreamsAccept   = `application/activity+json, application/ld+json; profile="https://www.w3.org/ns/activitystreams"`
)

var (
	ErrConfiguration = errors.New("actor resolver configuration is invalid")
	ErrKeyID         = errors.New("ActivityPub signing-key identifier is invalid")
	ErrNetworkTarget = errors.New("ActivityPub network target is prohibited")
	ErrActorFetch    = errors.New("ActivityPub actor retrieval failed")
	ErrActorDocument = errors.New("ActivityPub actor document is invalid")
	ErrPublicKey     = errors.New("ActivityPub RSA public key is invalid")
)

// ActorProbeResult is the validated identity/capability result of one actor
// fetch. InboxURL is empty when the actor document does not declare an inbox.
type ActorProbeResult struct {
	ActorID  string
	InboxURL string
}

// InboxProbeResult is the conservative outcome of one non-mutating inbox
// diagnostic. It does not imply that a POST was attempted.
type InboxProbeResult string

const (
	InboxProbeResponsive     InboxProbeResult = "responsive"
	InboxProbeMethodRejected InboxProbeResult = "method_rejected"
	InboxProbeUnreachable    InboxProbeResult = "unreachable"
)

func (result InboxProbeResult) Valid() bool {
	return result == InboxProbeResponsive || result == InboxProbeMethodRejected ||
		result == InboxProbeUnreachable
}

// Resolver resolves RFC 9421 keys and performs bounded ActivityPub endpoint
// probes through one shared SSRF-resistant transport.
type Resolver struct {
	client    *http.Client
	userAgent string
}

var _ v1.RFC9421KeyResolver = (*Resolver)(nil)

// New constructs the production resolver with a proxy-free, address-pinning
// HTTPS client. Construction does not perform DNS or network access.
func New(userAgent string) (*Resolver, error) {
	return newResolver(userAgent, newSafeHTTPClient())
}

func newResolver(userAgent string, client *http.Client) (*Resolver, error) {
	if client == nil || userAgent == "" || len(userAgent) > maximumUserAgentBytes ||
		strings.TrimSpace(userAgent) != userAgent || !utf8.ValidString(userAgent) ||
		!validUserAgent(userAgent) {
		return nil, ErrConfiguration
	}
	return &Resolver{client: client, userAgent: userAgent}, nil
}

func validUserAgent(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// ResolveRFC9421Key retrieves and validates one actor-owned RSA public key.
func (resolver *Resolver) ResolveRFC9421Key(
	ctx context.Context,
	keyID string,
) (v1.RFC9421ResolvedKey, error) {
	if resolver == nil || resolver.client == nil || ctx == nil {
		return v1.RFC9421ResolvedKey{}, ErrConfiguration
	}
	actorURL, err := actorURLFromKeyID(keyID)
	if err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	body, err := resolver.fetchActorBody(ctx, actorURL)
	if err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	return resolveActorDocument(body, actorURL, keyID)
}

// ProbeActor retrieves and validates one canonical ActivityPub relay actor
// without requiring a particular public key. This is the reachability/discovery
// primitive; it deliberately shares the exact production fetch boundary used by
// RFC 9421 key resolution.
func (resolver *Resolver) ProbeActor(ctx context.Context, actorURL string) (ActorProbeResult, error) {
	if resolver == nil || resolver.client == nil || ctx == nil {
		return ActorProbeResult{}, ErrConfiguration
	}
	canonical, err := v1.NormalizeRelayActorURL(actorURL)
	if err != nil || canonical != actorURL {
		return ActorProbeResult{}, ErrActorDocument
	}
	parsed, err := url.Parse(actorURL)
	if err != nil || validateActorFetchURL(parsed) != nil {
		return ActorProbeResult{}, ErrNetworkTarget
	}
	body, err := resolver.fetchActorBody(ctx, actorURL)
	if err != nil {
		return ActorProbeResult{}, err
	}
	return probeActorDocument(body, actorURL)
}

// ProbeInbox performs one bounded OPTIONS request against a canonical inbox. A
// successful actor declaration is the capability evidence; this result is only
// an additional non-mutating diagnostic.
func (resolver *Resolver) ProbeInbox(ctx context.Context, inboxURL string) (InboxProbeResult, error) {
	if resolver == nil || resolver.client == nil || ctx == nil {
		return "", ErrConfiguration
	}
	canonical, err := v1.NormalizeRelayActorURL(inboxURL)
	if err != nil || canonical != inboxURL {
		return "", ErrActorDocument
	}
	parsed, err := url.Parse(inboxURL)
	if err != nil || validateActorFetchURL(parsed) != nil {
		return "", ErrNetworkTarget
	}
	if err := ctx.Err(); err != nil {
		return "", errors.Join(ErrActorFetch, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodOptions, inboxURL, nil)
	if err != nil {
		return "", ErrActorFetch
	}
	request.Header.Set("User-Agent", resolver.userAgent)
	response, err := resolver.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", errors.Join(ErrActorFetch, ctx.Err())
		}
		return InboxProbeUnreachable, nil
	}
	if response == nil || response.Body == nil {
		return InboxProbeUnreachable, nil
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 400:
		return InboxProbeResponsive, nil
	case response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented:
		return InboxProbeMethodRejected, nil
	default:
		return InboxProbeUnreachable, nil
	}
}

func (resolver *Resolver) fetchActorBody(ctx context.Context, actorURL string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrActorFetch, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURL, nil)
	if err != nil {
		return nil, ErrActorFetch
	}
	request.Header.Set("Accept", activityStreamsAccept)
	request.Header.Set("User-Agent", resolver.userAgent)

	response, err := resolver.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(ErrActorFetch, ctx.Err())
		}
		return nil, ErrActorFetch
	}
	if response == nil || response.Body == nil {
		return nil, ErrActorFetch
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK ||
		!validActivityStreamsContentType(response.Header.Values("Content-Type")) {
		return nil, ErrActorFetch
	}
	if response.ContentLength > maximumActorBodyBytes {
		return nil, ErrActorFetch
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumActorBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maximumActorBodyBytes {
		return nil, ErrActorFetch
	}
	return body, nil
}

func actorURLFromKeyID(keyID string) (string, error) {
	if keyID == "" || len(keyID) > maximumKeyIDBytes ||
		strings.TrimSpace(keyID) != keyID || !utf8.ValidString(keyID) {
		return "", ErrKeyID
	}
	parsed, err := url.Parse(keyID)
	if err != nil || parsed.Fragment == "" || parsed.RawFragment != "" ||
		len(parsed.Fragment) > maximumKeyFragmentBytes ||
		!validKeyFragment(parsed.Fragment) {
		return "", ErrKeyID
	}
	fragment := parsed.Fragment
	parsed.Fragment = ""
	parsed.RawFragment = ""
	actorURL := parsed.String()
	canonical, err := v1.NormalizeRelayActorURL(actorURL)
	if err != nil || canonical != actorURL || keyID != actorURL+"#"+fragment {
		return "", ErrKeyID
	}
	if err := validateActorFetchURL(parsed); err != nil {
		return "", err
	}
	return actorURL, nil
}

func validKeyFragment(fragment string) bool {
	for _, character := range fragment {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("-._~", character) {
			continue
		}
		return false
	}
	return true
}

func validActivityStreamsContentType(values []string) bool {
	if len(values) != 1 || len(values[0]) > 512 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil {
		return false
	}
	if charset, present := parameters["charset"]; present {
		if !strings.EqualFold(charset, "utf-8") {
			return false
		}
		delete(parameters, "charset")
	}
	switch strings.ToLower(mediaType) {
	case "application/activity+json":
		if profile, present := parameters["profile"]; present {
			if len(strings.Fields(profile)) == 0 {
				return false
			}
			delete(parameters, "profile")
		}
	case "application/ld+json":
		profile, present := parameters["profile"]
		if !present || !profileContainsActivityStreams(profile) {
			return false
		}
		delete(parameters, "profile")
	default:
		return false
	}
	return len(parameters) == 0
}

func profileContainsActivityStreams(profile string) bool {
	for _, candidate := range strings.Fields(profile) {
		if candidate == activityStreamsProfile {
			return true
		}
	}
	return false
}

type actorDocument struct {
	ID        string          `json:"id"`
	Type      json.RawMessage `json:"type"`
	Inbox     string          `json:"inbox"`
	PublicKey json.RawMessage `json:"publicKey"`
}

type actorPublicKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

func probeActorDocument(body []byte, actorURL string) (ActorProbeResult, error) {
	if err := validateJSONDocument(body); err != nil {
		return ActorProbeResult{}, err
	}
	var document actorDocument
	if err := json.Unmarshal(body, &document); err != nil ||
		document.ID != actorURL || !validRelayActorType(document.Type) {
		return ActorProbeResult{}, ErrActorDocument
	}
	result := ActorProbeResult{ActorID: document.ID}
	if document.Inbox != "" {
		canonical, err := v1.NormalizeRelayActorURL(document.Inbox)
		if err != nil || canonical != document.Inbox {
			return ActorProbeResult{}, ErrActorDocument
		}
		parsed, err := url.Parse(document.Inbox)
		if err != nil || validateActorFetchURL(parsed) != nil {
			return ActorProbeResult{}, ErrNetworkTarget
		}
		result.InboxURL = document.Inbox
	}
	return result, nil
}

func resolveActorDocument(
	body []byte,
	actorURL string,
	keyID string,
) (v1.RFC9421ResolvedKey, error) {
	if err := validateJSONDocument(body); err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	var document actorDocument
	if err := json.Unmarshal(body, &document); err != nil ||
		document.ID != actorURL || !validRelayActorType(document.Type) {
		return v1.RFC9421ResolvedKey{}, ErrActorDocument
	}

	keys, err := decodeActorPublicKeys(document.PublicKey)
	if err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	var selected *actorPublicKey
	for index := range keys {
		if keys[index].ID != keyID {
			continue
		}
		if selected != nil {
			return v1.RFC9421ResolvedKey{}, ErrActorDocument
		}
		selected = &keys[index]
	}
	if selected == nil || selected.Owner != actorURL {
		return v1.RFC9421ResolvedKey{}, ErrActorDocument
	}
	owner, err := v1.NormalizeRelayActorURL(selected.Owner)
	if err != nil || owner != selected.Owner {
		return v1.RFC9421ResolvedKey{}, ErrActorDocument
	}

	publicKey, err := parseRSAPublicKey(selected.PublicKeyPEM)
	if err != nil {
		return v1.RFC9421ResolvedKey{}, err
	}
	return v1.RFC9421ResolvedKey{
		KeyID:     selected.ID,
		Owner:     owner,
		ActorID:   document.ID,
		PublicKey: publicKey,
	}, nil
}

func validRelayActorType(raw json.RawMessage) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == "Application" || single == "Service"
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil || len(multiple) == 0 ||
		len(multiple) > maximumActorTypes {
		return false
	}
	for _, actorType := range multiple {
		if actorType == "Application" || actorType == "Service" {
			return true
		}
	}
	return false
}

func decodeActorPublicKeys(raw json.RawMessage) ([]actorPublicKey, error) {
	var single actorPublicKey
	if json.Unmarshal(raw, &single) == nil && single.ID != "" {
		return []actorPublicKey{single}, nil
	}
	var multiple []actorPublicKey
	if json.Unmarshal(raw, &multiple) != nil || len(multiple) == 0 ||
		len(multiple) > maximumPublicKeys {
		return nil, ErrActorDocument
	}
	for _, key := range multiple {
		if key.ID == "" {
			return nil, ErrActorDocument
		}
	}
	return multiple, nil
}

func parseRSAPublicKey(value string) (*rsa.PublicKey, error) {
	if value == "" || len(value) > maximumPublicKeyPEM ||
		!utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		!strings.HasPrefix(value, "-----BEGIN ") {
		return nil, ErrPublicKey
	}
	block, trailing := pem.Decode([]byte(value))
	if block == nil || len(bytes.TrimSpace(trailing)) != 0 ||
		len(block.Headers) != 0 {
		return nil, ErrPublicKey
	}

	var publicKey *rsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, ErrPublicKey
		}
		var ok bool
		publicKey, ok = parsed.(*rsa.PublicKey)
		if !ok {
			return nil, ErrPublicKey
		}
	case "RSA PUBLIC KEY":
		parsed, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, ErrPublicKey
		}
		publicKey = parsed
	default:
		return nil, ErrPublicKey
	}
	if publicKey == nil || publicKey.N == nil ||
		publicKey.N.Sign() <= 0 || publicKey.N.Bit(0) == 0 ||
		publicKey.N.BitLen() < minimumRSAKeyBits ||
		publicKey.N.BitLen() > maximumRSAKeyBits || publicKey.E < 3 ||
		publicKey.E%2 == 0 {
		return nil, ErrPublicKey
	}
	return publicKey, nil
}

func validateJSONDocument(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, 0); err != nil {
		return ErrActorDocument
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrActorDocument
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumActorJSONDepth {
		return ErrActorDocument
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		entries := 0
		for decoder.More() {
			entries++
			if entries > maximumJSONEntries {
				return ErrActorDocument
			}
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return ErrActorDocument
			}
			if _, duplicate := seen[name]; duplicate {
				return ErrActorDocument
			}
			seen[name] = struct{}{}
			if err := consumeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrActorDocument
		}
	case '[':
		entries := 0
		for decoder.More() {
			entries++
			if entries > maximumJSONEntries {
				return ErrActorDocument
			}
			if err := consumeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrActorDocument
		}
	default:
		return ErrActorDocument
	}
	return nil
}
