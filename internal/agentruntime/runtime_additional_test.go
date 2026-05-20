package agentruntime

import "testing"

func TestDisplayNameFallsBackForUnknownHarness(t *testing.T) {
	t.Parallel()

	if got := DisplayName("unknown-runtime"); got != "Codex" {
		t.Fatalf("DisplayName(unknown) = %q, want Codex", got)
	}
}

func TestUnsupportedPromptImagesErrorFallsBackWhenDisplayLabelEmpty(t *testing.T) {
	original := harnessDisplayNames[defaultHarness]
	harnessDisplayNames[defaultHarness] = ""
	t.Cleanup(func() { harnessDisplayNames[defaultHarness] = original })

	err := UnsupportedPromptImagesError("unknown-runtime")
	if err == nil {
		t.Fatal("UnsupportedPromptImagesError() = nil, want error")
	}
	if got, want := err.Error(), " does not support prompt images:"; len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("UnsupportedPromptImagesError() = %q, want empty label fallback prefix %q", got, want)
	}
}

func TestUnsupportedPromptImagesErrorWhenNoSupportedHarnessLabels(t *testing.T) {
	originalHarnesses := promptImageHarnesses
	promptImageHarnesses = map[string]struct{}{}
	t.Cleanup(func() { promptImageHarnesses = originalHarnesses })

	err := UnsupportedPromptImagesError(HarnessClaude)
	if err == nil {
		t.Fatal("UnsupportedPromptImagesError() = nil, want error")
	}
	if got, want := err.Error(), "Claude does not support prompt images: prompt images are unsupported for this agent harness"; got != want {
		t.Fatalf("UnsupportedPromptImagesError() = %q, want %q", got, want)
	}
}

func TestSupportedPromptImageHarnessLabelsSkipsBlankAndDuplicateLabels(t *testing.T) {
	originalNames := harnessDisplayNames
	originalHarnesses := promptImageHarnesses
	harnessDisplayNames = map[string]string{
		HarnessCodex: "Agent",
		"blank":      " ",
	}
	promptImageHarnesses = map[string]struct{}{
		HarnessCodex: {},
		"blank":      {},
	}
	t.Cleanup(func() {
		harnessDisplayNames = originalNames
		promptImageHarnesses = originalHarnesses
	})

	if got := supportedPromptImageHarnessLabels(); got != "Agent" {
		t.Fatalf("supportedPromptImageHarnessLabels() = %q, want Agent", got)
	}
}

func TestSupportedPromptImageHarnessLabelsFormatsMultiple(t *testing.T) {
	originalNames := harnessDisplayNames
	originalHarnesses := promptImageHarnesses
	harnessDisplayNames = map[string]string{
		HarnessCodex:  "Codex",
		HarnessClaude: "Claude",
	}
	promptImageHarnesses = map[string]struct{}{
		HarnessCodex:  {},
		HarnessClaude: {},
	}
	t.Cleanup(func() {
		harnessDisplayNames = originalNames
		promptImageHarnesses = originalHarnesses
	})

	if got := supportedPromptImageHarnessLabels(); got != "Claude or Codex" {
		t.Fatalf("supportedPromptImageHarnessLabels() = %q, want two-item list", got)
	}
}

func TestValidatePromptImageSupportAllowsSupportedHarness(t *testing.T) {
	t.Parallel()

	if err := validatePromptImageSupport(HarnessCodex, []string{"screenshot.png"}); err != nil {
		t.Fatalf("validatePromptImageSupport() error = %v, want nil", err)
	}
}
