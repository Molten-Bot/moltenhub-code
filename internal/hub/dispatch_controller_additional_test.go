package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFlexibleFloatAndClampFloat(t *testing.T) {
	t.Parallel()

	if got, err := parseFlexibleFloat(`"12.5"`); err != nil || got != 12.5 {
		t.Fatalf("parseFlexibleFloat(quoted dot) = (%v, %v), want (12.5, nil)", got, err)
	}
	if got, err := parseFlexibleFloat("12,5"); err != nil || got != 12.5 {
		t.Fatalf("parseFlexibleFloat(comma decimal) = (%v, %v), want (12.5, nil)", got, err)
	}
	if _, err := parseFlexibleFloat(" "); err == nil {
		t.Fatal("parseFlexibleFloat(empty) error = nil, want non-nil")
	}

	if got := clampFloat(-3, 0, 10); got != 0 {
		t.Fatalf("clampFloat(low) = %v, want 0", got)
	}
	if got := clampFloat(13, 0, 10); got != 10 {
		t.Fatalf("clampFloat(high) = %v, want 10", got)
	}
	if got := clampFloat(7, 0, 10); got != 7 {
		t.Fatalf("clampFloat(in-range) = %v, want 7", got)
	}
}

func TestIncludeLinuxDiskDeviceAdditionalCases(t *testing.T) {
	t.Parallel()

	if includeLinuxDiskDevice("loop0") {
		t.Fatal("includeLinuxDiskDevice(loop0) = true, want false")
	}
	if !includeLinuxDiskDevice("dm-0") {
		t.Fatal("includeLinuxDiskDevice(dm-0) = false, want true")
	}
	if !includeLinuxDiskDevice("nvme0n1") {
		t.Fatal("includeLinuxDiskDevice(nvme0n1) = false, want true")
	}
	if includeLinuxDiskDevice("nvme0n1p1") {
		t.Fatal("includeLinuxDiskDevice(nvme0n1p1) = true, want false")
	}
}

func TestShouldDisableLinuxDiskSampling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		containerEnv    string
		markerFile      bool
		cgroupSnapshot  string
		wantDisableDisk bool
	}{
		{
			name:            "enabled on host",
			containerEnv:    "",
			markerFile:      false,
			cgroupSnapshot:  "0::/\n",
			wantDisableDisk: false,
		},
		{
			name:            "disabled by container env",
			containerEnv:    "docker",
			markerFile:      false,
			cgroupSnapshot:  "0::/\n",
			wantDisableDisk: true,
		},
		{
			name:            "disabled by marker file",
			containerEnv:    "",
			markerFile:      true,
			cgroupSnapshot:  "0::/\n",
			wantDisableDisk: true,
		},
		{
			name:            "disabled by cgroup marker",
			containerEnv:    "",
			markerFile:      false,
			cgroupSnapshot:  "1:name=systemd:/docker/abcdef\n",
			wantDisableDisk: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldDisableLinuxDiskSampling(tc.containerEnv, tc.markerFile, tc.cgroupSnapshot)
			if got != tc.wantDisableDisk {
				t.Fatalf(
					"shouldDisableLinuxDiskSampling(%q, %v, %q) = %v, want %v",
					tc.containerEnv,
					tc.markerFile,
					tc.cgroupSnapshot,
					got,
					tc.wantDisableDisk,
				)
			}
		})
	}
}

func TestShouldDisableLinuxDiskSamplingForZeroTotals(t *testing.T) {
	t.Parallel()

	if shouldDisableLinuxDiskSamplingForZeroTotals(1024, 99) {
		t.Fatal("shouldDisableLinuxDiskSamplingForZeroTotals(non-zero totals) = true, want false")
	}
	if shouldDisableLinuxDiskSamplingForZeroTotals(0, 0) {
		t.Fatal("shouldDisableLinuxDiskSamplingForZeroTotals(first zero total) = true, want false")
	}
	if !shouldDisableLinuxDiskSamplingForZeroTotals(0, 1) {
		t.Fatal("shouldDisableLinuxDiskSamplingForZeroTotals(repeated zero totals) = false, want true")
	}
}

func TestContainsContainerCGroupMarker(t *testing.T) {
	t.Parallel()

	for _, snapshot := range []string{
		"1:name=systemd:/docker/abcdef\n",
		"1:name=systemd:/kubepods.slice/pod-123\n",
		"1:name=systemd:/containerd/io.containerd.runtime.v2.task\n",
		"1:name=systemd:/libpod-123.scope\n",
	} {
		if !containsContainerCGroupMarker(snapshot) {
			t.Fatalf("containsContainerCGroupMarker(%q) = false, want true", snapshot)
		}
	}
	if containsContainerCGroupMarker("0::/\n") {
		t.Fatal("containsContainerCGroupMarker(host cgroup) = true, want false")
	}
}

func TestSampleWindowsParsesTypeperfOutput(t *testing.T) {
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "typeperf")
	script := `#!/bin/sh
echo '"(PDH-CSV 4.0)","\\Processor(_Total)\\% Processor Time","\\Memory\\% Committed Bytes In Use","\\PhysicalDisk(_Total)\\Disk Bytes/sec"'
echo '"04/07/2026 09:10:00.000","12,5","40.0","1048576"'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(typeperf) error = %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+origPath)

	sampler := &defaultResourceSampler{}
	got, err := sampler.sampleWindows()
	if err != nil {
		t.Fatalf("sampleWindows() error = %v", err)
	}
	if got.CPUPercent != 12.5 {
		t.Fatalf("CPUPercent = %v, want 12.5", got.CPUPercent)
	}
	if got.MemoryPercent != 40 {
		t.Fatalf("MemoryPercent = %v, want 40", got.MemoryPercent)
	}
	if got.DiskIOMBs != 1 {
		t.Fatalf("DiskIOMBs = %v, want 1", got.DiskIOMBs)
	}
}

func TestSampleWindowsErrorsOnMissingData(t *testing.T) {
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "typeperf")
	script := `#!/bin/sh
echo '"(PDH-CSV 4.0)","\\Processor(_Total)\\% Processor Time"'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(typeperf) error = %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+origPath)

	sampler := &defaultResourceSampler{}
	if _, err := sampler.sampleWindows(); err == nil {
		t.Fatal("sampleWindows() error = nil, want non-nil")
	}
}
