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
	CPUCores    int     `json:"cpuCores"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemTotalMB  int64   `json:"memTotalMB"`
	MemUsedMB   int64   `json:"memUsedMB"`
	DiskTotalGB float64 `json:"diskTotalGB"`
	DiskUsedGB  float64 `json:"diskUsedGB"`
	DiskPercent int     `json:"diskPercent"`
	NetRxBytes  int64   `json:"netRxBytes"`
	NetTxBytes  int64   `json:"netTxBytes"`
	LatencyMs   int64   `json:"latencyMs"`
	CollectedAt int64   `json:"collectedAt"`
}

const telemetryScript = `sh -c '
echo "H:$(hostname 2>/dev/null || uname -n)"
echo "K:$(uname -sr 2>/dev/null)"
if [ -r /etc/os-release ]; then
  echo "O:$(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-$NAME}")"
else
  echo "O:$(uname -s 2>/dev/null)"
fi
if [ -r /proc/uptime ]; then
  echo "U:$(awk "{print int(\$1)}" /proc/uptime 2>/dev/null)"
fi
if [ -r /proc/loadavg ]; then
  echo "L:$(awk "{print \$1,\$2,\$3}" /proc/loadavg 2>/dev/null)"
fi
echo "C:$(nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo 2>/dev/null || echo 1)"
if [ -r /proc/stat ]; then
  read _ a1 b1 c1 d1 e1 f1 g1 _ < /proc/stat
  sleep 0.25 2>/dev/null || true
  read _ a2 b2 c2 d2 e2 f2 g2 _ < /proc/stat
  echo "P:$a1 $b1 $c1 $d1 $e1 $f1 $g1 $a2 $b2 $c2 $d2 $e2 $f2 $g2"
fi
if [ -r /proc/meminfo ]; then
  echo "M:$(awk "/^MemTotal:/{t=\$2} /^MemAvailable:/{a=\$2} /^MemFree:/{f=\$2} /^Buffers:/{b=\$2} /^Cached:/{c=\$2} END{if(a==0)a=f+b+c; print int(t/1024),int((t-a)/1024)}" /proc/meminfo 2>/dev/null)"
fi
echo "D:$(df -Pk / 2>/dev/null | awk "NR==2{print \$2,\$3,\$5}")"
if [ -r /proc/net/dev ]; then
  echo "N:$(awk "NR>2 && \$1!~/^lo:/{rx+=\$2; tx+=\$10} END{printf \"%.0f %.0f\", rx, tx}" /proc/net/dev 2>/dev/null)"
fi
'`

// FetchStats samples live Linux/POSIX telemetry without requiring any remote agent.
func (s *Session) FetchStats(ctx context.Context) (ServerStats, error) {
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	t0 := time.Now()
	out, err := s.Exec(execCtx, telemetryScript, 16<<10)
	rtt := time.Since(t0).Milliseconds()
	if err != nil && strings.TrimSpace(out) == "" {
		return ServerStats{}, err
	}
	stats := parseTelemetry(out)
	if s.LatencyMs() > 0 {
		stats.LatencyMs = s.LatencyMs()
	} else {
		stats.LatencyMs = rtt
	}
	stats.CollectedAt = time.Now().Unix()
	return stats, nil
}

func parseTelemetry(raw string) ServerStats {
	var st ServerStats
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
		case 'P':
			parts := strings.Fields(val)
			if len(parts) >= 14 {
				nums := make([]float64, 14)
				for i := 0; i < 14; i++ {
					nums[i], _ = strconv.ParseFloat(parts[i], 64)
				}
				idle1 := nums[3] + nums[4]
				idle2 := nums[10] + nums[11]
				tot1 := nums[0] + nums[1] + nums[2] + nums[3] + nums[4] + nums[5] + nums[6]
				tot2 := nums[7] + nums[8] + nums[9] + nums[10] + nums[11] + nums[12] + nums[13]
				dt := tot2 - tot1
				di := idle2 - idle1
				if dt > 0 {
					pct := (1 - di/dt) * 100
					if pct < 0 {
						pct = 0
					}
					if pct > 100 {
						pct = 100
					}
					st.CPUPercent = math.Round(pct*10) / 10
				}
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
	return st
}
