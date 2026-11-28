package remote

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

// ServerStats holds lightweight remote host metrics sampled over an SSH exec channel.
type ServerStats struct {
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	Kernel      string  `json:"kernel"`
	UptimeSec   int64   `json:"uptimeSec"`
	Load1       string  `json:"load1"`
	Load5       string  `json:"load5"`
	Load15      string  `json:"load15"`
	Processes   int     `json:"processes,omitempty"`
	CPUCores    int     `json:"cpuCores"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemTotalMB  int64   `json:"memTotalMB"`
	MemUsedMB   int64   `json:"memUsedMB"`
	SwapTotalMB int64   `json:"swapTotalMB,omitempty"`
	SwapUsedMB  int64   `json:"swapUsedMB,omitempty"`
	DiskTotalGB float64 `json:"diskTotalGB"`
	DiskUsedGB  float64 `json:"diskUsedGB"`
	DiskPercent int     `json:"diskPercent"`
	NetRxBytes  int64   `json:"netRxBytes"`
	NetTxBytes  int64   `json:"netTxBytes"`
	LatencyMs   int64   `json:"latencyMs"`
	CollectedAt int64   `json:"collectedAt"`
}

// telemetryStaticAndDynamic runs once on the first sample (zero sleep!).
const telemetryFullScript = `sh -c '
echo "H:$(hostname 2>/dev/null || uname -n)"
echo "K:$(uname -sr 2>/dev/null)"
if [ -r /etc/os-release ]; then
  echo "O:$(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-$NAME}")"
else
  echo "O:$(uname -s 2>/dev/null)"
fi
echo "C:$(nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo 2>/dev/null || echo 1)"
if [ -r /proc/uptime ]; then read u _ < /proc/uptime; echo "U:${u%%.*}"; fi
if [ -r /proc/loadavg ]; then read l1 l2 l3 pr _ < /proc/loadavg; echo "L:$l1 $l2 $l3 $pr"; fi
if [ -r /proc/stat ]; then read _ a b c d e f g _ < /proc/stat; echo "P:$a $b $c $d $e $f $g"; fi
if [ -r /proc/meminfo ]; then awk "/^MemTotal:/{t=\$2} /^MemAvailable:/{a=\$2} /^MemFree:/{f=\$2} /^Buffers:/{b=\$2} /^Cached:/{c=\$2} /^SwapTotal:/{st=\$2} /^SwapFree:/{sf=\$2} END{if(a==0)a=f+b+c; print \"M:\"int(t/1024),int((t-a)/1024); if(st>0) print \"S:\"int(st/1024),int((st-sf)/1024)}" /proc/meminfo 2>/dev/null; fi
df -Pk / 2>/dev/null | awk "NR==2{print \"D:\"\$2,\$3,\$5}"
if [ -r /proc/net/dev ]; then awk "NR>2 && \$1!~/^lo:/{rx+=\$2; tx+=\$10} END{printf \"N:%.0f %.0f\n\", rx, tx}" /proc/net/dev 2>/dev/null; fi
'`

// telemetryFastScript runs on subsequent ticks: zero sleep, no hostname/uname/os-release forks.
const telemetryFastScript = `sh -c '
if [ -r /proc/uptime ]; then read u _ < /proc/uptime; echo "U:${u%%.*}"; fi
if [ -r /proc/loadavg ]; then read l1 l2 l3 pr _ < /proc/loadavg; echo "L:$l1 $l2 $l3 $pr"; fi
if [ -r /proc/stat ]; then read _ a b c d e f g _ < /proc/stat; echo "P:$a $b $c $d $e $f $g"; fi
if [ -r /proc/meminfo ]; then awk "/^MemTotal:/{t=\$2} /^MemAvailable:/{a=\$2} /^MemFree:/{f=\$2} /^Buffers:/{b=\$2} /^Cached:/{c=\$2} /^SwapTotal:/{st=\$2} /^SwapFree:/{sf=\$2} END{if(a==0)a=f+b+c; print \"M:\"int(t/1024),int((t-a)/1024); if(st>0) print \"S:\"int(st/1024),int((st-sf)/1024)}" /proc/meminfo 2>/dev/null; fi
df -Pk / 2>/dev/null | awk "NR==2{print \"D:\"\$2,\$3,\$5}"
if [ -r /proc/net/dev ]; then awk "NR>2 && \$1!~/^lo:/{rx+=\$2; tx+=\$10} END{printf \"N:%.0f %.0f\n\", rx, tx}" /proc/net/dev 2>/dev/null; fi
'`

// FetchStats samples live Linux/POSIX telemetry with zero remote sleep and static metadata caching.
func (s *Session) FetchStats(ctx context.Context) (ServerStats, error) {
	s.statsMu.Lock()
	hasStatic := s.staticHost != "" || s.staticOS != ""
	s.statsMu.Unlock()

	script := telemetryFullScript
	if hasStatic {
		script = telemetryFastScript
	}

	execCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	t0 := time.Now()
	out, err := s.Exec(execCtx, script, 16<<10)
	rtt := time.Since(t0).Milliseconds()
	if err != nil && strings.TrimSpace(out) == "" {
		return ServerStats{}, err
	}

	stats, snap, hasSnap := parseTelemetryWithCPU(out)

	s.statsMu.Lock()
	if stats.Hostname != "" {
		s.staticHost = stats.Hostname
	} else {
		stats.Hostname = s.staticHost
	}
	if stats.OS != "" {
		s.staticOS = stats.OS
	} else {
		stats.OS = s.staticOS
	}
	if stats.Kernel != "" {
		s.staticKernel = stats.Kernel
	} else {
		stats.Kernel = s.staticKernel
	}
	if stats.CPUCores > 0 {
		s.staticCores = stats.CPUCores
	} else {
		stats.CPUCores = s.staticCores
	}
	if hasSnap {
		if s.hasPrevCPU {
			stats.CPUPercent = calcCPUPercent(s.prevCPU, snap)
		}
		s.prevCPU = snap
		s.hasPrevCPU = true
	}
	s.statsMu.Unlock()

	if s.LatencyMs() > 0 {
		stats.LatencyMs = s.LatencyMs()
	} else {
		stats.LatencyMs = rtt
	}
	stats.CollectedAt = time.Now().Unix()
	return stats, nil
}

func calcCPUPercent(a, b [7]float64) float64 {
	idle1 := a[3] + a[4]
	idle2 := b[3] + b[4]
	tot1 := a[0] + a[1] + a[2] + a[3] + a[4] + a[5] + a[6]
	tot2 := b[0] + b[1] + b[2] + b[3] + b[4] + b[5] + b[6]
	dt := tot2 - tot1
	di := idle2 - idle1
	if dt <= 0 {
		return 0
	}
	pct := (1 - di/dt) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return math.Round(pct*10) / 10
}

func parseTelemetry(raw string) ServerStats {
	st, _, _ := parseTelemetryWithCPU(raw)
	return st
}

func parseTelemetryWithCPU(raw string) (ServerStats, [7]float64, bool) {
	var st ServerStats
	var snap [7]float64
	var hasSnap bool
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		tag, val := line[0], strings.TrimSpace(line[2:])
		switch tag {
		case 'H':
			st.Hostname = val
		case 'K':
			st.Kernel = val
		case 'O':
			st.OS = val
		case 'U':
			st.UptimeSec, _ = strconv.ParseInt(val, 10, 64)
		case 'C':
			st.CPUCores, _ = strconv.Atoi(val)
		case 'L':
			parts := strings.Fields(val)
			if len(parts) >= 3 {
				st.Load1, st.Load5, st.Load15 = parts[0], parts[1], parts[2]
			}
			if len(parts) >= 4 {
				procStr := parts[3]
				if idx := strings.LastIndexByte(procStr, '/'); idx >= 0 {
					procStr = procStr[idx+1:]
				}
				if p, err := strconv.Atoi(procStr); err == nil && p > 0 {
					st.Processes = p
				}
			}
		case 'P':
			parts := strings.Fields(val)
			if len(parts) >= 14 {
				var a, b [7]float64
				for i := 0; i < 7; i++ {
					a[i], _ = strconv.ParseFloat(parts[i], 64)
					b[i], _ = strconv.ParseFloat(parts[i+7], 64)
				}
				st.CPUPercent = calcCPUPercent(a, b)
				snap = b
				hasSnap = true
			} else if len(parts) >= 7 {
				for i := 0; i < 7; i++ {
					snap[i], _ = strconv.ParseFloat(parts[i], 64)
				}
				hasSnap = true
			}
		case 'M':
			parts := strings.Fields(val)
			if len(parts) >= 2 {
				st.MemTotalMB, _ = strconv.ParseInt(parts[0], 10, 64)
				st.MemUsedMB, _ = strconv.ParseInt(parts[1], 10, 64)
				if st.MemUsedMB < 0 {
					st.MemUsedMB = 0
				}
			}
		case 'S':
			parts := strings.Fields(val)
			if len(parts) >= 2 {
				st.SwapTotalMB, _ = strconv.ParseInt(parts[0], 10, 64)
				st.SwapUsedMB, _ = strconv.ParseInt(parts[1], 10, 64)
				if st.SwapTotalMB < 0 {
					st.SwapTotalMB = 0
				}
				if st.SwapUsedMB < 0 {
					st.SwapUsedMB = 0
				}
			}
		case 'D':
			parts := strings.Fields(val)
			if len(parts) >= 3 {
				totKB, _ := strconv.ParseFloat(parts[0], 64)
				usedKB, _ := strconv.ParseFloat(parts[1], 64)
				st.DiskTotalGB = math.Round((totKB/(1024*1024))*10) / 10
				st.DiskUsedGB = math.Round((usedKB/(1024*1024))*10) / 10
				pctStr := strings.TrimSuffix(parts[2], "%")
				st.DiskPercent, _ = strconv.Atoi(pctStr)
			}
		case 'N':
			parts := strings.Fields(val)
			if len(parts) >= 2 {
				st.NetRxBytes, _ = strconv.ParseInt(parts[0], 10, 64)
				st.NetTxBytes, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
	}
	return st, snap, hasSnap
}
