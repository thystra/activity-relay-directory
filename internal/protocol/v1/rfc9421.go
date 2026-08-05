package v1

import (
	"context"
	"crypto/rsa"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/common-fate/httpsig/alg_rsa"
	"github.com/common-fate/httpsig/sigparams"
	"github.com/dunglas/httpsfv"
)

const (
	// RFC9421SignatureTag identifies signatures made for version 1 directory
	// operations rather than an unrelated application on the same message.
	RFC9421SignatureTag = "activity-relay-directory-v1"

	// RFC9421SignatureAlgorithm is the algorithm used with existing RSA relay
	// actor keys in version 1.
	RFC9421SignatureAlgorithm = alg_rsa.RSASSA_PKCS1_1_5_SHA256

	RFC9421MaximumAge      = 5 * time.Minute
	RFC9421FutureSkew      = 30 * time.Second
	RFC9421MaximumLifetime = 5 * time.Minute

	maxRFC9421KeyIDBytes     = 2048
	maxRFC9421NonceBytes     = 256
	maxRFC9421SignatureBytes = 1024
	maxRFC9421Components     = 32
	minimumRFC9421RSAKeyBits = 2048
)

var (
	ErrRFC9421Configuration = errors.New("RFC 9421 verifier configuration is invalid")
	ErrRFC9421Malformed     = errors.New("RFC 9421 signature fields are malformed")
	ErrRFC9421Policy        = errors.New("RFC 9421 signature violates the directory profile")
	ErrRFC9421Time          = errors.New("RFC 9421 signature time is invalid")
	ErrRFC9421Digest        = errors.New("RFC 9421 content digest is invalid")
	ErrRFC9421Key           = errors.New("RFC 9421 verification key is invalid")
	ErrRFC9421Crypto        = errors.New("RFC 9421 cryptographic verification failed")
	ErrRFC9421ActorBinding  = errors.New("RFC 9421 key identity does not match relay actor")
)

var rfc9421POSTComponents = []string{
	"@method",
	"@authority",
	"@target-uri",
	"content-digest",
	"content-type",
	"date",
}

// RFC9421POSTComponents returns the required covered components in the order
// version 1 clients should emit them. Verifiers permit additional components.
func RFC9421POSTComponents() []string {
	return append([]string(nil), rfc9421POSTComponents...)
}

// RFC9421ResolvedKey is caller-resolved ActivityPub RSA key material. Runtime
// wiring remains responsible for authenticated retrieval and network safety.
type RFC9421ResolvedKey struct {
	KeyID     string
	Owner     string
	ActorID   string
	PublicKey *rsa.PublicKey
}

// RFC9421KeyResolver resolves a signature key ID. The verifier never fetches a
// URL or performs DNS access directly.
type RFC9421KeyResolver interface {
	ResolveRFC9421Key(context.Context, string) (RFC9421ResolvedKey, error)
}

// RFC9421KeyResolverFunc adapts a function to RFC9421KeyResolver.
type RFC9421KeyResolverFunc func(
	context.Context,
	string,
) (RFC9421ResolvedKey, error)

func (resolver RFC9421KeyResolverFunc) ResolveRFC9421Key(
	ctx context.Context,
	keyID string,
) (RFC9421ResolvedKey, error) {
	if resolver == nil {
		return RFC9421ResolvedKey{}, ErrRFC9421Key
	}
	return resolver(ctx, keyID)
}

// RFC9421VerifierOptions configures the fixed version 1 verification profile.
type RFC9421VerifierOptions struct {
	Authority   string
	KeyResolver RFC9421KeyResolver
	Now         func() time.Time
}

// RFC9421Verifier verifies version 1 directory-operation POST signatures.
// VerifyPOST is stateless; handler code must use VerifyPOSTAndReserve with a
// durable replay store appropriate to the service topology.
type RFC9421Verifier struct {
	authority   string
	keyResolver RFC9421KeyResolver
	now         func() time.Time
}

// RFC9421Verification contains the identity and metadata established by a
// successful signature and digest verification.
type RFC9421Verification struct {
	KeyID              string
	KeyOwner           string
	KeyActor           string
	Nonce              string
	Created            time.Time
	Expires            time.Time
	CoveredComponents  []string
	SignatureAlgorithm string
}

// NewRFC9421Verifier validates and constructs a version 1 verifier.
func NewRFC9421Verifier(
	options RFC9421VerifierOptions,
) (*RFC9421Verifier, error) {
	authority := strings.TrimSpace(options.Authority)
	if authority == "" || authority != options.Authority {
		return nil, ErrRFC9421Configuration
	}

	base, err := NormalizePublicBaseURL("https://" + authority)
	if err != nil || strings.TrimPrefix(base, "https://") != authority {
		return nil, ErrRFC9421Configuration
	}
	if options.KeyResolver == nil {
		return nil, ErrRFC9421Configuration
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	return &RFC9421Verifier{
		authority:   authority,
		keyResolver: options.KeyResolver,
		now:         now,
	}, nil
}

type parsedRFC9421Message struct {
	params          sigparams.Params
	signatureParams string
	signature       []byte
}

func parseRFC9421Message(request *http.Request) (parsedRFC9421Message, error) {
	if len(request.Header.Values("Signature-Input")) == 0 ||
		len(request.Header.Values("Signature")) == 0 {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}

	inputs, err := httpsfv.UnmarshalDictionary(
		request.Header.Values("Signature-Input"),
	)
	if err != nil {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}
	signatures, err := httpsfv.UnmarshalDictionary(
		request.Header.Values("Signature"),
	)
	if err != nil {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}

	inputNames := inputs.Names()
	signatureNames := signatures.Names()
	if len(inputNames) != 1 || len(signatureNames) != 1 ||
		inputNames[0] != signatureNames[0] || len(inputNames[0]) > 64 {
		return parsedRFC9421Message{}, ErrRFC9421Policy
	}

	inputMember, _ := inputs.Get(inputNames[0])
	innerList, ok := inputMember.(httpsfv.InnerList)
	if !ok || len(innerList.Items) == 0 ||
		len(innerList.Items) > maxRFC9421Components {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}

	for _, item := range innerList.Items {
		if _, ok := item.Value.(string); !ok ||
			item.Params == nil || len(item.Params.Names()) != 0 {
			return parsedRFC9421Message{}, ErrRFC9421Malformed
		}
	}

	allowedParameters := map[string]struct{}{
		"alg":     {},
		"created": {},
		"expires": {},
		"keyid":   {},
		"nonce":   {},
		"tag":     {},
	}
	if innerList.Params == nil {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}
	for _, name := range innerList.Params.Names() {
		if _, ok := allowedParameters[name]; !ok {
			return parsedRFC9421Message{}, ErrRFC9421Policy
		}
	}

	params, err := sigparams.UnmarshalInnerList(innerList)
	if err != nil {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}
	signatureParams, err := httpsfv.Marshal(innerList)
	if err != nil {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}

	signatureMember, _ := signatures.Get(signatureNames[0])
	signatureItem, ok := signatureMember.(httpsfv.Item)
	if !ok || signatureItem.Params == nil ||
		len(signatureItem.Params.Names()) != 0 {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}
	signature, ok := signatureItem.Value.([]byte)
	if !ok || len(signature) == 0 ||
		len(signature) > maxRFC9421SignatureBytes {
		return parsedRFC9421Message{}, ErrRFC9421Malformed
	}

	return parsedRFC9421Message{
		params:          *params,
		signatureParams: signatureParams,
		signature:       append([]byte(nil), signature...),
	}, nil
}

func validRFC9421ComponentName(component string) bool {
	switch component {
	case "@method", "@authority", "@scheme", "@target-uri":
		return true
	}
	if component == "" || component[0] == '@' {
		return false
	}
	for index := 0; index < len(component); index++ {
		character := component[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validateRFC9421Parameters(
	params sigparams.Params,
	now time.Time,
) error {
	if params.Tag != RFC9421SignatureTag ||
		params.Alg != RFC9421SignatureAlgorithm {
		return ErrRFC9421Policy
	}
	if params.KeyID == "" || strings.TrimSpace(params.KeyID) != params.KeyID ||
		len(params.KeyID) > maxRFC9421KeyIDBytes {
		return ErrRFC9421Policy
	}
	if params.Nonce == "" || len(params.Nonce) > maxRFC9421NonceBytes {
		return ErrRFC9421Policy
	}
	if params.Created.IsZero() || params.Expires.IsZero() {
		return ErrRFC9421Time
	}
	if params.Created.Before(now.Add(-RFC9421MaximumAge)) ||
		params.Created.After(now.Add(RFC9421FutureSkew)) {
		return ErrRFC9421Time
	}
	if !params.Expires.After(params.Created) ||
		!params.Expires.After(now) ||
		params.Expires.After(params.Created.Add(RFC9421MaximumLifetime)) {
		return ErrRFC9421Time
	}

	required := make(map[string]struct{}, len(rfc9421POSTComponents))
	for _, component := range rfc9421POSTComponents {
		required[component] = struct{}{}
	}
	seen := make(map[string]struct{}, len(params.CoveredComponents))
	for _, component := range params.CoveredComponents {
		if !validRFC9421ComponentName(component) {
			return ErrRFC9421Policy
		}
		if _, duplicate := seen[component]; duplicate {
			return ErrRFC9421Policy
		}
		seen[component] = struct{}{}
		delete(required, component)
	}
	if len(required) != 0 {
		return ErrRFC9421Policy
	}

	return nil
}

func validateRFC9421Request(
	request *http.Request,
	authority string,
	now time.Time,
) error {
	if request.Method != http.MethodPost || request.URL == nil {
		return ErrRFC9421Policy
	}

	wireAuthority := request.Host
	if wireAuthority == "" {
		wireAuthority = request.URL.Host
	}
	if wireAuthority != authority ||
		(request.URL.Host != "" && request.URL.Host != authority) ||
		(request.URL.Scheme != "" && request.URL.Scheme != "https") ||
		request.URL.Fragment != "" {
		return ErrRFC9421Policy
	}
	if values := request.Header.Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
		return ErrRFC9421Policy
	}
	dateValues := request.Header.Values("Date")
	if len(dateValues) != 1 {
		return ErrRFC9421Time
	}
	date, err := http.ParseTime(dateValues[0])
	if err != nil || date.Before(now.Add(-RFC9421MaximumAge)) ||
		date.After(now.Add(RFC9421FutureSkew)) {
		return ErrRFC9421Time
	}

	return nil
}

func rfc9421ComponentValue(
	request *http.Request,
	authority string,
	component string,
) (string, error) {
	switch component {
	case "@method":
		return request.Method, nil
	case "@authority":
		return authority, nil
	case "@scheme":
		return "https", nil
	case "@target-uri":
		copiedURL := *request.URL
		copiedURL.Scheme = "https"
		copiedURL.Host = authority
		if copiedURL.Path == "" {
			copiedURL.Path = "/"
		}
		return copiedURL.String(), nil
	}

	values := request.Header.Values(component)
	if len(values) == 0 {
		return "", ErrRFC9421Policy
	}
	canonical := make([]string, len(values))
	for index, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return "", ErrRFC9421Policy
		}
		canonical[index] = strings.TrimSpace(value)
	}
	return strings.Join(canonical, ", "), nil
}

func canonicalRFC9421SigningString(
	request *http.Request,
	authority string,
	components []string,
	signatureParams string,
) (string, error) {
	if request == nil || request.URL == nil || signatureParams == "" {
		return "", ErrRFC9421Policy
	}

	var signingString strings.Builder
	for _, component := range components {
		value, err := rfc9421ComponentValue(request, authority, component)
		if err != nil {
			return "", err
		}
		signingString.WriteByte('"')
		signingString.WriteString(component)
		signingString.WriteString("\": ")
		signingString.WriteString(value)
		signingString.WriteByte('\n')
	}
	signingString.WriteString(`"@signature-params": `)
	signingString.WriteString(signatureParams)
	return signingString.String(), nil
}

func validateRFC9421ResolvedKey(
	resolved RFC9421ResolvedKey,
	keyID string,
) (string, string, error) {
	if resolved.KeyID != keyID || resolved.PublicKey == nil ||
		resolved.PublicKey.N == nil ||
		resolved.PublicKey.N.BitLen() < minimumRFC9421RSAKeyBits {
		return "", "", ErrRFC9421Key
	}

	owner, err := NormalizeRelayActorURL(resolved.Owner)
	if err != nil || owner != resolved.Owner {
		return "", "", ErrRFC9421Key
	}
	actor, err := NormalizeRelayActorURL(resolved.ActorID)
	if err != nil || actor != resolved.ActorID || actor != owner {
		return "", "", ErrRFC9421Key
	}

	return owner, actor, nil
}

// VerifyPOST verifies an RFC 9421 directory-operation POST and its exact body
// digest. A successful result has not yet reserved its nonce.
func (verifier *RFC9421Verifier) VerifyPOST(
	request *http.Request,
	body []byte,
) (*RFC9421Verification, error) {
	if verifier == nil || request == nil || request.Header == nil {
		return nil, ErrRFC9421Policy
	}

	now := time.Unix(verifier.now().UTC().Unix(), 0)
	if err := validateRFC9421Request(request, verifier.authority, now); err != nil {
		return nil, err
	}
	message, err := parseRFC9421Message(request)
	if err != nil {
		return nil, err
	}
	if err := validateRFC9421Parameters(message.params, now); err != nil {
		return nil, err
	}
	if err := VerifyRFC9530ContentDigestSHA256(
		request.Header.Values("Content-Digest"),
		body,
	); err != nil {
		return nil, ErrRFC9421Digest
	}

	resolved, err := verifier.keyResolver.ResolveRFC9421Key(
		request.Context(),
		message.params.KeyID,
	)
	if err != nil {
		return nil, ErrRFC9421Key
	}
	owner, actor, err := validateRFC9421ResolvedKey(
		resolved,
		message.params.KeyID,
	)
	if err != nil {
		return nil, err
	}

	signingString, err := canonicalRFC9421SigningString(
		request,
		verifier.authority,
		message.params.CoveredComponents,
		message.signatureParams,
	)
	if err != nil {
		return nil, ErrRFC9421Policy
	}
	if err := alg_rsa.NewRSAPKCS256Verifier(resolved.PublicKey).Verify(
		request.Context(),
		signingString,
		message.signature,
	); err != nil {
		return nil, ErrRFC9421Crypto
	}

	return &RFC9421Verification{
		KeyID:              message.params.KeyID,
		KeyOwner:           owner,
		KeyActor:           actor,
		Nonce:              message.params.Nonce,
		Created:            message.params.Created,
		Expires:            message.params.Expires,
		CoveredComponents:  append([]string(nil), message.params.CoveredComponents...),
		SignatureAlgorithm: message.params.Alg,
	}, nil
}

// BindRelayActor requires the verified key owner and resolved actor identity to
// match the canonical relay_actor carried by a directory request.
func (verification *RFC9421Verification) BindRelayActor(
	relayActor string,
) error {
	if verification == nil {
		return ErrRFC9421ActorBinding
	}
	actor, err := NormalizeRelayActorURL(relayActor)
	if err != nil || actor != relayActor ||
		actor != verification.KeyOwner || actor != verification.KeyActor {
		return ErrRFC9421ActorBinding
	}
	return nil
}
