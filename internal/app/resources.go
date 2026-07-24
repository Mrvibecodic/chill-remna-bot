package app

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func resourceStats() string {
	var b strings.Builder
	if used, limit, ok := cgroupMemory(); ok {
		b.WriteString("🧠 Память контейнера: " + humanBytes(used))
		if limit > 0 {
			b.WriteString(fmt.Sprintf(" / %s (%.0f%%)", humanBytes(limit), float64(used)*100/float64(limit)))
		}
		b.WriteByte('\n')
	} else {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		b.WriteString("🧠 Память процесса: " + humanBytes(int64(m.Sys)) + "\n") //#nosec G115 -- memory byte counts are always within int64 range
	}
	if pct, ok := cgroupCPUPercent(200 * time.Millisecond); ok {
		b.WriteString(fmt.Sprintf("⚙️ CPU: %.1f%% от ВМ (%d ядер)\n", pct, runtime.NumCPU()))
	} else {
		b.WriteString(fmt.Sprintf("⚙️ Ядер ВМ: %d\n", runtime.NumCPU()))
	}
	if la, ok := loadAvg(); ok {
		b.WriteString("📈 Load average ВМ: " + la)
	}
	return strings.TrimRight(b.String(), "\n")
}

func readUintFile(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func cgroupMemory() (int64, int64, bool) {
	if used, ok := readUintFile("/sys/fs/cgroup/memory.current"); ok {
		var limit int64
		if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
			if s := strings.TrimSpace(string(data)); s != "max" {
				if l, e := strconv.ParseUint(s, 10, 64); e == nil {
					limit = int64(l) //#nosec G115 -- memory byte counts are always within int64 range
				}
			}
		}
		return int64(used), limit, true //#nosec G115 -- memory byte counts are always within int64 range
	}
	if used, ok := readUintFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); ok {
		var limit int64
		if l, ok := readUintFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok && l < (1<<62) {
			limit = int64(l) //#nosec G115 -- memory byte counts are always within int64 range
		}
		return int64(used), limit, true //#nosec G115 -- memory byte counts are always within int64 range
	}
	return 0, 0, false
}

func cpuUsageUsec() (uint64, bool) {
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "usage_usec ") {
				if n, e := strconv.ParseUint(strings.TrimSpace(line[len("usage_usec "):]), 10, 64); e == nil {
					return n, true
				}
			}
		}
	}
	if n, ok := readUintFile("/sys/fs/cgroup/cpuacct/cpuacct.usage"); ok {
		return n / 1000, true
	}
	return 0, false
}

func cgroupCPUPercent(d time.Duration) (float64, bool) {
	t0, ok := cpuUsageUsec()
	if !ok {
		return 0, false
	}
	start := time.Now()
	time.Sleep(d)
	t1, ok := cpuUsageUsec()
	if !ok {
		return 0, false
	}
	elapsed := float64(time.Since(start).Microseconds())
	cores := float64(runtime.NumCPU())
	if elapsed <= 0 || cores <= 0 {
		return 0, false
	}
	pct := float64(t1-t0) / elapsed / cores * 100
	if pct < 0 {
		pct = 0
	}
	return pct, true
}

func loadAvg() (string, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "", false
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return "", false
	}
	return f[0] + " " + f[1] + " " + f[2], true
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return strconv.FormatInt(n, 10) + " B"
	}
	val := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	i := -1
	for val >= u && i < len(units)-1 {
		val /= u
		i++
	}
	return fmt.Sprintf("%.1f %s", val, units[i])
}
