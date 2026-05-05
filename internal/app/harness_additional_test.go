package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPathIsDirAndSleepWithContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if !pathIsDir(dir) {
		t.Fatalf("pathIsDir(%q) = false, want true", dir)
	}

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if pathIsDir(filePath) {
		t.Fatalf("pathIsDir(%q) = true, want false", filePath)
	}
	if pathIsDir(filepath.Join(dir, "missing")) {
		t.Fatal("pathIsDir(missing) = true, want false")
	}

	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepWithContext(0) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(ctx, 10*time.Millisecond); err == nil {
		t.Fatal("sleepWithContext(canceled) error = nil, want non-nil")
	}
}

func TestPromptImageExtensionMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mediaType string
		want      string
	}{
		{mediaType: "image/jpeg", want: ".jpg"},
		{mediaType: "image/jpg", want: ".jpg"},
		{mediaType: "image/gif", want: ".gif"},
		{mediaType: "image/webp", want: ".webp"},
		{mediaType: "image/png", want: ".png"},
		{mediaType: "", want: ".png"},
		{mediaType: "application/octet-stream", want: ".img"},
	}
	for _, tt := range tests {
		if got := promptImageExtension(tt.mediaType); got != tt.want {
			t.Fatalf("promptImageExtension(%q) = %q, want %q", tt.mediaType, got, tt.want)
		}
	}
}
