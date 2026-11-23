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
}

func RegisterAll(s *mcp.Server, enabled []string) (bins []string, err error) {
	want := map[string]bool{}
	for _, n := range enabled {
		want[n] = true
	}
	for _, e := range registry {
		if len(want) == 0 || want[e.name] {
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
