package tools

import (
	"context"
	"fmt"
	"time"

	"ai.engineer.runner/internal/service"
	"ai.engineer.runner/internal/types"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TrivyTimeout caps one trivy_scan call. Image scans and the first
// vulnerability-DB download can be slow.
const TrivyTimeout = 10 * time.Minute

var TrivyTool = &mcp.Tool{
	Name:  "trivy_scan",
	Title: "Trivy security scan",
	Description: "Scan for security issues with Trivy. " +
		"scan_type=image scans a container image for vulnerable OS/language packages and secrets. " +
		"scan_type=fs scans a local project directory's dependency lockfiles (go.mod, package-lock.json, requirements.txt...) and source for secrets. " +
		"scan_type=repo scans a remote https git repository for vulnerable dependencies, secrets and IaC misconfigurations " +
		"(Dockerfile, Terraform, Kubernetes YAML, Helm) by default; set ref to scan a branch, tag or commit instead of the default branch. " +
		"scan_type=config checks IaC in a local directory for misconfigurations. " +
		"Returns findings sorted by severity with fix versions; secret values are never returned.",
	InputSchema: trivyInputSchema(),
	Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: boolPtr(true),
	},
}

// trivyInputSchema infers the schema from TrivyScanInput and adds enums,
// so clients show dropdowns and the SDK rejects bad values before we run.
func trivyInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[types.TrivyScanInput](nil)
	if err != nil {
		panic(fmt.Sprintf("trivy input schema: %v", err))
	}
	s.Properties["scan_type"].Enum = toAny(service.TrivyScanTypes)
	s.Properties["ref_type"].Enum = toAny(service.TrivyRefTypes)
	s.Properties["scanners"].Items.Enum = toAny(service.TrivyScanners)
	s.Properties["severities"].Items.Enum = toAny(service.TrivySeverities)
	return s
}

// RunTrivy is the MCP handler for trivy_scan.
func RunTrivy(ctx context.Context, _ *mcp.CallToolRequest, in types.TrivyScanInput) (*mcp.CallToolResult, *types.TrivyScanResult, error) {
	ctx, cancel := context.WithTimeout(ctx, TrivyTimeout)
	defer cancel()

	res, err := service.ScanTrivy(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("trivy_scan: %w", err)
	}
	return nil, res, nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
