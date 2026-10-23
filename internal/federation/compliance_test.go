package federation

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// good is a server that meets every per-server floor and is a quarter of the
// node minimum, so four of them make a compliant node.
func good(name, ip string) Server {
	return Server{
		ID: "au-sydney/" + name, Name: name, Services: map[string]string{"photon": ip + ":22002"},
		Report:   Report{Cores: 2, RAMMB: 4096, DiskGB: 40, DiskSSD: true, IPv4: ip, PortMbps: 1000},
		LastSeen: now.Add(-30 * time.Second), ObservedIP: ip,
		DownMbps: 400, DownAt: now.Add(-time.Hour), UpMbps: 300, UpAt: now.Add(-time.Hour),
	}
}

func node(servers ...Server) *Node {
	return &Node{ID: "au-sydney", Approved: true, Servers: servers}
}

func has(t *testing.T, n *Node, want string) {
	t.Helper()
	for _, p := range n.Problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("no problem containing %q in %q", want, n.Problems)
}

func TestFourSmallServersMakeOneNode(t *testing.T) {
	r := DefaultRequirements()
	n := node(good("a", "1.1.1.1"), good("b", "1.1.1.2"), good("c", "1.1.1.3"), good("d", "1.1.1.4"))
	r.evaluate(n, now)
	if !n.InRotation || len(n.Problems) != 0 {
		t.Fatalf("compliant node out of rotation: %q", n.Problems)
	}
	if n.TotalCores != 8 || n.TotalRAMMB != 16384 || n.TotalDiskGB != 160 || n.Services["photon"] != "1.1.1.1:22002" {
		t.Fatalf("totals: %+v", n)
	}
}

func TestAggregateShortfallIsNamed(t *testing.T) {
	r := DefaultRequirements()
	n := node(good("a", "1.1.1.1"), good("b", "1.1.1.2"))
	r.evaluate(n, now)
	if n.InRotation {
		t.Fatal("half a node in rotation")
	}
	has(t, n, "total CPU cores 4, minimum 8")
	has(t, n, "total RAM 8.0 GB, minimum 16 GB")
	has(t, n, "total NVMe/SSD storage 80 GB, minimum 160 GB")
}

func TestSlackForgivesKernelReportedSizes(t *testing.T) {
	r := DefaultRequirements()
	s := good("a", "1.1.1.1")
	s.Cores, s.RAMMB, s.DiskGB = 8, 15600, 157 // a "16 GB / 160 GB" box as the kernel sees it
	n := node(s)
	r.evaluate(n, now)
	if !n.InRotation {
		t.Fatalf("slack not applied: %q", n.Problems)
	}
}

func TestPerServerFloors(t *testing.T) {
	r := DefaultRequirements()
	bad := good("tiny", "1.1.1.9")
	bad.Cores, bad.RAMMB, bad.DiskSSD, bad.PortMbps, bad.DownMbps = 1, 2048, false, 100, 20
	big := good("big", "1.1.1.1")
	big.Cores, big.RAMMB, big.DiskGB = 16, 32768, 500
	n := node(big, bad)
	r.evaluate(n, now)
	if n.InRotation {
		t.Fatal("one under-floor server must take the node out")
	}
	has(t, n, "server tiny: 1 CPU cores, minimum 2")
	has(t, n, "server tiny: RAM 2.0 GB, minimum 4 GB")
	has(t, n, "server tiny: storage is not NVMe or SSD")
	has(t, n, "server tiny: port 100 Mbps, minimum 1000 Mbps symmetric")
	has(t, n, "server tiny: measured download 20 Mbps, minimum 100 Mbps")
}

func TestAddressRules(t *testing.T) {
	r := DefaultRequirements()
	for ip, want := range map[string]string{
		"100.72.1.2":  "carrier-grade NAT",
		"192.168.1.5": "not a public address",
		"":            "no IPv4 address reported",
		"2001:db8::1": "not an IPv4 address",
	} {
		s := good("a", "1.1.1.1")
		s.IPv4 = ip
		n := node(s)
		r.evaluate(n, now)
		has(t, n, want)
	}
	s := good("a", "1.1.1.1")
	s.ObservedIP = "8.8.8.8"
	n := node(s)
	r.evaluate(n, now)
	has(t, n, "announced from 8.8.8.8")
}

func TestLivenessAndSelfTest(t *testing.T) {
	r := DefaultRequirements()
	s := good("a", "1.1.1.1")
	s.LastSeen = now.Add(-5 * time.Minute)
	n := node(s)
	r.evaluate(n, now)
	has(t, n, "no report for 5m0s")

	s = good("a", "1.1.1.1")
	s.UpAt = time.Time{}
	n = node(s)
	r.evaluate(n, now)
	has(t, n, "no bandwidth self-test yet")

	s = good("a", "1.1.1.1")
	s.DownAt = now.Add(-80 * time.Hour)
	n = node(s)
	r.evaluate(n, now)
	has(t, n, "self-test is 80h0m0s old")
	if !r.SelfTestDue(s, now) || r.SelfTestDue(good("b", "1.1.1.2"), now) {
		t.Fatal("self-test due wrong")
	}

	n = node()
	r.evaluate(n, now)
	has(t, n, "no servers have joined")
}

// A waived node skips the hardware figures and nothing else.
func TestWaiver(t *testing.T) {
	r := DefaultRequirements()
	s := good("vps", "1.1.1.1")
	s.Cores, s.RAMMB, s.DiskGB, s.DiskSSD, s.PortMbps, s.DownMbps = 1, 1024, 20, false, 100, 30
	n := node(s)
	n.WaiverReason = "owner test node"
	r.evaluate(n, now)
	if !n.InRotation {
		t.Fatalf("waived node out: %q", n.Problems)
	}
	n.WaiverUntil = now.Add(-time.Minute)
	r.evaluate(n, now)
	if n.InRotation {
		t.Fatal("expired waiver still applies")
	}
	n.WaiverUntil = time.Time{}
	n.Servers[0].IPv4 = "10.0.0.2"
	r.evaluate(n, now)
	has(t, n, "no public IPv4")
	n.Servers[0].IPv4 = "1.1.1.1"
	n.Servers[0].LastSeen = now.Add(-time.Hour)
	r.evaluate(n, now)
	has(t, n, "no report for")
	n.Servers[0].LastSeen = now
	n.Approved = false
	r.evaluate(n, now)
	if n.InRotation {
		t.Fatal("waiver must not replace approval")
	}
}

func TestRequirementsFromEnv(t *testing.T) {
	t.Setenv("ACCOUNT_FEDERATION_NODE_CORES", "12")
	t.Setenv("ACCOUNT_FEDERATION_SELFTEST_INTERVAL", "12h")
	r, err := RequirementsFromEnv()
	if err != nil || r.NodeCores != 12 || r.SelfTestInterval != 12*time.Hour || r.NodeRAMGB != 16 {
		t.Fatalf("%+v %v", r, err)
	}
	t.Setenv("ACCOUNT_FEDERATION_NODE_CORES", "eight")
	if _, err := RequirementsFromEnv(); err == nil {
		t.Fatal("malformed value accepted")
	}
}

func TestDefaultsAreTheOwnerFigures(t *testing.T) {
	r := DefaultRequirements()
	if r.NodeCores != 8 || r.NodeRAMGB != 16 || r.NodeDiskGB != 160 || r.ServerCores != 2 || r.ServerRAMGB != 4 ||
		r.ServerBandwidthMbps != 1000 || r.AvailabilityPct != 99.5 {
		t.Fatalf("%+v", r)
	}
}

func TestLocationOpen(t *testing.T) {
	if !(Location{Status: "open", Wanted: 2, Approved: 1}).Open() || (Location{Status: "open", Wanted: 1, Approved: 1}).Open() ||
		(Location{Status: "closed", Wanted: 3}).Open() {
		t.Fatal("open rule")
	}
	if !ValidRegion("sa-east") || !ValidRegion("oc") || ValidRegion("SA East") {
		t.Fatal("region shape")
	}
}
