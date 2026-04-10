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
		t.Fatalf("Skills = %#v, want [code_for_me]", profile.Profile.Skills)
	}
}
