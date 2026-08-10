package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestContainerWorkflowsUseNode24DockerActionMajors(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowDirectory := filepath.Join(repositoryRoot, ".github", "workflows")

	var workflowPaths []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(workflowDirectory, pattern))
		if err != nil {
			t.Fatalf("glob workflow pattern %q: %v", pattern, err)
		}
		workflowPaths = append(workflowPaths, matches...)
	}
	sort.Strings(workflowPaths)
	if len(workflowPaths) == 0 {
		t.Fatal("no GitHub workflows were found")
	}

	retired := []string{
		"docker/setup-buildx-action@v3",
		"docker/build-push-action@v6",
	}
	for _, workflowPath := range workflowPaths {
		contents, err := os.ReadFile(workflowPath)
		if err != nil {
			t.Fatalf("read workflow %s: %v", workflowPath, err)
		}
		workflow := string(contents)
		for _, action := range retired {
			if strings.Contains(workflow, action) {
				t.Fatalf("workflow %s still contains %q", workflowPath, action)
			}
		}
	}

	buildPath := filepath.Join(workflowDirectory, "build.yml")
	contents, err := os.ReadFile(buildPath)
	if err != nil {
		t.Fatalf("read container workflow: %v", err)
	}
	buildWorkflow := string(contents)
	for _, required := range []string{
		"docker/setup-buildx-action@v4",
		"docker/build-push-action@v7",
	} {
		if !strings.Contains(buildWorkflow, required) {
			t.Fatalf("container workflow is missing %q", required)
		}
	}
}
