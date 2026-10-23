package federation

import (
	"fmt"
	"net/netip"
	"sort"
	"time"
)

// cgnat is RFC 6598 shared address space: an address here is behind
// carrier-grade NAT, and a console cannot reach a relay through it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicIPv4 says whether a reported address is one a console can reach.
func publicIPv4(s string) (bool, string) {
	a, err := netip.ParseAddr(s)
	switch {
	case s == "":
		return false, "no IPv4 address reported"
	case err != nil || !a.Is4():
		return false, fmt.Sprintf("%q is not an IPv4 address", s)
	case cgnat.Contains(a):
		return false, fmt.Sprintf("IPv4 %s is carrier-grade NAT (100.64.0.0/10)", s)
	case a.IsPrivate(), a.IsLoopback(), a.IsLinkLocalUnicast(), a.IsUnspecified(), a.IsMulticast(), !a.IsGlobalUnicast():
		return false, fmt.Sprintf("IPv4 %s is not a public address", s)
	}
	return true, ""
}

// Waived reports whether a node's hardware figures are waived at now.
func (n Node) Waived(now time.Time) bool {
	return n.WaiverReason != "" && (n.WaiverUntil.IsZero() || now.Before(n.WaiverUntil))
}

// evaluate fills in each server's problems, the node's totals, its own
// problems and whether it may be handed out. It is the one place the rules
// are applied: approval, /regions and the interceptor all read its verdict.
//
// A node is in rotation when it is approved and has nothing in Problems.
// Problems lists every shortfall, worded for a person: "total RAM 12 GB, minimum 16 GB".
func (r Requirements) evaluate(n *Node, now time.Time) {
	waived := n.Waived(now)
	n.Problems = nil
	n.TotalCores, n.TotalRAMMB, n.TotalDiskGB = 0, 0, 0
	n.Services = map[string]string{}
	n.LastSeen = time.Time{}
	if len(n.Servers) == 0 {
		n.Problems = append(n.Problems, "no servers have joined")
	}
	sort.Slice(n.Servers, func(i, j int) bool { return n.Servers[i].ID < n.Servers[j].ID })
	for i := range n.Servers {
		s := &n.Servers[i]
		s.Problems = r.serverProblems(*s, waived, now)
		n.TotalCores += s.Cores
		n.TotalRAMMB += s.RAMMB
		if s.DiskSSD {
			n.TotalDiskGB += s.DiskGB
		}
		if s.LastSeen.After(n.LastSeen) {
			n.LastSeen = s.LastSeen
		}
		for k, v := range s.Services {
			if _, taken := n.Services[k]; !taken {
				n.Services[k] = v
			}
		}
		for _, p := range s.Problems {
			n.Problems = append(n.Problems, "server "+s.Name+": "+p)
		}
	}
	if !waived && len(n.Servers) > 0 {
		if n.TotalCores < r.NodeCores {
			n.Problems = append(n.Problems, fmt.Sprintf("total CPU cores %d, minimum %d", n.TotalCores, r.NodeCores))
		}
		if n.TotalRAMMB < r.withSlack(int64(r.NodeRAMGB)*1024) {
			n.Problems = append(n.Problems, fmt.Sprintf("total RAM %s, minimum %d GB", gb(n.TotalRAMMB), r.NodeRAMGB))
		}
		if n.TotalDiskGB < r.withSlack(int64(r.NodeDiskGB)) {
			n.Problems = append(n.Problems, fmt.Sprintf("total NVMe/SSD storage %d GB, minimum %d GB", n.TotalDiskGB, r.NodeDiskGB))
		}
	}
	n.InRotation = n.Approved && len(n.Problems) == 0
}

func (r Requirements) serverProblems(s Server, waived bool, now time.Time) []string {
	var p []string
	if s.LastSeen.IsZero() {
		return []string{"has never reported"}
	}
	if now.Sub(s.LastSeen) > r.ReportStale {
		p = append(p, fmt.Sprintf("no report for %s (limit %s)", now.Sub(s.LastSeen).Round(time.Second), r.ReportStale))
	}
	if ok, why := publicIPv4(s.IPv4); !ok {
		p = append(p, "no public IPv4: "+why)
	} else if obs, err := netip.ParseAddr(s.ObservedIP); err == nil {
		if obs.Unmap().Is4() && obs.Unmap().String() != s.IPv4 {
			p = append(p, fmt.Sprintf("reports IPv4 %s but announced from %s (NAT or a proxy in the path)", s.IPv4, obs.Unmap()))
		} else if !obs.Unmap().Is4() {
			p = append(p, fmt.Sprintf("announced over IPv6 (%s), so its IPv4 cannot be checked; announce over IPv4", obs))
		}
	}
	if len(s.Services) == 0 {
		p = append(p, "announces no roles")
	}
	if s.DownAt.IsZero() || s.UpAt.IsZero() {
		p = append(p, "no bandwidth self-test yet")
	} else if age := now.Sub(oldest(s.DownAt, s.UpAt)); age > r.SelfTestMaxAge {
		p = append(p, fmt.Sprintf("bandwidth self-test is %s old (limit %s)", age.Round(time.Hour), r.SelfTestMaxAge))
	}
	if waived {
		return p
	}
	if s.Cores < r.ServerCores {
		p = append(p, fmt.Sprintf("%d CPU cores, minimum %d", s.Cores, r.ServerCores))
	}
	if s.RAMMB < r.withSlack(int64(r.ServerRAMGB)*1024) {
		p = append(p, fmt.Sprintf("RAM %s, minimum %d GB", gb(s.RAMMB), r.ServerRAMGB))
	}
	if !s.DiskSSD {
		p = append(p, "storage is not NVMe or SSD")
	}
	if s.PortMbps < r.ServerBandwidthMbps {
		p = append(p, fmt.Sprintf("port %d Mbps, minimum %d Mbps symmetric", s.PortMbps, r.ServerBandwidthMbps))
	}
	if !s.DownAt.IsZero() && s.DownMbps < float64(r.MeasuredMbps) {
		p = append(p, fmt.Sprintf("measured download %.0f Mbps, minimum %d Mbps", s.DownMbps, r.MeasuredMbps))
	}
	if !s.UpAt.IsZero() && s.UpMbps < float64(r.MeasuredMbps) {
		p = append(p, fmt.Sprintf("measured upload %.0f Mbps, minimum %d Mbps", s.UpMbps, r.MeasuredMbps))
	}
	return p
}

// SelfTestDue says whether a server should run its daily self-test now.
func (r Requirements) SelfTestDue(s Server, now time.Time) bool {
	return s.DownAt.IsZero() || s.UpAt.IsZero() || now.Sub(oldest(s.DownAt, s.UpAt)) >= r.SelfTestInterval
}

func oldest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func gb(mb int64) string { return fmt.Sprintf("%.1f GB", float64(mb)/1024) }
