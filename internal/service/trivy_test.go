package service

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"ai.engineer.runner/internal/types"
)

const (
	testRepo   = "https://github.com/docker-library/hello-world"
	testCommit = "522bcd2faf422c60b9d20e64d7cd6d56600aec97"
)

func TestTrivyRootSlashRejected(t *testing.T) {
	t.Setenv("TRIVY_ALLOWED_ROOTS", "/")
	if err := CheckTrivyConfig(); err == nil {
		t.Fatal("expected '/' to be rejected")
	}
}

func TestResolveRef(t *testing.T) {
	for _, tc := range []struct {
		ref, refType   string
		wantFlag, want string // wantFlag "" + want "err" means an error is expected
	}{
		{"", "", "", ""},                 // default branch
		{"main", "", "--branch", "main"}, // plain name is a branch
		{"release/1.4", "", "--branch", "release/1.4"},
		{"v1.4.2", "tag", "--tag", "v1.4.2"},
		{testCommit, "", "--commit", testCommit}, // 40 hex chars = commit
		{testCommit, "commit", "--commit", testCommit},
		{"522bcd2", "commit", "", "err"},      // short SHA: trivy needs the full one
		{"--upload-pack=evil", "", "", "err"}, // flag injection
		{"-main", "branch", "", "err"},
		{"main..evil", "", "", "err"},
		{"refs//x", "", "", "err"},
		{"main.lock", "", "", "err"},
		{"main; rm -rf /", "", "", "err"},
		{"main", "sha", "", "err"}, // unknown ref_type
		{"", "branch", "", "err"},  // ref_type without ref
	} {
		flag, val, err := resolveRef(types.TrivyScanInput{ScanType: "repo", Ref: tc.ref, RefType: tc.refType})
		if tc.want == "err" {
			if err == nil {
				t.Errorf("ref %q type %q: expected an error, got %s %s", tc.ref, tc.refType, flag, val)
			}
			continue
		}
		if err != nil || flag != tc.wantFlag || val != tc.want {
			t.Errorf("ref %q type %q: got %q %q %v, want %q %q", tc.ref, tc.refType, flag, val, err, tc.wantFlag, tc.want)
		}
	}
}

func TestRefOnlyForRepo(t *testing.T) {
	_, err := ValidateTrivyInput(types.TrivyScanInput{ScanType: "image", Target: "alpine:3.19", Ref: "main"})
	if err == nil {
		t.Fatal("expected ref on an image scan to be rejected")
	}
}

func TestBuildTrivyArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   types.TrivyScanInput
		want string // the args between the subcommand and "--"
	}{
		{"repo defaults include misconfig",
			types.TrivyScanInput{ScanType: "repo"},
			"--format json --quiet --timeout 9m --list-all-pkgs=false --scanners vuln,secret,misconfig"},
		{"repo with explicit scanners keeps them",
			types.TrivyScanInput{ScanType: "repo", Scanners: []string{"secret"}},
			"--format json --quiet --timeout 9m --list-all-pkgs=false --scanners secret"},
		{"repo at a commit",
			types.TrivyScanInput{ScanType: "repo", Ref: testCommit},
			"--format json --quiet --timeout 9m --list-all-pkgs=false --scanners vuln,secret,misconfig --commit " + testCommit},
		{"image keeps trivy's default scanners",
			types.TrivyScanInput{ScanType: "image"},
			"--format json --quiet --timeout 9m --list-all-pkgs=false"},
	} {
		args := buildTrivyArgs(tc.in, "TARGET")
		i := slices.Index(args, "--")
		if got := strings.Join(args[1:i], " "); got != tc.want || args[0] != tc.in.ScanType || args[len(args)-1] != "TARGET" {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, strings.Join(args, " "), tc.want)
		}
	}
}

// Clones a small public repo, so it only runs when asked:
//
//	TRIVY_NETWORK_TESTS=1 go test ./internal/service -run RepoAtCommit
func TestScanTrivyRepoAtCommit(t *testing.T) {
	if os.Getenv("TRIVY_NETWORK_TESTS") == "" {
		t.Skip("set TRIVY_NETWORK_TESTS=1 to run")
	}
	if _, err := exec.LookPath("trivy"); err != nil {
		t.Skip("trivy not installed")
	}
	res, err := ScanTrivy(context.Background(), types.TrivyScanInput{
		ScanType: "repo", Target: testRepo, Ref: testCommit, Scanners: []string{"misconfig"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref != "commit "+testCommit {
		t.Errorf("ref not echoed: %q", res.Ref)
	}
	if res.TotalFound == 0 || res.Findings[0].Kind != "misconfiguration" {
		t.Errorf("expected Dockerfile misconfigurations, got %s", res.Summary)
	}
}
