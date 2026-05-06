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

func TestDeployProdPromotesExistingImageTag(t *testing.T) {
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
		"default: vnext",
		"timeout-minutes: 5",
		"SOURCE_TAG: ${{ github.event.inputs.source_tag }}",
		"Invalid source_tag: ${SOURCE_TAG}",
		"docker buildx imagetools create",
		"--tag moltenai/moltenhub-code:latest",
		"\"moltenai/moltenhub-code:${SOURCE_TAG}\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing promotion setting %q", workflowPath, want)
		}
	}
	for _, forbidden := range []string{
		"uses: docker/build-push-action",
		"uses: actions/checkout",
		"cache-from:",
		"provenance:",
		"sbom:",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("%s still rebuilds latest through %q", workflowPath, forbidden)
		}
	}
}
