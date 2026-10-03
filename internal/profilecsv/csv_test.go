package profilecsv

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thystra/activity-relay-directory/internal/storage"
)

func TestEncodeDecodeRoundTripAndFormulaProtection(t *testing.T) {
	records := []Record{{
		Relay: "https://relay.example/actor",
		Profile: storage.RelayProfile{
			ParticipationMode: "public",
			Languages:         []string{"fr", "en"},
			Topics:            []string{"+federation", "technology"},
			ContactFediverse:  "'@operator@example.org",
			Notes:             "=HYPERLINK(\"https://evil.example\")",
		},
	}}
	body, err := Encode(records)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "'=HYPERLINK") ||
		!strings.Contains(text, "''@operator@example.org") ||
		strings.Contains(text, ",=HYPERLINK") {
		t.Fatalf("formula protection output = %q", text)
	}
	decoded, err := Decode(body, "community_list", 100)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(decoded) != 1 || decoded[0].Relay != records[0].Relay ||
		decoded[0].Profile.Notes != records[0].Profile.Notes ||
		decoded[0].Profile.ContactFediverse != records[0].Profile.ContactFediverse ||
		strings.Join(decoded[0].Profile.Languages, ",") != "en,fr" ||
		strings.Join(decoded[0].Profile.Topics, ",") != "+federation,technology" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestDecodeHeaderOrderOptionalSourceURLAndNormalization(t *testing.T) {
	body := []byte("notes,relay,languages,source_url\nhello,relay.example,en; fr,https://source.example/list\n")
	records, err := Decode(body, "community_list", 100)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(records) != 1 || records[0].Line != 2 || records[0].Relay != "relay.example" ||
		records[0].Profile.Notes != "hello" || strings.Join(records[0].Profile.Languages, ",") != "en,fr" ||
		records[0].SourceURL != "https://source.example/list" {
		t.Fatalf("records = %#v", records)
	}
}

func TestDecodeRejectsInvalidShapeAndBounds(t *testing.T) {
	for _, body := range [][]byte{
		[]byte("notes\nmissing relay\n"),
		[]byte("relay,relay\nrelay.example,relay.example\n"),
		[]byte("relay,unknown\nrelay.example,x\n"),
		[]byte("relay,languages\nrelay.example,en;;fr\n"),
		[]byte("relay,contact_url\nrelay.example,http://unsafe.example/\n"),
	} {
		if records, err := Decode(body, "community_list", 100); err == nil || records != nil {
			t.Fatalf("Decode(%q) = %#v, %v", body, records, err)
		}
	}
	body := []byte("relay\none.example\ntwo.example\n")
	if records, err := Decode(body, "community_list", 1); err == nil || records != nil {
		t.Fatalf("Decode(limit) = %#v, %v", records, err)
	}
	if records, err := Decode(body, "bad label", 100); err == nil || records != nil {
		t.Fatalf("Decode(source label) = %#v, %v", records, err)
	}
}

func TestListEncodingRoundTripsSemicolonAndBackslash(t *testing.T) {
	values := []string{`alpha;beta`, `path\name`, `plain`}
	encoded := encodeList(values)
	if encoded != `alpha\;beta;path\\name;plain` {
		t.Fatalf("encodeList() = %q", encoded)
	}
	decoded, err := decodeList(encoded)
	if err != nil || strings.Join(decoded, "|") != `alpha;beta|path\name|plain` {
		t.Fatalf("decodeList() = %#v, %v", decoded, err)
	}
	for _, invalid := range []string{`alpha\q`, `alpha\`} {
		if decoded, err := decodeList(invalid); err == nil || decoded != nil {
			t.Fatalf("decodeList(%q) = %#v, %v", invalid, decoded, err)
		}
	}
}

func TestProtectCellIsReversibleForLeadingApostrophes(t *testing.T) {
	for _, value := range []string{"=x", "+x", "-x", "@x", "'x", "''x", "plain"} {
		protected := protectCell(value)
		if got := unprotectCell(protected); got != value {
			t.Fatalf("round trip %q -> %q -> %q", value, protected, got)
		}
	}
	if got := unprotectCell("'plain"); got != "'plain" {
		t.Fatalf("unprotect literal = %q", got)
	}
}

func TestEncodeRejectsInvalidRecord(t *testing.T) {
	if body, err := Encode([]Record{{Relay: "relay.example"}}); err == nil || body != nil {
		t.Fatalf("Encode(invalid actor) = %q, %v", body, err)
	}
	if body, err := Encode([]Record{{
		Relay: "https://relay.example/actor",
		Profile: storage.RelayProfile{
			Notes: string(bytes.Repeat([]byte{'x'}, storage.MaximumProfileNoteBytes+1)),
		},
	}}); err == nil || body != nil {
		t.Fatalf("Encode(invalid profile) = %q, %v", body, err)
	}
}
