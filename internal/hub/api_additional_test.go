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
