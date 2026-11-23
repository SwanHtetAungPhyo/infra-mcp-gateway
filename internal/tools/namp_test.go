package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"ai.engineer.runner/internal/types"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(srv, NmapTool, RunNmap)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestRejectsInjection(t *testing.T) {
	t.Setenv("NMAP_ALLOWED_CIDRS", "127.0.0.1/32")
	cs := connect(t)
	for _, args := range []map[string]any{
		{"targets": []string{"8.8.8.8"}}, // valid syntax, out of scope
		{"targets": []string{"--script=evil"}},
		{"targets": []string{"127.0.0.1; rm -rf /"}},
		{"targets": []string{"127.0.0.1"}, "ports": "80 -oN /etc/passwd"},
		{"targets": []string{}},
		{"targets": []string{"127.0.0.1"}, "timing": 9},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nmap_scan", Arguments: args})
		if err != nil {
			t.Fatalf("%v: protocol error %v", args, err)
		}
		if !res.IsError {
			t.Errorf("%v: expected IsError", args)
		}
	}
}

func TestScanLocalhost(t *testing.T) {
	if _, err := exec.LookPath("nmap"); err != nil {
		t.Skip("nmap not installed")
	}
	t.Setenv("NMAP_ALLOWED_CIDRS", "127.0.0.1/32")
	cs := connect(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "nmap_scan",
		Arguments: map[string]any{"targets": []string{"127.0.0.1"}, "ports": "1-1024"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content[0].(*mcp.TextContent).Text)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out types.NmapSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary == "" || len(out.Hosts) != 1 || out.Hosts[0].Address != "127.0.0.1" {
		t.Fatalf("expected one summarized host, got %s", raw)
	}
}
