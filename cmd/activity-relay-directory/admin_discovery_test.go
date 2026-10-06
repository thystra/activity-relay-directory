package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thystra/activity-relay-directory/internal/actorresolver"
	"github.com/thystra/activity-relay-directory/internal/discoverycommand"
)

func TestAdminDiscoveryAddPersistsVerifiedObservationWithoutLifecycleState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	prober := &adminDiscoveryProber{
		actor: actorresolver.ActorProbeResult{
			ActorID:  "https://relay.example/actor",
			InboxURL: "https://relay.example/inbox",
		},
		inbox: actorresolver.InboxProbeMethodRejected,
	}
	var stdout, stderr bytes.Buffer
	code := runDiscoveryAdminWithProberFactory(
		[]string{
			"activity-relay-directory", "admin", "discovery", "add",
			"--url", "https://relay.example/inbox",
			"--operator", "operator",
			"--reason", "public_relay",
			"--source-label", "manual_review",
			"--yes",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(200, 0).UTC() },
		func() (discoverycommand.Prober, error) { return prober, nil },
	)
	if code != 0 || !strings.Contains(stdout.String(), "status=ok") ||
		!strings.Contains(stderr.String(), "discovery prospective:") {
		t.Fatalf("discovery add = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}

	database, err = initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer database.Close()
	var discoveryState, publicBase string
	if err := database.QueryRow(`SELECT discovery_state, public_base_url
		FROM relay_discoveries WHERE relay_actor = ?`, "https://relay.example/actor").Scan(
		&discoveryState, &publicBase,
	); err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	if discoveryState != "active" || publicBase != "https://relay.example" {
		t.Fatalf("discovery = (%q, %q)", discoveryState, publicBase)
	}
	var actorState, inboxURL, inboxState string
	var rfc9421 any
	if err := database.QueryRow(`SELECT actor_state, inbox_url, inbox_probe_state,
		rfc9421_verified_at_unix FROM relay_observations WHERE relay_actor = ?`,
		"https://relay.example/actor").Scan(&actorState, &inboxURL, &inboxState, &rfc9421); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if actorState != "reachable" || inboxURL != "https://relay.example/inbox" ||
		inboxState != "method_rejected" || rfc9421 != nil {
		t.Fatalf("observation = (%q, %q, %q, %v)", actorState, inboxURL, inboxState, rfc9421)
	}
	var lifecycleCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relays WHERE relay_actor = ?`,
		"https://relay.example/actor").Scan(&lifecycleCount); err != nil {
		t.Fatalf("count lifecycle rows: %v", err)
	}
	if lifecycleCount != 0 {
		t.Fatalf("discovery fabricated %d lifecycle rows", lifecycleCount)
	}
}

func TestAdminDiscoveryRequiresConfirmationAfterProbeBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	prober := &adminDiscoveryProber{actor: actorresolver.ActorProbeResult{ActorID: "https://relay.example/actor"}}
	var stdout, stderr bytes.Buffer
	code := runDiscoveryAdminWithProberFactory(
		[]string{
			"activity-relay-directory", "admin", "discovery", "add",
			"--url", "https://relay.example/",
			"--operator", "operator",
			"--reason", "public_relay",
		},
		strings.NewReader("wrong\n"), &stdout, &stderr,
		func() time.Time { return time.Unix(200, 0).UTC() },
		func() (discoverycommand.Prober, error) { return prober, nil },
	)
	if code != discoverycommand.ExitUsage || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "discovery confirmation failed") {
		t.Fatalf("unconfirmed discovery = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}

	database, err = initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_discoveries`).Scan(&count); err != nil {
		t.Fatalf("count discoveries: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed confirmation created %d discoveries", count)
	}
}

func TestAdminDiscoveryImportStoresLabelNotFilePathAndRemoveNeedsNoProbe(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	t.Setenv("DIRECTORY_DATABASE_PATH", path)
	candidatePath := filepath.Join(directory, "private-operator-path.txt")
	if err := osWriteFile(candidatePath, []byte("https://relay.example/\n")); err != nil {
		t.Fatalf("write candidates: %v", err)
	}

	prober := &adminDiscoveryProber{actor: actorresolver.ActorProbeResult{ActorID: "https://relay.example/actor"}}
	var stdout, stderr bytes.Buffer
	code := runDiscoveryAdminWithProberFactory(
		[]string{
			"activity-relay-directory", "admin", "discovery", "import",
			"--file", candidatePath,
			"--operator", "operator",
			"--reason", "public_list",
			"--source-label", "curated_list",
			"--yes",
		},
		strings.NewReader(""), &stdout, &stderr,
		func() time.Time { return time.Unix(200, 0).UTC() },
		func() (discoverycommand.Prober, error) { return prober, nil },
	)
	if code != 0 {
		t.Fatalf("discovery import = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}

	database, err = initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	var sourceLabel string
	if err := database.QueryRow(`SELECT source_label FROM discovery_events
		WHERE relay_actor = ? ORDER BY discovery_event_id DESC LIMIT 1`, "https://relay.example/actor").Scan(&sourceLabel); err != nil {
		database.Close()
		t.Fatalf("read discovery event: %v", err)
	}
	if sourceLabel != "curated_list" || strings.Contains(sourceLabel, directory) {
		database.Close()
		t.Fatalf("source label = %q", sourceLabel)
	}
	database.Close()

	stdout.Reset()
	stderr.Reset()
	code = runDiscoveryAdminWithProberFactory(
		[]string{
			"activity-relay-directory", "admin", "discovery", "remove",
			"--actor", "https://relay.example/actor",
			"--operator", "operator",
			"--reason", "no_longer_public",
			"--yes",
		},
		strings.NewReader(""), &stdout, &stderr,
		func() time.Time { return time.Unix(201, 0).UTC() },
		nil,
	)
	if code != 0 || !strings.Contains(stdout.String(), "outcome=removed") {
		t.Fatalf("discovery remove = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}

func TestAdminDiscoveryCSVImportPersistsProfileAndExportsSpreadsheetSafeCSV(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	candidatePath := filepath.Join(directory, "relays.csv")
	if err := osWriteFile(candidatePath, []byte(
		"relay,notes,languages,source_url\n"+
			"relay.example,=support,fr; en,https://source.example/list\n"),
	); err != nil {
		t.Fatalf("write CSV candidates: %v", err)
	}
	prober := &adminDiscoveryProber{
		actor: actorresolver.ActorProbeResult{ActorID: "https://relay.example/actor"},
	}
	var stdout, stderr bytes.Buffer
	code := runDiscoveryAdminWithProberFactory(
		[]string{
			"activity-relay-directory", "admin", "discovery", "import",
			"--file", candidatePath,
			"--input-format", "csv",
			"--operator", "operator",
			"--reason", "public_list",
			"--source-label", "curated_list",
			"--yes",
		},
		strings.NewReader(""), &stdout, &stderr,
		func() time.Time { return time.Unix(200, 0).UTC() },
		func() (discoverycommand.Prober, error) { return prober, nil },
	)
	if code != discoverycommand.ExitSuccess || !strings.Contains(stdout.String(), "profile_changes=2") {
		t.Fatalf("CSV discovery import = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}

	database, err = initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	var valueJSON, sourceLabel, sourceURL string
	if err := database.QueryRow(`SELECT value_json, source_label, source_url
		FROM relay_profile_values
		WHERE relay_actor = ? AND source_kind = 'csv' AND field_name = 'notes'`,
		"https://relay.example/actor",
	).Scan(&valueJSON, &sourceLabel, &sourceURL); err != nil {
		database.Close()
		t.Fatalf("read CSV profile: %v", err)
	}
	if valueJSON != `"=support"` || sourceLabel != "curated_list" || sourceURL != "https://source.example/list" {
		database.Close()
		t.Fatalf("CSV profile = (%q, %q, %q)", valueJSON, sourceLabel, sourceURL)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close profile database: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = runAdmin(
		[]string{
			"activity-relay-directory", "admin", "export",
			"--scope", "all",
			"--format", "csv",
		},
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(201, 0).UTC() },
	)
	if code != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "https://relay.example/actor") ||
		!strings.Contains(stdout.String(), "'=support") ||
		strings.Contains(stdout.String(), "https://source.example/list") {
		t.Fatalf("CSV admin export = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}

func TestAdminDiscoveryImportReportsRegisteredLifecycleRelayAsAlreadyKnown(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "directory.sqlite")
	database, err := initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("initializeDatabase() error = %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO relays (
			relay_actor,
			public_base_url,
			lifecycle_state,
			administrative_state,
			first_registered_at_unix,
			updated_at_unix,
			last_seen_at_unix
		) VALUES (?, ?, 'registered', 'active', 100, 100, 100)`,
		"https://relay.example/actor",
		"https://relay.example",
	); err != nil {
		database.Close()
		t.Fatalf("seed lifecycle relay: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	t.Setenv("DIRECTORY_DATABASE_PATH", path)

	candidatePath := filepath.Join(directory, "relays.txt")
	if err := osWriteFile(candidatePath, []byte("relay.example\n")); err != nil {
		t.Fatalf("write candidates: %v", err)
	}
	prober := &adminDiscoveryProber{
		actor: actorresolver.ActorProbeResult{ActorID: "https://relay.example/actor"},
	}
	var stdout, stderr bytes.Buffer
	code := runDiscoveryAdminWithProberFactory(
		[]string{
			"activity-relay-directory", "admin", "discovery", "import",
			"--file", candidatePath,
			"--operator", "operator",
			"--reason", "public_list",
			"--source-label", "curated_list",
			"--yes",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(200, 0).UTC() },
		func() (discoverycommand.Prober, error) { return prober, nil },
	)
	if code != discoverycommand.ExitSuccess ||
		!strings.Contains(stderr.String(), "ready=0 retained=0 already_known=1 duplicate_input=0 failed=0") ||
		!strings.Contains(stderr.String(),
			"already_known line=1 actor=https://relay.example/actor source=lifecycle") ||
		!strings.Contains(stdout.String(),
			"line=1 status=already_known actor=https://relay.example/actor") {
		t.Fatalf("known lifecycle import = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}

	database, err = initializeDatabase(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer database.Close()
	var discoveryCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM relay_discoveries
		WHERE relay_actor = ?`, "https://relay.example/actor").Scan(&discoveryCount); err != nil {
		t.Fatalf("count discoveries: %v", err)
	}
	if discoveryCount != 0 {
		t.Fatalf("already-known lifecycle relay created %d discovery rows", discoveryCount)
	}
}

type adminDiscoveryProber struct {
	actor actorresolver.ActorProbeResult
	inbox actorresolver.InboxProbeResult
}

func (prober *adminDiscoveryProber) ProbeActor(context.Context, string) (actorresolver.ActorProbeResult, error) {
	return prober.actor, nil
}

func (prober *adminDiscoveryProber) ProbeInbox(context.Context, string) (actorresolver.InboxProbeResult, error) {
	if prober.inbox == "" {
		return actorresolver.InboxProbeUnreachable, nil
	}
	return prober.inbox, nil
}

func osWriteFile(path string, body []byte) error {
	return os.WriteFile(path, body, 0o600)
}
