package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"resty.dev/v3"
)

func TestPostJSONStatus(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"ok", http.StatusOK, `{"status":true,"run_token":"abc"}`, ""},
		{"unauthorized", http.StatusUnauthorized, `{"error":"bad token"}`, "401"},
		{"server error", http.StatusInternalServerError, "boom", "500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := resty.New()
			defer c.Close()

			got, err := postJSON[ClaimResponse](context.Background(), c, srv.URL, nil, nil)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !got.Status || got.RunToken != "abc" {
					t.Fatalf("bad decode: %+v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), tt.body) {
				t.Fatalf("want error containing %q and body, got %v", tt.wantErr, err)
			}
		})
	}
}

// listTools connects an in-memory client to the server main actually builds.
func listTools(t *testing.T) []string {
	t.Helper()
	server, _, err := newMCPServer()
	if err != nil {
		t.Fatalf("newMCPServer: %v", err)
	}
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// Regression: ENABLED_TOOL typo + " " comparison registered zero tools.
func TestNewMCPServerRegistersTools(t *testing.T) {
	t.Setenv("ENABLED_TOOLS", "")
	if got := listTools(t); !slices.Equal(got, []string{"k8s_list_contexts", "k8s_list_images", "k8s_list_namespaces", "nmap_scan", "trivy_scan"}) {
		t.Fatalf("unset ENABLED_TOOLS: got %v", got)
	}
	t.Setenv("ENABLED_TOOLS", " trivy_scan, ")
	if got := listTools(t); !slices.Equal(got, []string{"trivy_scan"}) {
		t.Fatalf("ENABLED_TOOLS=trivy_scan: got %v", got)
	}
	t.Setenv("ENABLED_TOOLS", "nope")
	if _, _, err := newMCPServer(); err == nil {
		t.Fatal("expected an error for an unknown tool name")
	}
}

func TestBearerAuth(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef"
	h := bearerAuth(tok, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for hdr, want := range map[string]int{
		"":              http.StatusUnauthorized,
		"Bearer wrong":  http.StatusUnauthorized,
		tok:             http.StatusUnauthorized, // missing "Bearer "
		"Bearer " + tok: http.StatusNoContent,
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("header %q: got %d, want %d", hdr, rec.Code, want)
		}
	}
}

func TestRunHTTPRequiresToken(t *testing.T) {
	t.Setenv("MCP_AUTH_TOKEN", "short")
	if err := runHTTP(context.Background(), "127.0.0.1:0"); err == nil {
		t.Fatal("expected runHTTP to refuse a short token")
	}
}

func TestReadyz(t *testing.T) {
	rec := httptest.NewRecorder()
	readyz([]string{"definitely-not-installed-xyz"})(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "definitely-not-installed-xyz") {
		t.Fatalf("missing binary: got %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	readyz([]string{"sh"})(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("present binary: got %d", rec.Code)
	}
}
