package hub

import "testing"

func BenchmarkRedactSensitiveLogText_NoSensitiveData(b *testing.B) {
	b.ReportAllocs()

	input := "dispatch request_id=req-42 stage=pr status=ok workspace=/tmp/run branch=moltenhub-speedup"
	for i := 0; i < b.N; i++ {
		_ = redactSensitiveLogText(input)
	}
}

func BenchmarkRedactSensitiveLogText_WithSensitiveData(b *testing.B) {
	b.ReportAllocs()

	input := `bind_token=bind_123 token=agent_123 {"agent_token":"agent_abc","authorization":"Bearer secret_xyz"} Authorization: Bearer top_secret`
	for i := 0; i < b.N; i++ {
		_ = redactSensitiveLogText(input)
	}
}
