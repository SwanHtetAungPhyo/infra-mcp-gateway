package types

// ---------------------------------------------------------------------------
// MCP tool input / output
// ---------------------------------------------------------------------------

// TrivyScanInput is the argument schema exposed to MCP clients for trivy_scan.
type TrivyScanInput struct {
	ScanType      string   `json:"scan_type" jsonschema:"image = container image reference; fs = local directory (dependencies + secrets); repo = remote git repository https URL; config = local directory IaC misconfigurations (Terraform, Kubernetes, Dockerfile...)"`
	Target        string   `json:"target" jsonschema:"what to scan: image ref like alpine:3.19, absolute directory path for fs/config, or https git URL for repo"`
	Ref           string   `json:"ref,omitempty" jsonschema:"repo only: branch name, tag or full 40-character commit SHA to scan; default is the repository's default branch"`
	RefType       string   `json:"ref_type,omitempty" jsonschema:"repo only: what ref is (branch, tag or commit); default: commit for a 40-character hex value, otherwise branch"`
	Scanners      []string `json:"scanners,omitempty" jsonschema:"subset of vuln, misconfig, secret, license; default vuln,secret,misconfig for repo, vuln,secret for image and fs (ignored for config)"`
	Severities    []string `json:"severities,omitempty" jsonschema:"only report these severities: UNKNOWN, LOW, MEDIUM, HIGH, CRITICAL; default all"`
	IgnoreUnfixed bool     `json:"ignore_unfixed,omitempty" jsonschema:"only report vulnerabilities that have a fixed version"`
}

// TrivyScanResult is the compact, LLM-friendly result returned by trivy_scan.
type TrivyScanResult struct {
	ScanType   string         `json:"scan_type"`
	Target     string         `json:"target"`
	Ref        string         `json:"ref,omitempty"` // e.g. "commit 522bcd2f..." for repo scans
	Summary    string         `json:"summary"`
	Counts     map[string]int `json:"counts"`
	Truncated  bool           `json:"truncated,omitempty"`
	Findings   []TrivyFinding `json:"findings"`
	TotalFound int            `json:"total_found"`
}

// TrivyFinding is one vulnerability, misconfiguration, secret or license issue.
// Secret values are never included: only the rule, file and line.
type TrivyFinding struct {
	Kind             string `json:"kind"` // vulnerability | misconfiguration | secret | license
	ID               string `json:"id"`
	Severity         string `json:"severity"`
	Title            string `json:"title,omitempty"`
	Location         string `json:"location"` // file / package manifest / image layer target
	Line             int    `json:"line,omitempty"`
	Package          string `json:"package,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	FixedVersion     string `json:"fixed_version,omitempty"`
	Resolution       string `json:"resolution,omitempty"`
	URL              string `json:"url,omitempty"`
}

// ---------------------------------------------------------------------------
// Raw `trivy --format json` report (only the fields we use)
// ---------------------------------------------------------------------------

type TrivyReport struct {
	ArtifactName string        `json:"ArtifactName"`
	ArtifactType string        `json:"ArtifactType"`
	Results      []TrivyResult `json:"Results"`
}

type TrivyResult struct {
	Target            string               `json:"Target"`
	Class             string               `json:"Class"`
	Type              string               `json:"Type"`
	Vulnerabilities   []TrivyVulnerability `json:"Vulnerabilities"`
	Misconfigurations []TrivyMisconfig     `json:"Misconfigurations"`
	Secrets           []TrivySecret        `json:"Secrets"`
	Licenses          []TrivyLicense       `json:"Licenses"`
}

type TrivyVulnerability struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
	PrimaryURL       string `json:"PrimaryURL"`
}

type TrivyMisconfig struct {
	ID            string `json:"ID"`
	AVDID         string `json:"AVDID"`
	Title         string `json:"Title"`
	Message       string `json:"Message"`
	Resolution    string `json:"Resolution"`
	Severity      string `json:"Severity"`
	PrimaryURL    string `json:"PrimaryURL"`
	Status        string `json:"Status"`
	CauseMetadata struct {
		StartLine int `json:"StartLine"`
	} `json:"CauseMetadata"`
}

// TrivySecret deliberately omits Match and Code, which contain the secret text.
type TrivySecret struct {
	RuleID    string `json:"RuleID"`
	Category  string `json:"Category"`
	Severity  string `json:"Severity"`
	Title     string `json:"Title"`
	StartLine int    `json:"StartLine"`
}

type TrivyLicense struct {
	Severity string `json:"Severity"`
	Category string `json:"Category"`
	PkgName  string `json:"PkgName"`
	FilePath string `json:"FilePath"`
	Name     string `json:"Name"`
	Link     string `json:"Link"`
}
