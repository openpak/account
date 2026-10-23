package federation

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Requirements are the admission rules for a node, in one place. Every figure
// the website shows (operator terms, the application form, the admin page) is
// read from here through GetRequirements, so changing one means changing it
// once, in the environment of the core.
//
// Aggregates (cores, RAM, disk) count all of a node's servers together. The
// per-server floors, the address rules and bandwidth hold for each server on
// its own: bandwidth does not add up across servers.
type Requirements struct {
	NodeCores  int // total CPU cores across the node's servers
	NodeRAMGB  int // total RAM, GB
	NodeDiskGB int // total NVMe/SSD, GB

	ServerCores         int // per server
	ServerRAMGB         int // per server
	ServerBandwidthMbps int // per server, symmetric, declared port speed

	// MeasuredMbps is the floor for home's own timed transfer, each direction.
	// It is far below ServerBandwidthMbps on purpose: the test runs over the
	// public internet to one home box, so it proves a server is not lying about
	// a gigabit port by an order of magnitude, not that it has one.
	MeasuredMbps int

	// AvailabilityPct is the target operators agree to. It is published, not
	// enforced: liveness is enforced by ReportStale.
	AvailabilityPct float64

	// SlackPct forgives the gap between a box's advertised size and what the
	// kernel reports: MemTotal leaves out reserved memory and a filesystem is
	// smaller than its disk. A "16 GB" box reporting 15.2 GiB passes.
	SlackPct int

	ReportStale      time.Duration // no announce for this long = not reporting
	SelfTestInterval time.Duration // at most one self-test per direction per server per interval
	SelfTestMaxAge   time.Duration // an older (or missing) self-test fails the server
	SelfTestBytes    int64         // cap on one timed transfer, each direction
}

// DefaultRequirements are the owner's figures of 2026-10-23.
func DefaultRequirements() Requirements {
	return Requirements{
		NodeCores: 8, NodeRAMGB: 16, NodeDiskGB: 160,
		ServerCores: 2, ServerRAMGB: 4, ServerBandwidthMbps: 1000,
		MeasuredMbps:     100,
		AvailabilityPct:  99.5,
		SlackPct:         10,
		ReportStale:      Stale,
		SelfTestInterval: 24 * time.Hour,
		SelfTestMaxAge:   72 * time.Hour,
		SelfTestBytes:    100_000_000,
	}
}

// RequirementsFromEnv is DefaultRequirements with any ACCOUNT_FEDERATION_*
// override applied. A malformed value fails startup rather than silently
// admitting nodes on a default.
func RequirementsFromEnv() (Requirements, error) {
	r := DefaultRequirements()
	ints := []struct {
		env string
		dst *int
	}{
		{"ACCOUNT_FEDERATION_NODE_CORES", &r.NodeCores},
		{"ACCOUNT_FEDERATION_NODE_RAM_GB", &r.NodeRAMGB},
		{"ACCOUNT_FEDERATION_NODE_DISK_GB", &r.NodeDiskGB},
		{"ACCOUNT_FEDERATION_SERVER_CORES", &r.ServerCores},
		{"ACCOUNT_FEDERATION_SERVER_RAM_GB", &r.ServerRAMGB},
		{"ACCOUNT_FEDERATION_SERVER_BANDWIDTH_MBPS", &r.ServerBandwidthMbps},
		{"ACCOUNT_FEDERATION_MEASURED_MBPS", &r.MeasuredMbps},
		{"ACCOUNT_FEDERATION_SLACK_PCT", &r.SlackPct},
	}
	for _, f := range ints {
		if v := os.Getenv(f.env); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return r, fmt.Errorf("%s: want a non-negative integer, got %q", f.env, v)
			}
			*f.dst = n
		}
	}
	if v := os.Getenv("ACCOUNT_FEDERATION_AVAILABILITY_PCT"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f > 100 {
			return r, fmt.Errorf("ACCOUNT_FEDERATION_AVAILABILITY_PCT: want a percentage, got %q", v)
		}
		r.AvailabilityPct = f
	}
	durs := []struct {
		env string
		dst *time.Duration
	}{
		{"ACCOUNT_FEDERATION_REPORT_STALE", &r.ReportStale},
		{"ACCOUNT_FEDERATION_SELFTEST_INTERVAL", &r.SelfTestInterval},
		{"ACCOUNT_FEDERATION_SELFTEST_MAX_AGE", &r.SelfTestMaxAge},
	}
	for _, d := range durs {
		if v := os.Getenv(d.env); v != "" {
			p, err := time.ParseDuration(v)
			if err != nil || p <= 0 {
				return r, fmt.Errorf("%s: want a duration such as 24h, got %q", d.env, v)
			}
			*d.dst = p
		}
	}
	if v := os.Getenv("ACCOUNT_FEDERATION_SELFTEST_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1_000_000 {
			return r, fmt.Errorf("ACCOUNT_FEDERATION_SELFTEST_BYTES: want at least 1000000, got %q", v)
		}
		r.SelfTestBytes = n
	}
	return r, nil
}

// withSlack is the smallest reported figure that still counts as want.
func (r Requirements) withSlack(want int64) int64 {
	return want * int64(100-r.SlackPct) / 100
}
