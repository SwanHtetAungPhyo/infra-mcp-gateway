package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"ai.engineer.runner/internal/types"
)

// MaxTrivyFindings caps how many findings are returned to the client.
// Counts always cover every finding.
const MaxTrivyFindings = 200

var (
	TrivyScanTypes  = []string{"image", "fs", "repo", "config"}
	TrivyScanners   = []string{"vuln", "misconfig", "secret", "license"}
	TrivySeverities = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}

	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)

	TrivyRefTypes = []string{"branch", "tag", "commit"}
	// Full SHA-1 (40) or SHA-256 (64) object id.
	commitRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	// Starts with a letter or digit, so a ref can never be read as a flag.
	refNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// trivyAllowedRoots returns the directories fs/config scans may read.
// Set TRIVY_ALLOWED_ROOTS (colon-separated) to override; default is $HOME.
func trivyAllowedRoots() ([]string, error) {
	var roots []string
	if env := os.Getenv("TRIVY_ALLOWED_ROOTS"); env != "" {
		roots = filepath.SplitList(env)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		roots = []string{home}
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(r)
		if real == "/" {
			return nil, errors.New("TRIVY_ALLOWED_ROOTS must not be '/'; set a specific directory such as /workspace")
		}

		if err != nil {
			continue // a root that doesn't exist allows nothing
		}
		out = append(out, real)
	}
	if len(out) == 0 {
		return nil, errors.New("no valid TRIVY_ALLOWED_ROOTS")
	}
	return out, nil
}

func CheckTrivyConfig() error {
	_, err := trivyAllowedRoots()
	return err
}

// resolveScanPath makes sure a local path exists, is a directory, and sits
// inside an allowed root after symlinks are resolved.
func resolveScanPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("target must be an absolute path, got %q", p)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("target %q: %w", p, err)
	}

	//if real == "/" {
	//return nil, errors.New("TRIVY_ALLOWED_ROOTS must not be root. it must be /workspace")
	//}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("target %q: %w", p, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("target %q is not a directory", p)
	}
	roots, err := trivyAllowedRoots()
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		if rel, err := filepath.Rel(root, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", fmt.Errorf("target %q is outside the allowed directories %v", p, roots)
}

// ValidateTrivyInput checks the input and returns the normalised target.
func ValidateTrivyInput(in types.TrivyScanInput) (string, error) {
	if !slices.Contains(TrivyScanTypes, in.ScanType) {
		return "", fmt.Errorf("scan_type must be one of %v, got %q", TrivyScanTypes, in.ScanType)
	}
	for _, s := range in.Scanners {
		if !slices.Contains(TrivyScanners, s) {
			return "", fmt.Errorf("invalid scanner %q (allowed: %v)", s, TrivyScanners)
		}
	}
	for _, s := range in.Severities {
		if !slices.Contains(TrivySeverities, s) {
			return "", fmt.Errorf("invalid severity %q (allowed: %v)", s, TrivySeverities)
		}
	}

	t := strings.TrimSpace(in.Target)
	if t == "" {
		return "", errors.New("target is required")
	}

	if in.ScanType != "repo" && (in.Ref != "" || in.RefType != "") {
		return "", fmt.Errorf("ref and ref_type apply only to scan_type=repo, not %q", in.ScanType)
	}

	switch in.ScanType {
	case "image":
		if !imageRe.MatchString(t) {
			return "", fmt.Errorf("invalid image reference %q", t)
		}
		return t, nil
	case "repo":
		u, err := url.Parse(t)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return "", fmt.Errorf("repo target must be an https URL without credentials, got %q", t)
		}
		if _, _, err := resolveRef(in); err != nil {
			return "", err
		}
		return u.String(), nil
	default: // fs, config
		return resolveScanPath(t)
	}
}

// resolveRef validates ref/ref_type and returns the trivy flag and value
// ("--branch", "main"). An empty ref returns empty strings: scan the default branch.
func resolveRef(in types.TrivyScanInput) (flag, value string, err error) {
	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		if in.RefType != "" {
			return "", "", errors.New("ref_type is set but ref is empty")
		}
		return "", "", nil
	}
	kind := in.RefType
	if kind == "" {
		kind = "branch"
		if commitRe.MatchString(ref) {
			kind = "commit"
		}
	}
	switch kind {
	case "commit":
		if !commitRe.MatchString(ref) {
			return "", "", fmt.Errorf("commit ref must be a full 40-character (or 64 for SHA-256) hex SHA, got %q", ref)
		}
	case "branch", "tag":
		if !validRefName(ref) {
			return "", "", fmt.Errorf("invalid %s name %q", kind, ref)
		}
	default:
		return "", "", fmt.Errorf("ref_type must be one of %v, got %q", TrivyRefTypes, kind)
	}
	return "--" + kind, ref, nil
}

// validRefName is a conservative subset of git's ref-name rules: it can't be
// read as a flag and can't escape into another ref.
func validRefName(s string) bool {
	return refNameRe.MatchString(s) &&
		!strings.Contains(s, "..") && !strings.Contains(s, "//") &&
		!strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".lock")
}

// defaultScanners: what runs when the caller doesn't choose. A repo usually
// holds the Dockerfiles and IaC, so misconfig is on by default there.
func defaultScanners(scanType string) []string {
	if scanType == "repo" {
		return []string{"vuln", "secret", "misconfig"}
	}
	return nil // trivy's own default: vuln,secret
}

func buildTrivyArgs(in types.TrivyScanInput, target string) []string {
	args := []string{in.ScanType, "--format", "json", "--quiet", "--timeout", "9m"}
	if in.ScanType != "config" {
		args = append(args, "--list-all-pkgs=false")
		scanners := in.Scanners
		if len(scanners) == 0 {
			scanners = defaultScanners(in.ScanType)
		}
		if len(scanners) > 0 {
			args = append(args, "--scanners", strings.Join(scanners, ","))
		}
		if in.IgnoreUnfixed {
			args = append(args, "--ignore-unfixed")
		}
	}
	if len(in.Severities) > 0 {
		args = append(args, "--severity", strings.Join(in.Severities, ","))
	}
	if in.ScanType == "repo" {
		if flag, value, _ := resolveRef(in); flag != "" { // validated in ValidateTrivyInput
			args = append(args, flag, value)
		}
	}
	// "--" ends flag parsing, so the target can never be read as a flag.
	return append(args, "--", target)
}

// ScanTrivy runs trivy and returns a compact result.
func ScanTrivy(ctx context.Context, in types.TrivyScanInput) (*types.TrivyScanResult, error) {
	target, err := ValidateTrivyInput(in)
	if err != nil {
		return nil, err
	}

	stdout, stderr, err := RunCommand(ctx, "trivy", buildTrivyArgs(in, target)...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("trivy aborted: %w", ctx.Err())
		}
		return nil, fmt.Errorf("trivy failed: %w: %s", err, lastLines(stderr, 5))
	}

	var report types.TrivyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil, fmt.Errorf("parse trivy output: %w", err)
	}
	res := summarizeTrivy(in.ScanType, target, &report)
	if flag, value, _ := resolveRef(in); flag != "" && in.ScanType == "repo" {
		res.Ref = strings.TrimPrefix(flag, "--") + " " + value
	}
	return res, nil
}

func summarizeTrivy(scanType, target string, r *types.TrivyReport) *types.TrivyScanResult {
	var all []types.TrivyFinding
	for _, res := range r.Results {
		for _, v := range res.Vulnerabilities {
			all = append(all, types.TrivyFinding{
				Kind: "vulnerability", ID: v.VulnerabilityID, Severity: v.Severity, Title: v.Title,
				Location: res.Target, Package: v.PkgName, InstalledVersion: v.InstalledVersion,
				FixedVersion: v.FixedVersion, URL: v.PrimaryURL,
			})
		}
		for _, m := range res.Misconfigurations {
			if m.Status == "PASS" {
				continue
			}
			title := m.Title
			if m.Message != "" {
				title = m.Title + ": " + m.Message
			}
			id := m.ID
			if id == "" {
				id = m.AVDID
			}
			all = append(all, types.TrivyFinding{
				Kind: "misconfiguration", ID: id, Severity: m.Severity, Title: title,
				Location: res.Target, Line: m.CauseMetadata.StartLine, Resolution: m.Resolution, URL: m.PrimaryURL,
			})
		}
		for _, s := range res.Secrets {
			all = append(all, types.TrivyFinding{
				Kind: "secret", ID: s.RuleID, Severity: s.Severity, Title: s.Title,
				Location: res.Target, Line: s.StartLine,
			})
		}
		for _, l := range res.Licenses {
			loc := l.FilePath
			if loc == "" {
				loc = res.Target
			}
			all = append(all, types.TrivyFinding{
				Kind: "license", ID: l.Name, Severity: l.Severity, Title: l.Category + " license",
				Location: loc, Package: l.PkgName, URL: l.Link,
			})
		}
	}

	rank := func(s string) int {
		if i := slices.Index(TrivySeverities, s); i >= 0 {
			return i
		}
		return len(TrivySeverities)
	}
	sort.SliceStable(all, func(i, j int) bool { return rank(all[i].Severity) < rank(all[j].Severity) })

	counts := map[string]int{}
	for _, f := range all {
		counts[f.Severity]++
	}

	out := &types.TrivyScanResult{
		ScanType:   scanType,
		Target:     target,
		Counts:     counts,
		TotalFound: len(all),
		Findings:   all,
	}
	if out.Findings == nil {
		out.Findings = []types.TrivyFinding{}
	}
	if len(all) > MaxTrivyFindings {
		out.Findings = all[:MaxTrivyFindings]
		out.Truncated = true
	}

	parts := make([]string, 0, len(TrivySeverities))
	for _, s := range TrivySeverities {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if len(parts) == 0 {
		out.Summary = "no findings"
	} else {
		out.Summary = fmt.Sprintf("%d findings: %s", len(all), strings.Join(parts, ", "))
		if out.Truncated {
			out.Summary += fmt.Sprintf(" (showing top %d by severity)", MaxTrivyFindings)
		}
	}
	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
