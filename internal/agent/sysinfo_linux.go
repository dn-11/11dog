//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

type cpuSample struct {
	idle  uint64
	total uint64
}

var (
	sysMu      sync.Mutex
	prevSample *cpuSample
)

func readSysMetrics() (cpuPercent float64, memPercent float64) {
	cpuPercent = readLinuxCPU()
	memPercent = readLinuxMem()
	return cpuPercent, memPercent
}

func readLinuxCPU() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		val, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			continue
		}
		total += val
		if i == 4 || i == 5 { // idle + iowait
			idle += val
		}
	}

	sysMu.Lock()
	defer sysMu.Unlock()
	cur := &cpuSample{idle: idle, total: total}
	if prevSample == nil || cur.total <= prevSample.total {
		prevSample = cur
		return 0
	}
	dTotal := float64(cur.total - prevSample.total)
	dIdle := float64(cur.idle - prevSample.idle)
	prevSample = cur
	if dTotal <= 0 {
		return 0
	}
	usage := (1.0 - dIdle/dTotal) * 100.0
	if usage < 0 {
		return 0
	}
	return float64(int(usage*10)) / 10
}

func readLinuxMem() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var totalKB, availKB uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			availKB, _ = strconv.ParseUint(fields[1], 10, 64)
		}
		if totalKB > 0 && availKB > 0 {
			break
		}
	}
	if totalKB == 0 {
		return 0
	}
	usedPct := float64(totalKB-availKB) / float64(totalKB) * 100.0
	return float64(int(usedPct*10)) / 10
}
