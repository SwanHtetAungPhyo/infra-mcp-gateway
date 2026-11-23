package types

// NmapScanInput is the argument schema exposed to MCP clients for the nmap tool.
// The binary and output flags are fixed server-side; clients only pick what to scan.
type NmapScanInput struct {
	Targets          []string `json:"targets" jsonschema:"hosts, IPv4/IPv6 addresses or CIDR ranges to scan (max 16)"`
	Ports            string   `json:"ports,omitempty" jsonschema:"port spec such as 22,80,443 or 1-1024; omit for nmap's top 1000 ports"`
	ServiceDetection bool     `json:"service_detection,omitempty" jsonschema:"probe open ports for service/version info (-sV)"`
	Timing           *int     `json:"timing,omitempty" jsonschema:"nmap timing template 0-5 (-T); default 3"`
}

type NmapSummary struct {
	Summary string            `json:"summary"`
	Hosts   []NmapHostSummary `json:"hosts"`
}

type NmapHostSummary struct {
	Address   string         `json:"address"`
	Hostname  string         `json:"hostname,omitempty"`
	State     string         `json:"state"`
	OpenPorts []NmapOpenPort `json:"open_ports"`
}

type NmapOpenPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Service  string `json:"service,omitempty"`
	Product  string `json:"product,omitempty"`
	Version  string `json:"version,omitempty"`
}
