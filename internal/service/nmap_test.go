package service

import (
	"context"
	"os/exec"
	"testing"

	"ai.engineer.runner/internal/types"
)

func TestResolveTargets(t *testing.T) {
	t.Setenv("NMAP_ALLOWED_CIDRS", "10.0.0.0/16")
	for target, ok := range map[string]bool{
		"10.0.1.5":        true,
		"10.0.1.0/24":     true,
		"10.0.1.77/24":    true,  // masked to 10.0.1.0/24
		"10.0.0.0/8":      false, // wider than the allowed range
		"10.0.0.0/20":     false, // wider than /24
		"10.1.0.1":        false,
		"8.8.8.8":         false,
		"169.254.169.254": false,
		"::1":             false, // IPv6 not supported yet
		"10.0.1.5; id":    false,
	} {
		_, err := resolveTargets(context.Background(), []string{target})
		if (err == nil) != ok {
			t.Errorf("%s: want allowed=%v, got err=%v", target, ok, err)
		}
	}
}

func TestResolveTargetsFailsClosedWithoutConfig(t *testing.T) {
	t.Setenv("NMAP_ALLOWED_CIDRS", "")
	if _, err := resolveTargets(context.Background(), []string{"127.0.0.1"}); err == nil {
		t.Fatal("expected an error when NMAP_ALLOWED_CIDRS is unset")
	}
}

// Regression: the resolveTargets error was dropped, so nmap ran with no
// targets, exited 0, and the scan looked successful.
func TestScanNmapRejectsOutOfScope(t *testing.T) {
	if _, err := exec.LookPath("nmap"); err != nil {
		t.Skip("nmap not installed")
	}
	t.Setenv("NMAP_ALLOWED_CIDRS", "127.0.0.1/32")
	run, err := ScanNmap(context.Background(), types.NmapScanInput{Targets: []string{"10.9.9.9"}, Ports: "80"})
	if err == nil {
		t.Fatalf("expected an out-of-scope error, got result %+v", run)
	}
}

func TestSummarizeNmap(t *testing.T) {
	var run types.NmapRun
	run.RunStats.Hosts.Up, run.RunStats.Hosts.Total = 1, 1
	host := types.Host{Status: types.HostStatus{State: "up"}}
	host.HostAddress.Items = []types.HostAddress{{Address: "127.0.0.1"}}
	host.Port.Items = []types.Port{
		{PortID: 22, Protocol: "tcp", State: types.PortState{State: "open"}, Service: types.Service{Name: "ssh"}},
		{PortID: 23, Protocol: "tcp", State: types.PortState{State: "closed"}},
	}
	run.Host.Items = []types.Host{host}

	s := SummarizeNmap(&run)
	if len(s.Hosts) != 1 || len(s.Hosts[0].OpenPorts) != 1 || s.Hosts[0].OpenPorts[0].Port != 22 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if s.Summary != "1 of 1 hosts up, 1 open ports, 0.00s" {
		t.Fatalf("summary line: %q", s.Summary)
	}
}
