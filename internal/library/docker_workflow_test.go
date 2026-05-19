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
		"cache-from:",
		"provenance:",
		"sbom:",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("%s still rebuilds latest through %q", workflowPath, forbidden)
		}
	}
}

func TestDeployProdPublishesGoReleaserGitHubRelease(t *testing.T) {
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
		"contents: write",
		"release_tag:",
		"description: Git tag to publish to GitHub Releases",
		"name: Publish GitHub Release artifacts",
		"ref: ${{ github.event.inputs.release_tag }}",
		"fetch-depth: 0",
		"Invalid release_tag: ${RELEASE_TAG}",
		"Missing release tag: ${RELEASE_TAG}",
		"uses: goreleaser/goreleaser-action@v7",
		"distribution: goreleaser",
		"version: '~> v2'",
		"args: release --clean",
		"GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"needs: release",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing GoReleaser release setting %q", workflowPath, want)
		}
	}
}

func TestDeployVnextDoesNotPublishGoReleaserRelease(t *testing.T) {
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
	for _, forbidden := range []string{
		"goreleaser/goreleaser-action",
		"release_tag:",
		"args: release --clean",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("%s publishes GoReleaser release through %q", workflowPath, forbidden)
		}
	}
}

func TestGoReleaserPublishesHarnessArtifactsToGitHubRelease(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	configPath := filepath.Join(repoRoot, ".goreleaser.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", configPath, err)
	}

	content := string(data)
	for _, want := range []string{
		"version: 2",
		"project_name: moltenhub-code",
		"main: ./cmd/harness",
		"binary: harness",
		"CGO_ENABLED=0",
		"- -trimpath",
		"- -s -w",
		"- darwin",
		"- linux",
		"- windows",
		"- amd64",
		"- arm64",
		"goos: windows",
		"goarch: arm64",
		"formats:",
		"- tar.gz",
		"- zip",
		"name_template: checksums.txt",
		"release:",
		"github:",
		"owner: Molten-Bot",
		"name: moltenhub-code",
		"prerelease: auto",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing GoReleaser setting %q", configPath, want)
		}
	}
}
