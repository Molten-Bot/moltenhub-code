package hubui

import (
	"encoding/base64"
	"testing"
)

func BenchmarkBrokerIngestLogDispatchStatus(b *testing.B) {
	b.ReportAllocs()

	broker := NewBroker()
	line := "dispatch status=start request_id=req-bench skill=codex_harness_run repo=git@github.com:acme/repo.git"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		broker.IngestLog(line)
	}
}

func BenchmarkBrokerIngestLogCommandOutput(b *testing.B) {
	b.ReportAllocs()

	broker := NewBroker()
	encoded := base64.StdEncoding.EncodeToString([]byte("thinking..."))
	line := "dispatch request_id=req-bench cmd phase=codex name=codex stream=stdout b64=" + encoded

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		broker.IngestLog(line)
	}
}
