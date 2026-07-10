//go:build linux
// +build linux

package admin

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── System CPU ──

var (
	linuxCPUCacheMu sync.Mutex
	linuxCPUCache   struct {
		idle  uint64
		total uint64
		time  time.Time
	}
	linuxFirstCPU sync.Once
)

// getSystemCPUUsage returns the recent system-wide CPU usage (%) by measuring
// the delta between consecutive reads of /proc/stat.
func getSystemCPUUsage() float64 {
	idle, total, err := readCPUStat()
	if err != nil {
		return 0
	}

	now := time.Now()
	linuxCPUCacheMu.Lock()
	defer linuxCPUCacheMu.Unlock()

	// First call: cache and return average since boot
	linuxFirstCPU.Do(func() {
		linuxCPUCache.idle = idle
		linuxCPUCache.total = total
		linuxCPUCache.time = now
	})

	dIdle := idle - linuxCPUCache.idle
	dTotal := total - linuxCPUCache.total
	elapsed := now.Sub(linuxCPUCache.time).Nanoseconds()

	// Update cache
	linuxCPUCache.idle = idle
	linuxCPUCache.total = total
	linuxCPUCache.time = now

	if elapsed < 500_000_000 || dTotal <= 0 {
		if total <= 0 {
			return 0
		}
		return float64(total-idle) * 100 / float64(total)
	}

	pct := float64(dTotal-dIdle) * 100 / float64(dTotal)
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// readCPUStat parses the first "cpu" line from /proc/stat.
// Returns (idle, total) in USER_HZ ticks.
func readCPUStat() (idle, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0, 0, fmt.Errorf("empty /proc/stat")
	}
	line := scanner.Text()
	if !strings.HasPrefix(line, "cpu ") {
		return 0, 0, fmt.Errorf("unexpected /proc/stat format")
	}

	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, 0, fmt.Errorf("too few fields in /proc/stat cpu line")
	}

	// Fields: user nice system idle iowait irq softirq steal
	// idle is field 4 (index 4 from Fields which includes "cpu" at index 0)
	for i := 1; i < len(fields); i++ {
		val, _ := strconv.ParseUint(fields[i], 10, 64)
		total += val
	}
	idle, _ = strconv.ParseUint(fields[4], 10, 64)
	return idle, total, nil
}

// ── Process CPU ──

// getProcessCPUUsage returns the average CPU usage (%) since process start.
// Reads /proc/self/stat for utime+stime and divides by wall clock time.
func getProcessCPUUsage() float64 {
	f, err := os.Open("/proc/self/stat")
	if err != nil {
		return 0
	}
	defer f.Close()

	var comm string
	fmt.Fscanf(f, "%d (%s", new(int), &comm)
	// comm ends with ')', read the rest
	comm = strings.TrimRight(comm, ")")
	// Re-read properly: skip pid and comm, read the rest
	// Actually let me do this more carefully
	_ = comm

	// Reset and read all content
	f.Seek(0, 0)
	data := make([]byte, 1024)
	n, _ := f.Read(data)
	content := string(data[:n])

	// Find the closing paren of comm field
	idx := strings.LastIndex(content, ")")
	if idx < 0 {
		return 0
	}
	rest := content[idx+2:] // skip ") "

	fields := strings.Fields(rest)
	if len(fields) < 13 { // utime is field 11 (0-indexed after comm), stime is field 12
		return 0
	}

	utime, _ := strconv.ParseUint(fields[11], 10, 64) // field 14 in /proc/self/stat (0-indexed from rest start)
	stime, _ := strconv.ParseUint(fields[12], 10, 64) // field 15

	// Actually let me be precise: after the pid and comm, the fields are:
	// 0: state, 1: ppid, 2: pgrp, 3: session, 4: tty_nr, 5: tpgid,
	// 6: flags, 7: minflt, 8: cminflt, 9: majflt, 10: cmajflt,
	// 11: utime, 12: stime, ...
	// So fields[11] = utime, fields[12] = stime

	cpuTicks := utime + stime
	// Standard USER_HZ is 100 on most Linux systems
	const userHZ = 100
	cpuSecs := float64(cpuTicks) / userHZ

	// Process start time from /proc/self/stat field 21 (starttime)
	// But easier: use boot time + starttime
	// Actually, the simplest is to read /proc/uptime and compute process age from starttime
	// Let me just use a simpler approach:

	// Read process start time from /proc/self/stat field 21 (starttime)
	if len(fields) < 21 {
		// Fallback: use time since boot
		return 0
	}
	startTicks, _ := strconv.ParseUint(fields[19], 10, 64) // field 21 (0-indexed from rest = 19)

	// Read system uptime from /proc/uptime
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	uptimeStr := strings.Fields(string(uptimeData))
	if len(uptimeStr) < 1 {
		return 0
	}
	uptimeSecs, _ := strconv.ParseFloat(uptimeStr[0], 64)

	startTimeSecs := float64(startTicks) / userHZ
	processAge := uptimeSecs - startTimeSecs
	if processAge <= 0 {
		return 0
	}

	pct := cpuSecs * 100 / processAge
	if pct > 100 {
		pct = 100
	}
	if math.IsNaN(pct) || math.IsInf(pct, 0) {
		return 0
	}
	return pct
}

// ── System Memory ──

// getSystemMemoryGB returns (usedGB, totalGB) of physical RAM by reading /proc/meminfo.
func getSystemMemoryGB() (usedGB, totalGB float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}

	var memTotal, memAvailable uint64
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		case strings.HasPrefix(line, "MemAvailable:"):
			fmt.Sscanf(line, "MemAvailable: %d kB", &memAvailable)
		}
		if memTotal > 0 && memAvailable > 0 {
			break
		}
	}

	totalGB = float64(memTotal) / 1024 / 1024
	usedGB = float64(memTotal-memAvailable) / 1024 / 1024
	return usedGB, totalGB
}

// ── Disk Free ──

// getDiskFreeGB returns (freeGB, totalGB) for the given path using statfs.
func getDiskFreeGB(path string) (freeGB, totalGB float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	totalGB = float64(stat.Blocks*uint64(stat.Bsize)) / 1024 / 1024 / 1024
	freeGB = float64(stat.Bfree*uint64(stat.Bsize)) / 1024 / 1024 / 1024
	return freeGB, totalGB
}

// getAllDiskInfo returns usage info for physical mount points on Linux.
func getAllDiskInfo() []diskInfo {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []diskInfo
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mp := fields[1]
		fs := fields[2]
		// Skip pseudo / virtual filesystems
		switch fs {
		case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts",
			"cgroup", "cgroup2", "pstore", "securityfs", "selinuxfs",
			"autofs", "mqueue", "hugetlbfs", "configfs", "debugfs",
			"tracefs", "ramfs", "overlay", "efivarfs", "bpf":
			continue
		}
		// Skip common pseudo paths
		if strings.HasPrefix(mp, "/sys") || strings.HasPrefix(mp, "/proc") ||
			strings.HasPrefix(mp, "/dev") || strings.HasPrefix(mp, "/snap") {
			continue
		}
		if seen[mp] {
			continue
		}
		seen[mp] = true
		free, total := getDiskFreeGB(mp)
		if total > 0 {
			result = append(result, diskInfo{
				MountPoint: mp,
				UsedGB:     total - free,
				TotalGB:    total,
				Percent:    (total - free) / total * 100,
			})
		}
	}
	return result
}

// getNetworkIO returns total bytes received and sent across all non-loopback interfaces.
func getNetworkIO() (rxBytes, txBytes uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		iface := strings.TrimRight(fields[0], ":")
		// Skip loopback
		if iface == "lo" {
			continue
		}
		rx, _ := strconv.ParseUint(fields[1], 10, 64)
		tx, _ := strconv.ParseUint(fields[9], 10, 64)
		rxBytes += rx
		txBytes += tx
	}
	return rxBytes, txBytes
}
