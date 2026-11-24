//go:build !linux

package agent

import "runtime"

func readSysMetrics() (cpuPercent float64, memPercent float64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.Sys == 0 {
		return 0, 0
	}
	pct := float64(ms.Alloc) / float64(ms.Sys) * 100.0
	return 0, float64(int(pct*10)) / 10
}
