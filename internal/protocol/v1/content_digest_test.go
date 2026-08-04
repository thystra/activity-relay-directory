package v1

import (
	"errors"
	"strings"
	"testing"
)

const followContentDigest = "sha-256=:GYwYnH3BiO6aICFt0ThC5bUIJ4byvqdpWtR8m5fNkww=:"

func TestRFC9530ContentDigestSHA256(t *testing.T) {
	fixture := decodeFixture[contentDigestFixture](t, "content-digest.valid.json")

	got, err := RFC9530ContentDigestSHA256([]byte(fixture.Body))
	if err != nil {
		t.Fatalf("generate Content-Digest: %v", err)
	}
	if got != fixture.ContentDigest || got != followContentDigest {
		t.Fatalf("Content-Digest = %q, want %q", got, fixture.ContentDigest)
	}

	if err := VerifyRFC9530ContentDigestSHA256(
		[]string{got},
		[]byte(fixture.Body),
	); err != nil {
		t.Fatalf("verify Content-Digest: %v", err)
	}
}

func TestRFC9530ContentDigestSHA256EmptyBody(t *testing.T) {
	got, err := RFC9530ContentDigestSHA256(nil)
	if err != nil {
		t.Fatalf("generate Content-Digest: %v", err)
	}
	want := "sha-256=:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=:"
	if got != want {
		t.Fatalf("Content-Digest = %q, want %q", got, want)
	}
}

func TestVerifyRFC9530ContentDigestSHA256AllowsAdditionalAlgorithms(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	values := []string{
		"sha-512=:AA==:",
		followContentDigest,
	}
	if err := VerifyRFC9530ContentDigestSHA256(values, body); err != nil {
		t.Fatalf("verify multiple Content-Digest field lines: %v", err)
	}
}

func TestVerifyRFC9530ContentDigestSHA256UsesLastDuplicate(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	wrong := "sha-256=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=:"

	if err := VerifyRFC9530ContentDigestSHA256(
		[]string{wrong, followContentDigest},
		body,
	); err != nil {
		t.Fatalf("verify last valid duplicate: %v", err)
	}

	err := VerifyRFC9530ContentDigestSHA256(
		[]string{followContentDigest, wrong},
		body,
	)
	if !errors.Is(err, ErrContentDigestMismatch) {
		t.Fatalf("last invalid duplicate error = %v", err)
	}
}

func TestVerifyRFC9530ContentDigestSHA256RejectsInvalidValues(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	for _, test := range []struct {
		name   string
		values []string
		want   error
	}{
		{name: "missing", want: ErrContentDigestMissing},
		{name: "malformed", values: []string{"not a dictionary"}, want: ErrContentDigestMalformed},
		{name: "missing sha-256", values: []string{"sha-512=:AA==:"}, want: ErrContentDigestSHA256Missing},
		{name: "inner list", values: []string{"sha-256=(:AA==:)"}, want: ErrContentDigestSHA256Invalid},
		{name: "string", values: []string{`sha-256="secret-presented-value"`}, want: ErrContentDigestSHA256Invalid},
		{name: "wrong length", values: []string{"sha-256=:AA==:"}, want: ErrContentDigestSHA256Invalid},
		{name: "mismatch", values: []string{"sha-256=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=:"}, want: ErrContentDigestMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := VerifyRFC9530ContentDigestSHA256(test.values, body)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifyRFC9530ContentDigestSHA256UsesExactBytes(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	for _, changed := range [][]byte{
		[]byte(" " + string(body)),
		[]byte(string(body) + "\n"),
		[]byte(`{"type": "Follow"}`),
	} {
		err := VerifyRFC9530ContentDigestSHA256(
			[]string{followContentDigest},
			changed,
		)
		if !errors.Is(err, ErrContentDigestMismatch) {
			t.Fatalf("changed body error = %v", err)
		}
	}
}

func TestContentDigestErrorsDoNotEchoFieldValues(t *testing.T) {
	secret := "secret-presented-value"
	for _, value := range []string{
		`sha-256="` + secret + `"`,
		`sha-256=:` + secret,
	} {
		err := VerifyRFC9530ContentDigestSHA256(
			[]string{value},
			[]byte(`{"type":"Follow"}`),
		)
		if err == nil {
			t.Fatal("invalid digest unexpectedly passed verification")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error discloses supplied field material: %v", err)
		}
	}
}

type contentDigestFixture struct {
	Body          string `json:"body"`
	ContentDigest string `json:"content_digest"`
}

func FuzzVerifyRFC9530ContentDigestSHA256(f *testing.F) {
	for _, seed := range []string{
		followContentDigest,
		"sha-512=:AA==:, " + followContentDigest,
		"sha-256=:AA==:",
		`sha-256="not bytes"`,
		"not a dictionary",
	} {
		f.Add(seed, []byte(`{"type":"Follow"}`))
	}

	f.Fuzz(func(t *testing.T, value string, body []byte) {
		_ = VerifyRFC9530ContentDigestSHA256([]string{value}, body)
	})
}
