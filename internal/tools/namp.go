package tools

import (
	"context"
	"fmt"
	"time"

	"ai.engineer.runner/internal/service"
	"ai.engineer.runner/internal/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ScanTimeout caps how long one tool call may run nmap.
const ScanTimeout = 5 * time.Minute

// NmapTool describes the tool to MCP clients. Input/output schemas are inferred
// by mcp.AddTool from types.NmapScanInput and types.NmapSummary.
var NmapTool = &mcp.Tool{
	Name:  "nmap_scan",
	Title: "Nmap port scan",
	Description: "Scan hosts with nmap and return each host's state and open ports (with service and version when service_detection is on). " +
		"Targets must be inside the server's allowed network ranges; out-of-scope targets are rejected.",
	Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: boolPtr(true),
	},
}

// RunNmap is the MCP handler. The SDK uses the typed output to fill
// CallToolResult.StructuredContent and Content, and a returned error becomes
// IsError=true with the message in Content, so the model can read it and retry.
func RunNmap(ctx context.Context, _ *mcp.CallToolRequest, in types.NmapScanInput) (*mcp.CallToolResult, *types.NmapSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, ScanTimeout)
	defer cancel()

	run, err := service.ScanNmap(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("nmap_scan: %w", err)
	}
	return nil, service.SummarizeNmap(run), nil
}

func boolPtr(b bool) *bool { return &b }
