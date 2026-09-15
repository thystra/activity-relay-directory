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
