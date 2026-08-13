package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canonicalReleaseWorkflow(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", ".forgejo", "workflows", "release.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical release workflow: %v", err)
	}
	return string(body)
}

func TestCanonicalReleaseWorkflowDispatchInputsAreShellData(t *testing.T) {
	workflow := canonicalReleaseWorkflow(t)

	required := []string{
		"RELEASE_EXPECTED_COMMIT: ${{ inputs.expected_commit }}",
		"RELEASE_VERSION: ${{ inputs.version }}",
		"RELEASE_CONFIRMATION: ${{ inputs.confirm }}",
		`VERSION="$RELEASE_VERSION"`,
		`test "$RELEASE_CONFIRMATION" = "BUILD $VERSION"`,
		`test "$(git rev-parse HEAD)" = "$RELEASE_EXPECTED_COMMIT"`,
	}
	for _, marker := range required {
		if !strings.Contains(workflow, marker) {
			t.Fatalf("canonical release workflow missing input-safety marker %q", marker)
		}
	}

	lines := strings.Split(workflow, "\n")
	inRun := false
	runIndent := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)

		if strings.HasPrefix(strings.TrimSpace(line), "run:") {
			inRun = true
			runIndent = indent
			continue
		}
		if !inRun || strings.TrimSpace(line) == "" {
			continue
		}
		if indent <= runIndent {
			inRun = false
			continue
		}
		if strings.Contains(line, "${{ inputs.") {
			t.Fatalf("canonical release run body directly interpolates workflow_dispatch input: %q", line)
		}
	}
}

func TestCanonicalReleaseWorkflowInstallsDebianToolsBeforeUse(t *testing.T) {
	workflow := canonicalReleaseWorkflow(t)

	dispatch := strings.Index(workflow, "- name: Require exact reviewed dispatch identity")
	install := strings.Index(workflow, "- name: Install Debian packaging tools")
	source := strings.Index(workflow, "- name: Require exact source package version")
	firstParse := strings.Index(workflow, "dpkg-parsechangelog -SVersion")
	goSetup := strings.Index(workflow, "- name: Set up exact Go 1.26.5")

	for name, pos := range map[string]int{
		"dispatch identity":         dispatch,
		"Debian tool install":       install,
		"source-version check":      source,
		"first dpkg-parsechangelog": firstParse,
		"Go setup":                  goSetup,
	} {
		if pos < 0 {
			t.Fatalf("canonical release workflow missing %s", name)
		}
	}
	if !(dispatch < install && install < source && source <= firstParse && firstParse < goSetup) {
		t.Fatalf(
			"canonical release tool ordering = dispatch:%d install:%d source:%d parse:%d go:%d",
			dispatch, install, source, firstParse, goSetup,
		)
	}
	if !strings.Contains(workflow, "debhelper dpkg-dev fakeroot lintian") {
		t.Fatal("canonical release workflow does not explicitly install dpkg-dev with packaging tools")
	}
}
