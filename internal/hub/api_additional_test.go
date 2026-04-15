package hub

import "testing"

func TestToMapParsesMapAndJSONString(t *testing.T) {
	t.Parallel()

	inputMap := map[string]any{"k": "v"}
	if got := toMap(inputMap); got["k"] != "v" {
		t.Fatalf("toMap(map) = %#v, want key k=v", got)
	}

	if got := toMap(`{"a":"b"}`); got["a"] != "b" {
		t.Fatalf("toMap(json string) = %#v, want key a=b", got)
	}
	if got := toMap(" "); got != nil {
		t.Fatalf("toMap(blank string) = %#v, want nil", got)
	}
	if got := toMap("{invalid"); got != nil {
		t.Fatalf("toMap(invalid json string) = %#v, want nil", got)
	}
}

func TestExtractAgentProfileFromJSONPrefersNestedAgentAndMetadata(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"ok": true,
		"result": {
			"agent": {
				"handle": "molten-agent",
				"metadata": {
					"display_name": "Molten Agent",
					"emoji": "🔥",
					"profile": "Builds production changes"
				}
			}
		}
	}`))

	if got, want := profile.Handle, "molten-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := profile.Profile.DisplayName, "Molten Agent"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Emoji, "🔥"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := profile.Profile.ProfileText, "Builds production changes"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestExtractAgentProfileFromJSONUsesExplicitProfileObject(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"data": {
			"handle": "builder-two",
			"profile": {
				"display_name": "Builder Two",
				"emoji": "🤖",
				"profile": "Owns UI automation",
				"llm": "claude",
				"harness": "moltenhub-code",
				"skills": ["code_for_me"]
			}
		}
	}`))

	if got, want := profile.Handle, "builder-two"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := profile.Profile.DisplayName, "Builder Two"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Emoji, "🤖"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := profile.Profile.ProfileText, "Owns UI automation"; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
	if got, want := profile.Profile.LLM, "claude"; got != want {
		t.Fatalf("LLM = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Harness, "moltenhub-code"; got != want {
		t.Fatalf("Harness = %q, want %q", got, want)
	}
	if len(profile.Profile.Skills) != 1 || profile.Profile.Skills[0] != "code_for_me" {
		t.Fatalf("Skills = %#v, want preserved explicit [code_for_me]", profile.Profile.Skills)
	}
}

func TestExtractAgentProfileFromJSONUsesProfileMarkdownBodyWhenProfileMissing(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"result": {
			"agent": {
				"handle": "markdown-agent",
				"metadata": {
					"display_name": "Markdown Agent",
					"emoji": "💯",
					"profile_markdown": "# 💯 Markdown Agent\n\nOwns remediation tasks."
				}
			}
		}
	}`))

	if got, want := profile.Handle, "markdown-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := profile.Profile.DisplayName, "Markdown Agent"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Emoji, "💯"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := profile.Profile.ProfileText, "Owns remediation tasks."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestExtractAgentProfileFromJSONIgnoresHeaderOnlyProfileMarkdown(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"result": {
			"agent": {
				"metadata": {
					"display_name": "Header Only",
					"emoji": "😀",
					"profile_markdown": "# 😀 Header Only"
				}
			}
		}
	}`))

	if got := profile.Profile.ProfileText; got != "" {
		t.Fatalf("ProfileText = %q, want empty for header-only markdown", got)
	}
}

func TestExtractAgentProfileFromJSONDerivesProfileHeaderFromMarkdownWhenMetadataCompact(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"ok": true,
		"result": {
			"agent": {
				"handle": "compact-agent",
				"metadata": {
					"profile_markdown": "# 🌊 Compact Agent\n\nOwns compact profile payloads."
				}
			}
		}
	}`))

	if got, want := profile.Handle, "compact-agent"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := profile.Profile.DisplayName, "Compact Agent"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Emoji, "🌊"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := profile.Profile.ProfileText, "Owns compact profile payloads."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestExtractAgentProfileFromJSONMergesExplicitProfileWithMetadata(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"ok": true,
		"result": {
			"agent": {
				"handle": "codex-beast",
				"profile": {
					"display_name": "Jef's Codex"
				},
				"metadata": {
					"profile_markdown": "# 🦍 Jef's Codex\n\nRunning code updates quickly."
				}
			}
		}
	}`))

	if got, want := profile.Handle, "codex-beast"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := profile.Profile.DisplayName, "Jef's Codex"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Emoji, "🦍"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := profile.Profile.ProfileText, "Running code updates quickly."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}

func TestExtractAgentProfileFromJSONMergesDirectProfileAndMetadata(t *testing.T) {
	t.Parallel()

	profile := extractAgentProfileFromJSON([]byte(`{
		"handle": "codex-beast",
		"profile": {
			"display_name": "Jef's Codex",
			"emoji": "🦍"
		},
		"metadata": {
			"profile": "Running code updates quickly."
		}
	}`))

	if got, want := profile.Handle, "codex-beast"; got != want {
		t.Fatalf("Handle = %q, want %q", got, want)
	}
	if got, want := profile.Profile.DisplayName, "Jef's Codex"; got != want {
		t.Fatalf("DisplayName = %q, want %q", got, want)
	}
	if got, want := profile.Profile.Emoji, "🦍"; got != want {
		t.Fatalf("Emoji = %q, want %q", got, want)
	}
	if got, want := profile.Profile.ProfileText, "Running code updates quickly."; got != want {
		t.Fatalf("ProfileText = %q, want %q", got, want)
	}
}
