package v1

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/dunglas/httpsfv"
)

var (
	// ErrContentDigestMissing identifies a request without a Content-Digest
	// field line.
	ErrContentDigestMissing = errors.New("Content-Digest field is missing")

	// ErrContentDigestMalformed identifies a Content-Digest value that is not
	// an RFC 8941 dictionary. The parser detail is deliberately not returned
	// because field values are attacker-controlled.
	ErrContentDigestMalformed = errors.New("Content-Digest field is malformed")

	// ErrContentDigestSHA256Missing identifies a parsed dictionary without the
	// mandatory sha-256 member used by directory protocol version 1.
	ErrContentDigestSHA256Missing = errors.New("Content-Digest has no sha-256 member")

	// ErrContentDigestSHA256Invalid identifies a sha-256 member that is not a
	// 32-byte RFC 8941 Byte Sequence.
	ErrContentDigestSHA256Invalid = errors.New("Content-Digest sha-256 member is invalid")

	// ErrContentDigestMismatch identifies a validly encoded digest that does
	// not match the exact message content bytes.
	ErrContentDigestMismatch = errors.New("Content-Digest sha-256 value does not match message content")
)

// RFC9530ContentDigestSHA256 returns an RFC 9530 Content-Digest field value
// over the exact message content bytes.
func RFC9530ContentDigestSHA256(body []byte) (string, error) {
	sum := sha256.Sum256(body)
	dictionary := httpsfv.NewDictionary()
	dictionary.Add("sha-256", httpsfv.NewItem(sum[:]))

	value, err := httpsfv.Marshal(dictionary)
	if err != nil {
		return "", fmt.Errorf("marshal RFC 9530 Content-Digest: %w", err)
	}
	return value, nil
}

// VerifyRFC9530ContentDigestSHA256 validates the sha-256 member of one or more
// Content-Digest field lines against the exact message content bytes.
func VerifyRFC9530ContentDigestSHA256(values []string, body []byte) error {
	if len(values) == 0 {
		return ErrContentDigestMissing
	}

	dictionary, err := httpsfv.UnmarshalDictionary(values)
	if err != nil {
		return ErrContentDigestMalformed
	}

	member, ok := dictionary.Get("sha-256")
	if !ok {
		return ErrContentDigestSHA256Missing
	}

	item, ok := member.(httpsfv.Item)
	if !ok {
		return ErrContentDigestSHA256Invalid
	}

	presented, ok := item.Value.([]byte)
	if !ok || len(presented) != sha256.Size {
		return ErrContentDigestSHA256Invalid
	}

	expected := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(presented, expected[:]) != 1 {
		return ErrContentDigestMismatch
	}

	return nil
}
