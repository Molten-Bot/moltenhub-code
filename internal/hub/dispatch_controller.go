package hub

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errDispatchControllerClosed = errors.New("dispatch controller is closed")

// AdaptiveDispatchController controls dispatch admission with rolling system load samples.
// It never preempts running work; when pressure rises, newer tasks stay queued.
type AdaptiveDispatchController struct {
	cfg    DispatcherConfig
	logf   func(string, ...any)
	sample resourceSampler

	mu      sync.Mutex
	waiters []*dispatchWaiter
	running int
	allowed int
	window  []resourceSample
	closed  bool

	startOnce sync.Once
	stopCh    chan struct{}
	nextID    atomic.Uint64
}

type dispatchWaiter struct {
	id        uint64
	requestID string
	granted   bool
	notify    chan struct{}
}

type resourceSample struct {
	CPUPercent    float64
	MemoryPercent float64
	DiskIOMBs     float64
}

type resourceSampler interface {
	Sample() (resourceSample, error)
}

type defaultResourceSampler struct {
	lastCPUTotal uint64
	lastCPUIdle  uint64
	lastDiskIO   uint64
	lastDiskTS   time.Time
	haveCPU      bool
	haveDisk     bool
}

// NewAdaptiveDispatchController returns a queue-aware, adaptive dispatcher.
func NewAdaptiveDispatchController(cfg DispatcherConfig, logf func(string, ...any)) *AdaptiveDispatchController {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cfg = normalizeDispatcherConfig(cfg)
	return &AdaptiveDispatchController{
		cfg:     cfg,
		logf:    logf,
		sample:  &defaultResourceSampler{},
		allowed: cfg.MaxParallel,
		stopCh:  make(chan struct{}),
	}
}

// Start begins background sampling. Safe to call multiple times.
func (c *AdaptiveDispatchController) Start(ctx context.Context) {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		interval := time.Duration(c.cfg.SampleIntervalMS) * time.Millisecond
		if interval < 250*time.Millisecond {
			interval = 250 * time.Millisecond
		}

		go func() {
			c.sampleAndUpdate()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					c.close()
					return
				case <-ticker.C:
					c.sampleAndUpdate()
				}
			}
		}()
	})
}

// Acquire blocks until the caller is admitted to run. The returned function must be called once when done.
func (c *AdaptiveDispatchController) Acquire(ctx context.Context, requestID string) (func(), error) {
	if c == nil {
		return func() {}, nil
	}

	requestID = strings.TrimSpace(requestID)
	waiter := &dispatchWaiter{
		id:        c.nextID.Add(1),
		requestID: requestID,
		notify:    make(chan struct{}, 1),
	}

	var (
		queued     bool
		queueDepth int
		running    int
		allowed    int
		granted    bool
	)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errDispatchControllerClosed
	}
	c.waiters = append(c.waiters, waiter)
	c.tryGrantLocked()
	granted = waiter.granted
	queued = !granted
	if queued {
		queueDepth = len(c.waiters)
		running = c.running
		allowed = c.allowed
	}
	c.mu.Unlock()

	if queued {
		c.logf(
			"dispatch status=queued request_id=%s queue_depth=%d running=%d allowed=%d",
			firstNonEmpty(waiter.requestID, fmt.Sprintf("queued-%d", waiter.id)),
			queueDepth,
			running,
			allowed,
		)
	}

	if granted {
		return c.releaseFunc(), nil
	}

	for {
		select {
		case <-waiter.notify:
			c.mu.Lock()
			granted := waiter.granted
			c.mu.Unlock()
			if granted {
				return c.releaseFunc(), nil
			}
		case <-ctx.Done():
			c.mu.Lock()
			if waiter.granted {
				if c.running > 0 {
					c.running--
				}
				c.tryGrantLocked()
				c.mu.Unlock()
				return nil, ctx.Err()
			}
			c.removeWaiterLocked(waiter.id)
			c.tryGrantLocked()
			c.mu.Unlock()
			return nil, ctx.Err()
		case <-c.stopCh:
			c.mu.Lock()
			if waiter.granted {
				if c.running > 0 {
					c.running--
				}
				c.tryGrantLocked()
				c.mu.Unlock()
				return nil, errDispatchControllerClosed
			}
			c.removeWaiterLocked(waiter.id)
			c.mu.Unlock()
			return nil, errDispatchControllerClosed
		}
	}
}

func (c *AdaptiveDispatchController) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.running > 0 {
				c.running--
			}
			c.tryGrantLocked()
			c.mu.Unlock()
		})
	}
}

func (c *AdaptiveDispatchController) tryGrantLocked() {
	for c.running < c.allowed && len(c.waiters) > 0 {
		waiter := c.waiters[0]
		c.waiters = c.waiters[1:]
		waiter.granted = true
		c.running++
		select {
		case waiter.notify <- struct{}{}:
		default:
		}
	}
}

func (c *AdaptiveDispatchController) removeWaiterLocked(id uint64) {
	for i, waiter := range c.waiters {
		if waiter.id != id {
			continue
		}
		c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
		return
	}
}

func (c *AdaptiveDispatchController) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	waiters := append([]*dispatchWaiter(nil), c.waiters...)
	c.waiters = nil
	c.mu.Unlock()

	for _, waiter := range waiters {
		select {
		case waiter.notify <- struct{}{}:
		default:
		}
	}
	close(c.stopCh)
}

func (c *AdaptiveDispatchController) sampleAndUpdate() {
	if c == nil || c.sample == nil {
		return
	}

	sample, err := c.sample.Sample()
	if err != nil {
		c.logf("dispatcher status=sample_error err=%q", err)
		return
	}

	c.mu.Lock()
	windowSize := c.cfg.SampleWindow
	if windowSize <= 0 {
		windowSize = 1
	}
	if len(c.window) < windowSize {
		c.window = append(c.window, sample)
	} else {
		copy(c.window, c.window[1:])
		c.window[len(c.window)-1] = sample
	}
	avg := averageResourceSample(c.window)

	prevAllowed := c.allowed
	nextAllowed := computeAllowedParallel(c.cfg, avg)
	if nextAllowed < c.cfg.MinParallel {
		nextAllowed = c.cfg.MinParallel
	}
	if nextAllowed > c.cfg.MaxParallel {
		nextAllowed = c.cfg.MaxParallel
	}
	c.allowed = nextAllowed
	c.tryGrantLocked()
	queueDepth := len(c.waiters)
	running := c.running
	c.mu.Unlock()

	if prevAllowed != nextAllowed {
		c.logf(
			"dispatcher status=window cpu=%.1f memory=%.1f disk_io_mb_s=%.1f allowed=%d max=%d running=%d queue_depth=%d",
			avg.CPUPercent,
			avg.MemoryPercent,
			avg.DiskIOMBs,
			nextAllowed,
			c.cfg.MaxParallel,
			running,
			queueDepth,
		)
	}
}

func averageResourceSample(values []resourceSample) resourceSample {
	if len(values) == 0 {
		return resourceSample{}
	}

	var (
		cpuSum, memSum, diskSum       float64
		cpuCount, memCount, diskCount int
	)
	for _, value := range values {
		if value.CPUPercent > 0 {
			cpuSum += value.CPUPercent
			cpuCount++
		}
		if value.MemoryPercent > 0 {
			memSum += value.MemoryPercent
			memCount++
		}
		if value.DiskIOMBs > 0 {
			diskSum += value.DiskIOMBs
			diskCount++
		}
	}

	var out resourceSample
	if cpuCount > 0 {
		out.CPUPercent = cpuSum / float64(cpuCount)
	}
	if memCount > 0 {
		out.MemoryPercent = memSum / float64(memCount)
	}
	if diskCount > 0 {
		out.DiskIOMBs = diskSum / float64(diskCount)
	}
	return out
}

func computeAllowedParallel(cfg DispatcherConfig, avg resourceSample) int {
	maxParallel := cfg.MaxParallel
	if maxParallel < 1 {
		maxParallel = 1
	}

	pressure := 0.0
	if avg.CPUPercent > 0 && cfg.CPUHighWatermark > 0 {
		pressure = maxFloat(pressure, avg.CPUPercent/cfg.CPUHighWatermark)
	}
	if avg.MemoryPercent > 0 && cfg.MemoryHighWatermark > 0 {
		pressure = maxFloat(pressure, avg.MemoryPercent/cfg.MemoryHighWatermark)
	}
	if avg.DiskIOMBs > 0 && cfg.DiskIOHighWatermarkMBs > 0 {
		pressure = maxFloat(pressure, avg.DiskIOMBs/cfg.DiskIOHighWatermarkMBs)
	}

	if pressure <= 1 || pressure == 0 {
		return maxParallel
	}
	allowed := int(float64(maxParallel) / pressure)
	if allowed < cfg.MinParallel {
		return cfg.MinParallel
	}
	if allowed > maxParallel {
		return maxParallel
	}
	return allowed
}

func maxFloat(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func normalizeDispatcherConfig(cfg DispatcherConfig) DispatcherConfig {
	normalized := InitConfig{
		Dispatcher: cfg,
	}
	normalized.ApplyDefaults()
	return normalized.Dispatcher
}

func (s *defaultResourceSampler) Sample() (resourceSample, error) {
	switch runtime.GOOS {
	case "windows":
		return s.sampleWindows()
	case "linux":
		return s.sampleLinux()
	default:
		return resourceSample{}, nil
	}
}

func (s *defaultResourceSampler) sampleLinux() (resourceSample, error) {
	cpuTotal, cpuIdle, err := readLinuxCPUStat("/proc/stat")
	if err != nil {
		return resourceSample{}, err
	}
	memPercent, err := readLinuxMemoryUsagePercent("/proc/meminfo")
	if err != nil {
		return resourceSample{}, err
	}
	diskTotalBytes, err := readLinuxDiskIOBytes("/proc/diskstats")
	if err != nil {
		return resourceSample{}, err
	}

	sample := resourceSample{
		MemoryPercent: memPercent,
	}

	if s.haveCPU {
		totalDelta := cpuTotal - s.lastCPUTotal
		idleDelta := cpuIdle - s.lastCPUIdle
		if totalDelta > 0 {
			busy := float64(totalDelta-idleDelta) / float64(totalDelta)
			sample.CPUPercent = busy * 100
		}
	}
	s.lastCPUTotal = cpuTotal
	s.lastCPUIdle = cpuIdle
	s.haveCPU = true

	now := time.Now()
	if s.haveDisk {
		if elapsed := now.Sub(s.lastDiskTS).Seconds(); elapsed > 0 {
			bytesDelta := diskTotalBytes - s.lastDiskIO
			sample.DiskIOMBs = float64(bytesDelta) / elapsed / (1024 * 1024)
		}
	}
	s.lastDiskIO = diskTotalBytes
	s.lastDiskTS = now
	s.haveDisk = true

	return sample, nil
}

func (s *defaultResourceSampler) sampleWindows() (resourceSample, error) {
	// Sample three counters in one call to keep a consistent timestamp.
	cmd := exec.Command(
		"typeperf",
		"-sc", "1",
		`\\Processor(_Total)\\% Processor Time`,
		`\\Memory\\% Committed Bytes In Use`,
		`\\PhysicalDisk(_Total)\\Disk Bytes/sec`,
	)
	out, err := cmd.Output()
	if err != nil {
		return resourceSample{}, fmt.Errorf("sample windows resources: %w", err)
	}

	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		return resourceSample{}, fmt.Errorf("parse windows resource sample: %w", err)
	}
	if len(rows) < 2 {
		return resourceSample{}, fmt.Errorf("parse windows resource sample: missing data row")
	}

	data := rows[len(rows)-1]
	if len(data) < 4 {
		return resourceSample{}, fmt.Errorf("parse windows resource sample: expected 4 columns, got %d", len(data))
	}

	cpuPercent, err := parseFlexibleFloat(data[1])
	if err != nil {
		return resourceSample{}, fmt.Errorf("parse cpu sample: %w", err)
	}
	memPercent, err := parseFlexibleFloat(data[2])
	if err != nil {
		return resourceSample{}, fmt.Errorf("parse memory sample: %w", err)
	}
	diskBytesPerSec, err := parseFlexibleFloat(data[3])
	if err != nil {
		return resourceSample{}, fmt.Errorf("parse disk sample: %w", err)
	}

	return resourceSample{
		CPUPercent:    clampFloat(cpuPercent, 0, 100),
		MemoryPercent: clampFloat(memPercent, 0, 100),
		DiskIOMBs:     maxFloat(0, diskBytesPerSec/(1024*1024)),
	}, nil
}

func parseFlexibleFloat(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "\"")
	if raw == "" {
		return 0, fmt.Errorf("empty float value")
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err == nil {
		return parsed, nil
	}
	if strings.Contains(raw, ",") && !strings.Contains(raw, ".") {
		raw = strings.ReplaceAll(raw, ",", ".")
		return strconv.ParseFloat(raw, 64)
	}
	return 0, err
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func readLinuxCPUStat(path string) (uint64, uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("parse %s: malformed cpu line", path)
		}
		var (
			total uint64
			idle  uint64
		)
		for i := 1; i < len(fields); i++ {
			v, parseErr := strconv.ParseUint(fields[i], 10, 64)
			if parseErr != nil {
				return 0, 0, fmt.Errorf("parse %s cpu field %q: %w", path, fields[i], parseErr)
			}
			total += v
			if i == 4 || i == 5 {
				idle += v
			}
		}
		return total, idle, nil
	}
	return 0, 0, fmt.Errorf("parse %s: missing cpu aggregate line", path)
}

func readLinuxMemoryUsagePercent(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	var (
		totalKB uint64
		availKB uint64
	)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			totalKB, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable":
			availKB, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if totalKB == 0 {
		return 0, fmt.Errorf("parse %s: missing MemTotal", path)
	}
	if availKB > totalKB {
		availKB = totalKB
	}
	usedRatio := float64(totalKB-availKB) / float64(totalKB)
	return usedRatio * 100, nil
}

func readLinuxDiskIOBytes(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	var totalSectors uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		name := fields[2]
		if !includeLinuxDiskDevice(name) {
			continue
		}

		readSectors, readErr := strconv.ParseUint(fields[5], 10, 64)
		writeSectors, writeErr := strconv.ParseUint(fields[9], 10, 64)
		if readErr != nil || writeErr != nil {
			continue
		}
		totalSectors += readSectors + writeSectors
	}

	return totalSectors * 512, nil
}

var (
	nvmeWholeDiskRE = regexp.MustCompile(`^nvme\d+n\d+$`)
	mmcWholeDiskRE  = regexp.MustCompile(`^mmcblk\d+$`)
	sdWholeDiskRE   = regexp.MustCompile(`^(sd|hd|vd|xvd)[a-z]+$`)
)

func includeLinuxDiskDevice(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "fd") || strings.HasPrefix(name, "sr") {
		return false
	}
	if strings.HasPrefix(name, "dm-") {
		return true
	}
	if sdWholeDiskRE.MatchString(name) {
		return true
	}
	if nvmeWholeDiskRE.MatchString(name) {
		return true
	}
	if mmcWholeDiskRE.MatchString(name) {
		return true
	}
	return false
}
