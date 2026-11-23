package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai.engineer.runner/internal/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectTrivy(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(srv, TrivyTool, RunTrivy)
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

func TestTrivyRejectsBadInput(t *testing.T) {
	allowed := t.TempDir()
	t.Setenv("TRIVY_ALLOWED_ROOTS", allowed)
	outside := t.TempDir() // sibling temp dir, not under allowed
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	cs := connectTrivy(t)
	for _, args := range []map[string]any{
		{"scan_type": "rm", "target": "/"},
		{"scan_type": "image", "target": "--config=/etc/passwd"},
		{"scan_type": "image", "target": "alpine; rm -rf /"},
		{"scan_type": "repo", "target": "file:///etc"},
		{"scan_type": "repo", "target": "https://user:pass@github.com/x/y"},
		{"scan_type": "fs", "target": "relative/path"},
		{"scan_type": "fs", "target": outside},
		{"scan_type": "fs", "target": link}, // symlink pointing outside
		{"scan_type": "fs", "target": allowed + "/../"},
		{"scan_type": "fs", "target": allowed, "scanners": []string{"vuln", "--exec"}},
		{"scan_type": "fs", "target": allowed, "severities": []string{"high"}},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "trivy_scan", Arguments: args})
		if err != nil {
			t.Fatalf("%v: protocol error %v", args, err)
		}
		if !res.IsError {
			t.Errorf("%v: expected IsError", args)
		}
	}
}

func TestTrivySecretScan(t *testing.T) {
	if _, err := exec.LookPath("trivy"); err != nil {
		t.Skip("trivy not installed")
	}
	dir := t.TempDir()
	t.Setenv("TRIVY_ALLOWED_ROOTS", dir)
	secret := "ghp_" + strings.Repeat("A1b2C3d4E5", 3) + "f6G7h8"
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte("GITHUB_TOKEN="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cs := connectTrivy(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "trivy_scan",
		Arguments: map[string]any{"scan_type": "fs", "target": dir, "scanners": []string{"secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].(*mcp.TextContent).Text)
	}

	raw, _ := json.Marshal(res.StructuredContent)
	if strings.Contains(string(raw), secret) || strings.Contains(res.Content[0].(*mcp.TextContent).Text, secret) {
		t.Fatal("secret value leaked into tool output")
	}
	var out types.TrivyScanResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.TotalFound == 0 || out.Findings[0].Kind != "secret" || out.Findings[0].Line != 1 {
		t.Fatalf("expected a secret finding on line 1, got %s", raw)
	}
	t.Logf("result: %s", raw)
}
