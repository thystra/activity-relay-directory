package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCanonicalBundlePreservesExecutableModeAndDeterministicMetadata(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("canonical release tooling is Linux-only")
	}
	for _, command := range []string{"bash", "tar", "sha256sum"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("release-tool regression requires %s: %v", command, err)
		}
	}

	const (
		version = "9.9.9"
		epoch   = int64(1791054000)
	)
	root := t.TempDir()
	publicDir := filepath.Join(root, "public")
	evidenceDir := filepath.Join(root, "evidence")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	metadata := strings.Join([]string{
		"package=activity-relay-directory",
		"application_version=" + version,
		"debian_version=9.9.9-1",
		"architecture=amd64",
		fmt.Sprintf("source_date_epoch=%d", epoch),
		"source_identity=test-commit:test-tree",
		"",
	}, "\n")
	files := map[string][]byte{
		"BUILD-METADATA.txt":                                    []byte(metadata),
		"activity-relay-directory_9.9.9-1_amd64.deb":            []byte("deb fixture\n"),
		"activity-relay-directory_9.9.9_amd64.cdx.json":         []byte("{}\n"),
		"activity-relay-directory_9.9.9_linux_amd64":            []byte("#!/bin/sh\n[ \"$1\" = \"--version\" ] && printf '9.9.9\\n'\n"),
		"activity-relay-directory_9.9.9_linux_amd64.docker.tar": []byte("docker fixture\n"),
	}
	for name, body := range files {
		mode := os.FileMode(0o644)
		if name == "activity-relay-directory_9.9.9_linux_amd64" {
			mode = 0o755
		}
		path := filepath.Join(publicDir, name)
		if err := os.WriteFile(path, body, mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "proof.txt"), []byte("evidence\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		digest := sha256.Sum256(files[name])
		fmt.Fprintf(&sums, "%x  %s\n", digest, name)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Deliberately give the source tree inconsistent mtimes. The carrier must
	// normalize every tar member to the recorded release epoch.
	odd := time.Unix(epoch+86400, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, odd, odd)
	}); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "release", "package-canonical-bundle.sh")
	bundleA := filepath.Join(t.TempDir(), "canonical-a.tar")
	bundleB := filepath.Join(t.TempDir(), "canonical-b.tar")
	for _, output := range []string{bundleA, bundleB} {
		cmd := exec.Command("bash", script, root, output)
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("package canonical bundle: %v\n%s", err, combined)
		}
	}
	bytesA, err := os.ReadFile(bundleA)
	if err != nil {
		t.Fatal(err)
	}
	bytesB, err := os.ReadFile(bundleB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytesA, bytesB) {
		t.Fatal("canonical transport bundle is not byte-reproducible")
	}

	reader := tar.NewReader(bytes.NewReader(bytesA))
	seenBinary := false
	members := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read canonical tar: %v", err)
		}
		members++
		if header.ModTime.Unix() != epoch {
			t.Fatalf("member %q mtime = %d, want %d", header.Name, header.ModTime.Unix(), epoch)
		}
		if header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("member %q owner = %d:%d, want 0:0", header.Name, header.Uid, header.Gid)
		}
		if filepath.IsAbs(header.Name) || strings.Contains(header.Name, "../") {
			t.Fatalf("unsafe tar member %q", header.Name)
		}
		if header.Name == "public/activity-relay-directory_9.9.9_linux_amd64" {
			seenBinary = true
			if header.Mode&0o111 == 0 {
				t.Fatalf("standalone binary mode = %#o, executable bits lost", header.Mode)
			}
		}
	}
	if members == 0 || !seenBinary {
		t.Fatalf("canonical tar members=%d seenBinary=%t", members, seenBinary)
	}
}
