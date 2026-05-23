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

func TestComposeKeepsSpeechLanguageDefaultDeterministic(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	composePath := filepath.Join(repoRoot, "docker-compose.yml")
	envExamplePath := filepath.Join(repoRoot, ".env.example")

	composeData, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", composePath, err)
	}
	envExampleData, err := os.ReadFile(envExamplePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", envExamplePath, err)
	}

	compose := string(composeData)
	if !strings.Contains(compose, `MOLTEN_HUB_SPEECH_LANGUAGE: "${MOLTEN_HUB_SPEECH_LANGUAGE:-en}"`) {
		t.Fatalf("%s must default hub speech requests to English unless MOLTEN_HUB_SPEECH_LANGUAGE is explicit", composePath)
	}
	if strings.Contains(compose, `MOLTEN_HUB_SPEECH_LANGUAGE: "${MOLTEN_HUB_SPEECH_LANGUAGE:-${WHISPER_LANG:-en}}"`) {
		t.Fatalf("%s still lets WHISPER_LANG=auto disable the hub speech language hint", composePath)
	}
	if !strings.Contains(string(envExampleData), "WHISPER_LANG=en") {
		t.Fatalf("%s must keep the sample speech sidecar language aligned with the hub default", envExamplePath)
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
		"bump:",
		"description: Version bump to apply when creating the release tag",
		"type: choice",
		"first_release:",
		"description: First release tag to create when no vX.Y.Z tag exists",
		"default: v1.0.0",
		"name: Publish GitHub Release artifacts",
		"fetch-depth: 0",
		"fetch-tags: true",
		"id: release",
		"BUMP: ${{ github.event.inputs.bump }}",
		"FIRST_RELEASE: ${{ github.event.inputs.first_release }}",
		"Release tag already exists: ${release_tag}",
		"git tag -a \"${RELEASE_TAG}\" -m \"Release ${RELEASE_TAG}\"",
		"git push origin \"refs/tags/${RELEASE_TAG}\"",
		"git checkout --detach \"${RELEASE_TAG}\"",
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
