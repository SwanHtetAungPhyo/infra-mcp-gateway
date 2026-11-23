package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
)

const minPrefixBits = 24
const maxTargets = 16

var hostnameRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

var (
	// Hostnames, IPv4/IPv6 and CIDR. Must not start with '-' so it can't be read as a flag.
	targetRe = regexp.MustCompile(`^[A-Za-z0-9_.:/\[\]][A-Za-z0-9_.:/\[\]-]{0,252}$`)
	// e.g. "22,80,443", "1-1024", "T:80,U:53"
	portsRe = regexp.MustCompile(`^[0-9TUS:,\-]{1,256}$`)
)

func allowedCIDRs() ([]netip.Prefix, error) {
	raw := os.Getenv("NMAP_ALLOWED_CIDRS")
	if raw == "" {
		return nil, errors.New("NMAP_ALLOWED_CIDRS is not set")
	}

	var out []netip.Prefix
	for _, s := range strings.Split(raw, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("NMAP_ALLOWED_CIDRS %q: %w", s, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// inScope: p lies entirely inside one allowed range.
func inScope(allowed []netip.Prefix, p netip.Prefix) bool {
	for _, a := range allowed {
		if a.Bits() <= p.Bits() && a.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// resolveTargets returns the IPs/CIDRs to hand to nmap, all in scope.
func resolveTargets(ctx context.Context, targets []string) ([]string, error) {
	allowed, err := allowedCIDRs()
	if err != nil {
		return nil, err
	}
	var out []string
	check := func(orig string, p netip.Prefix) error {
		if !p.Addr().Is4() {
			return fmt.Errorf("target %q: only IPv4 is supported", orig)
		}
		if p.Bits() < minPrefixBits {
			return fmt.Errorf("target %q: range too large (max /%d)", orig, minPrefixBits)
		}
		if !inScope(allowed, p) {
			return fmt.Errorf("target %q (%s) is outside the allowed ranges", orig, p)
		}
		out = append(out, p.String())
		return nil
	}
	for _, t := range targets {
		if p, err := netip.ParsePrefix(t); err == nil { // CIDR
			if err := check(t, p.Masked()); err != nil {
				return nil, err
			}
			continue
		}
		if a, err := netip.ParseAddr(t); err == nil { // single IP
			a = a.Unmap()
			if err := check(t, netip.PrefixFrom(a, a.BitLen())); err != nil {
				return nil, err
			}
			continue
		}
		if !hostnameRe.MatchString(t) {
			return nil, fmt.Errorf("invalid target %q", t)
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", t)
		if err != nil || len(addrs) == 0 {
			return nil, fmt.Errorf("target %q: cannot resolve", t)
		}
		for _, a := range addrs { // every address must be in scope
			if err := check(t, netip.PrefixFrom(a.Unmap(), 32)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// ValidateScanInput rejects anything that could be smuggled into nmap as an extra flag.
func ValidateScanInput(in types.NmapScanInput) error {
	if len(in.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	if len(in.Targets) > maxTargets {
		return fmt.Errorf("too many targets: %d (max %d)", len(in.Targets), maxTargets)
	}
	for _, t := range in.Targets {
		if !targetRe.MatchString(t) {
			return fmt.Errorf("invalid target %q", t)
		}
	}
	if in.Ports != "" && !portsRe.MatchString(in.Ports) {
		return fmt.Errorf("invalid ports %q", in.Ports)
	}
	if in.Timing != nil && (*in.Timing < 0 || *in.Timing > 5) {
		return fmt.Errorf("timing must be 0-5, got %d", *in.Timing)
	}
	return nil
}

func buildNmapArgs(in types.NmapScanInput, xmlFile string) []string {
	args := []string{"-oX", xmlFile}
	if in.Ports != "" {
		args = append(args, "-p", in.Ports)
	}
	if in.ServiceDetection {
		args = append(args, "-sV")
	}
	if in.Timing != nil {
		args = append(args, "-T"+strconv.Itoa(*in.Timing))
	}
	// "--" ends option parsing, so targets can never be read as flags.
	args = append(args, "--")
	return append(args, in.Targets...)
}

// ScanNmap runs nmap with validated input and returns the parsed result.
// The XML report goes to a temp file that is always deleted.
func ScanNmap(ctx context.Context, in types.NmapScanInput) (*types.NmapRun, error) {
	if err := ValidateScanInput(in); err != nil {
		return nil, err
	}

	// Scope check. Never continue on error: an empty target list makes nmap
	// scan nothing and exit 0, which would look like a successful scan.
	resolved, err := resolveTargets(ctx, in.Targets)
	if err != nil {
		return nil, err
	}
	in.Targets = resolved

	dir, err := os.MkdirTemp("", "nmap-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	xmlFile := filepath.Join(dir, "scan.xml")

	if _, stderr, err := RunCommand(ctx, "nmap", buildNmapArgs(in, xmlFile)...); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("nmap aborted: %w", ctx.Err())
		}
		return nil, fmt.Errorf("nmap failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	stdout, stderr, err := RunCommand(ctx, "nmap-formatter", "json", xmlFile)
	if err != nil {
		return nil, fmt.Errorf("nmap-formatter failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	var run types.NmapRun
	if err := json.Unmarshal([]byte(stdout), &run); err != nil {
		return nil, fmt.Errorf("parse nmap-formatter output: %w", err)
	}
	return &run, nil
}

// SummarizeNmap turns the full nmap report into the compact shape the MCP
// tool returns: hosts with their open ports, nothing else.
func SummarizeNmap(run *types.NmapRun) *types.NmapSummary {
	out := &types.NmapSummary{Hosts: []types.NmapHostSummary{}}
	open := 0
	for _, h := range run.Host.Items {
		hs := types.NmapHostSummary{State: h.Status.State, OpenPorts: []types.NmapOpenPort{}}
		if a := h.HostAddress.Items; len(a) > 0 {
			hs.Address = a[0].Address
		}
		if n := h.HostNames.HostName.Items; len(n) > 0 {
			hs.Hostname = n[0].Name
		}
		for _, p := range h.Port.Items {
			if p.State.State != "open" {
				continue
			}
			hs.OpenPorts = append(hs.OpenPorts, types.NmapOpenPort{
				Port: p.PortID, Protocol: p.Protocol,
				Service: p.Service.Name, Product: p.Service.Product, Version: p.Service.Version,
			})
		}
		open += len(hs.OpenPorts)
		out.Hosts = append(out.Hosts, hs)
	}
	out.Summary = fmt.Sprintf("%d of %d hosts up, %d open ports, %.2fs",
		run.RunStats.Hosts.Up, run.RunStats.Hosts.Total, open, run.RunStats.Finished.Elapsed)
	return out
}

// RunNmap is the scheduled full scan of localhost used by the runner loop.
// The runningTime argument is kept for the existing caller.
func RunNmap(_ string) chan types.NmapResult {
	ch := make(chan types.NmapResult, 1) // buffered: the send never blocks, even if the caller gave up

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()

		run, err := ScanNmap(ctx, types.NmapScanInput{
			Targets:          []string{"localhost"},
			Ports:            "1-65535",
			ServiceDetection: true,
		})
		if err != nil {
			ch <- types.NmapResult{Err: err}
			return
		}
		ch <- types.NmapResult{Run: *run}
	}()
	return ch
}
