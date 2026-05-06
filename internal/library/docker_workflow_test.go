package library

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeployVnextPublishesSupplyChainAttestations(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "deploy-vnext.yml")

	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	content := string(data)
	for _, want := range []string{
		"uses: docker/build-push-action@v7",
		"provenance: mode=max",
		"sbom: true",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing supply chain attestation setting %q", workflowPath, want)
		}
	}
}

func TestDeployProdPublishesSupplyChainAttestations(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "deploy-prod.yml")

	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", workflowPath, err)
	}

	content := string(data)
	for _, want := range []string{
		"uses: docker/build-push-action@v7",
		"tags: moltenai/moltenhub-code:latest",
		"provenance: mode=max",
		"sbom: true",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing supply chain attestation setting %q", workflowPath, want)
		}
	}
	if strings.Contains(content, "docker buildx imagetools create") {
		t.Fatalf("%s still promotes latest without build attestations", workflowPath)
	}
}
