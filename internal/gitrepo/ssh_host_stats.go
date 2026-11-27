// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

type SSHHostStats struct {
	Load        [3]float64
	HasLoad     bool
	CPUs        int
	Interface   string
	Received    uint64
	Transmitted uint64
}

// 只取默认路由网卡，避免把隧道、网桥与物理网卡上的同一流量重复计入。
const sshHostStatsScript = `
export LC_ALL=C
if [ -r /proc/loadavg ]; then
    awk '{print "load", $1, $2, $3}' /proc/loadavg
    iface=$(awk '$2=="00000000" && $8=="00000000" && $1!="lo" && (!found || $7+0<metric) {name=$1; metric=$7+0; found=1} END {print name}' /proc/net/route 2>/dev/null)
    if [ -z "$iface" ]; then
        iface=$(ip -6 route show default 2>/dev/null | awk '{for(i=1;i<NF;i++) if($i=="dev") {print $(i+1); exit}}')
    fi
    awk -v dev="$iface" '{
        sub(/^[[:space:]]*/, "")
        if (dev!="" && index($0,dev ":")==1) {
            $0=substr($0,length(dev)+2)
            if (NF>=16) printf "net %s %s %s\n", dev, $1, $9
            exit
        }
    }' /proc/net/dev
else
    sysctl -n vm.loadavg 2>/dev/null | awk '$1=="{" {print "load", $2, $3, $4}'
    iface=$(route -n get default 2>/dev/null | awk '$1=="interface:" {print $2; exit}')
    if [ -n "$iface" ]; then
        netstat -ibn -I "$iface" 2>/dev/null | awk -v dev="$iface" '
            NR==1 {for(i=1;i<=NF;i++) {if($i=="Ibytes") rx=i; if($i=="Obytes") tx=i}}
            $1==dev && $3~/^<Link/ && rx && tx {print "net", dev, $rx, $tx; exit}'
    fi
fi
printf 'cpus %s\n' "$(getconf _NPROCESSORS_ONLN 2>/dev/null)"
`

func (repo SSHWorkspace) HostStats(ctx context.Context) SSHHostStats {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := runSSHScript(ctx, repo.Host, sshHostStatsScript)
	if err != nil {
		return SSHHostStats{}
	}
	return parseSSHHostStats(raw)
}

func parseSSHHostStats(raw string) SSHHostStats {
	var stats SSHHostStats
	for line := range strings.Lines(raw) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "load":
			if len(fields) == 4 {
				stats.HasLoad = true
				for index, field := range fields[1:] {
					value, err := strconv.ParseFloat(field, 64)
					if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
						stats.HasLoad = false
						break
					}
					stats.Load[index] = value
				}
			}
		case "cpus":
			if len(fields) == 2 {
				count, err := strconv.Atoi(fields[1])
				if err == nil && count > 0 {
					stats.CPUs = count
				}
			}
		case "net":
			if len(fields) == 4 {
				received, rxErr := strconv.ParseUint(fields[2], 10, 64)
				transmitted, txErr := strconv.ParseUint(fields[3], 10, 64)
				if rxErr == nil && txErr == nil {
					stats.Interface, stats.Received, stats.Transmitted = fields[1], received, transmitted
				}
			}
		}
	}
	return stats
}
