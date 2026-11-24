package tools

import (
	"fmt"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type entry struct {
	name string
	bins []string
	add  func(server *mcp.Server)
}

var registry = []entry{
	{
		name: NmapTool.Name,
		bins: []string{
			"nmap", "nmap-formatter",
		},
		add: func(server *mcp.Server) {
			mcp.AddTool(server, NmapTool, RunNmap)
		},
	},
	{
		name: TrivyTool.Name,
		bins: []string{
			"trivy",
		},
		add: func(server *mcp.Server) {
			mcp.AddTool(server, TrivyTool, RunTrivy)
		},
	},
	{
		name: K8sListContextsTool.Name,
		add: func(server *mcp.Server) {
			mcp.AddTool(server, K8sListContextsTool, k8sSuite.RunListK8sContexts)
		},
	},
	{
		name: K8sListImagesTool.Name,
		add: func(server *mcp.Server) {
			mcp.AddTool(server, K8sListImagesTool, k8sSuite.RunListK8sImages)
		},
	},
	{
		name: K8sListNameSpaceTool.Name,
		add: func(server *mcp.Server) {
			mcp.AddTool(server, K8sListNameSpaceTool, k8sSuite.RunListK8sNameSpace)
		},
	},
}

var k8sSuite = NewK8sToolsSuite()

// K8sSuite returns the suite the k8s tools use, so other MCP features
// (resources) share its clients, context allowlist and lazy config loading.
func K8sSuite() *K8sToolsSuite { return k8sSuite }

func init() {
	for _, t := range k8sInventoryTools {
		registry = append(registry, entry{
			name: t.Name,
			add:  func(server *mcp.Server) { k8sSuite.addInventoryTool(server, t.Name) },
		})
	}
}

func RegisterAll(s *mcp.Server, enabled []string) (bins []string, err error) {
	want := map[string]bool{}
	for _, n := range enabled {
		want[n] = true
	}
	// Decide "all tools" once: want shrinks below as names are matched.
	all := len(want) == 0
	for _, e := range registry {
		if all || want[e.name] {
			e.add(s)
			bins = append(bins, e.bins...)
			delete(want, e.name)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("unknown tools in ENABLED_TOOLS: %v", slices.Sorted(maps.Keys(want)))
	}
	return bins, nil
}
