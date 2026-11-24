package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai.engineer.runner/internal/service"
	tools2 "ai.engineer.runner/internal/tools"
	"ai.engineer.runner/internal/types"
	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	zl "github.com/rs/zerolog/log"
	"resty.dev/v3"
)

// ---------- types ----------

type ClaimResponse struct {
	Status   bool   `json:"status"`
	RunToken string `json:"run_token"`
}

type ResultPayload struct {
	Tool      string    `json:"tool"`
	RunToken  string    `json:"run_token"`
	StartedAt time.Time `json:"started_at"`
	Data      any       `json:"data"`
}

type Config struct {
	ToolName   string
	ClaimURL   string
	ResultURL  string
	ClaimToken string
	Interval   time.Duration
}

// A tool runner: takes a context, returns data to push.
type runnerFunc func(ctx context.Context, startedAt time.Time) (any, error)

// Registry: add new tools here instead of growing a switch.
var tools = map[string]runnerFunc{
	"nmap": runNmap,
}

// ---------- main ----------

func main() {
	mode := flag.String("mode", "runner", "runner | stdio | http")
	addr := flag.String("addr", "127.0.0.1:8080", "listen address for -mode=http")
	flag.Parse()

	// Everything logs to stderr; in stdio mode stdout carries the MCP protocol.
	log.SetOutput(os.Stderr)
	zl.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).With().Timestamp().Logger()

	if err := godotenv.Load(); err != nil {
		// Not fatal: in Docker/k8s, env vars come from the environment, not a file.
		log.Printf("no .env loaded: %v", err)
	}

	// Cancelled on Ctrl+C or SIGTERM (k8s sends SIGTERM on shutdown).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "stdio":
		if err := service.CheckTrivyConfig(); err != nil {
			log.Fatalf("trivy config: %v", err)
		}
		if err := runStdio(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("mcp stdio: %v", err)
		}
		return
	case "http":
		if err := service.CheckTrivyConfig(); err != nil {
			log.Fatalf("trivy config: %v", err)
		}

		if err := runHTTP(ctx, *addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("mcp http: %v", err)
		}
		return
	case "runner":
		runRunner(ctx)
	default:
		log.Fatalf("unknown -mode %q (want runner, stdio or http)", *mode)
	}
}

// runRunner is the scheduled claim → scan → push loop.
func runRunner(ctx context.Context) {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	run, ok := tools[cfg.ToolName]
	if !ok {
		log.Fatalf("tool %q is not registered", cfg.ToolName)
	}

	client := resty.New().
		SetTimeout(30 * time.Second).
		SetRetryCount(3)
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("close client: %v", err)
		}
	}()

	log.Printf("runner started: tool=%s interval=%s", cfg.ToolName, cfg.Interval)

	tick(ctx, cfg, client, run) // run once immediately, don't wait an hour

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case <-ticker.C:
			tick(ctx, cfg, client, run)
		}
	}
}

// newMCPServer builds the MCP server with all tools registered.
func newMCPServer() (*mcp.Server, []string, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "infra-server",
		Description: "Server to run the infra check",
		Version:     "1.0.0",
	}, nil)

	server.AddReceivingMiddleware(createLoggingMiddleware())

	enabled := enabledTools()
	bins, err := tools2.RegisterAll(server, enabled)
	if err != nil {
		return nil, nil, err
	}
	if k8sEnabled(enabled) {
		addK8sResources(server, tools2.K8sSuite())
	}
	return server, bins, nil
}

// ---------- MCP resources ----------

// k8sEnabled reports whether any k8s tool is enabled. Resources read the
// cluster too, so they follow the same switch.
func k8sEnabled(enabled []string) bool {
	if len(enabled) == 0 {
		return true
	}
	for _, n := range enabled {
		if strings.HasPrefix(n, "k8s_") {
			return true
		}
	}
	return false
}

// addK8sResources registers read-only cluster snapshots. They reuse the k8s
// tools' service functions, clients and K8S_ALLOWED_CONTEXTS.
func addK8sResources(server *mcp.Server, ks *tools2.K8sToolsSuite) {
	nodes := jsonResource(ks, func(ctx context.Context, c *service.K8sClients) (*types.K8sNodeListResult, error) {
		return service.ListK8sNodes(ctx, c.Typed, types.K8sNodeListInput{})
	})
	server.AddResource(&mcp.Resource{
		URI:         "k8s://default/nodes",
		Name:        "k8s-nodes",
		Title:       "Kubernetes nodes (default context)",
		MIMEType:    "application/json",
		Description: "Nodes in the default cluster with health, versions, capacity and pod requests. For filters use k8s_list_nodes.",
	}, nodes)
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "k8s://contexts/{context}/nodes",
		Name:        "k8s-context-nodes",
		Title:       "Kubernetes nodes for a context",
		MIMEType:    "application/json",
		Description: "Same as k8s://default/nodes for a context from k8s_list_contexts (URL-encode the name). Only contexts in K8S_ALLOWED_CONTEXTS work.",
	}, nodes)
}

// contextFromURI returns the context in "k8s://contexts/<name>/...", or ""
// (the default context) for any other URI.
func contextFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Host != "contexts" {
		return ""
	}
	// Split the still-encoded path: names like EKS ARNs contain an encoded "/".
	first, _, _ := strings.Cut(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	name, _ := url.PathUnescape(first)
	return name
}

// jsonResource turns a "fetch data for a context" function into a resource
// handler: resolves the context (allowlist applies), adds the timeout and
// returns the data as JSON. Errors are logged by the logging middleware.
func jsonResource[T any](ks *tools2.K8sToolsSuite, fetch func(ctx context.Context, c *service.K8sClients) (*T, error)) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		src, err := ks.Source()
		if err != nil {
			return nil, err
		}
		c, _, err := src.Clients(contextFromURI(req.Params.URI))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, tools2.K8sTimeout)
		defer cancel()

		data, err := fetch(ctx, c)
		if err != nil {
			return nil, err
		}
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Cacheable: mcp.Cacheable{TTLMs: 30_000, CacheScope: "private"}, // live cluster data
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(b),
			}},
		}, nil
	}
}

// enabledTools reads ENABLED_TOOLS ("nmap_scan,trivy_scan"). Unset or blank
// means every tool; blank entries from stray commas are ignored.
func enabledTools() []string {
	var out []string
	for _, s := range strings.Split(os.Getenv("ENABLED_TOOLS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// runStdio serves MCP over stdin/stdout. This is what Claude Desktop and the
// MCP Inspector use. stdout belongs to the protocol: all logging goes to stderr.
func runStdio(ctx context.Context) error {
	server, _, err := newMCPServer()
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

// bearerAuth rejects requests that don't carry "Authorization: Bearer <token>".
func bearerAuth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Constant-time compare so response timing doesn't leak the token.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readyz(bins []string) http.HandlerFunc {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		var problems []string
		for _, b := range bins {
			if _, err := exec.LookPath(b); err != nil {
				problems = append(problems, "missing binary "+b)
			}
		}
		if srv := os.Getenv("TRIVY_SERVER"); srv != "" {
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, srv+"/healthz", nil)
			resp, err := client.Do(req)
			if err != nil {
				problems = append(problems, "trivy server unreachable")
			} else {
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					problems = append(problems, "trivy server returned "+resp.Status)
				}
			}
		}
		if len(problems) > 0 {
			http.Error(w, strings.Join(problems, "; "), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}
}

// runHTTP serves MCP over Streamable HTTP at addr (POST/GET <addr>/mcp).
func runHTTP(ctx context.Context, addr string) error {
	token := os.Getenv("MCP_AUTH_TOKEN")
	if len(token) < 32 {
		return errors.New("MCP_AUTH_TOKEN must be set (at least 32 characters), e.g. openssl rand -hex 32")
	}

	server, bins, err := newMCPServer()
	if err != nil {
		return err
	}
	mux := http.NewServeMux()

	// Probes stay open: the kubelet calls them without a token.
	mux.HandleFunc("/readyz", readyz(bins))

	mux.Handle("/mcp", bearerAuth(token, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		zl.Info().Str("addr", addr).Msg("MCP HTTP server listening on /mcp")
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func createLoggingMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			start := time.Now()
			sessionID := ""
			if s := req.GetSession(); s != nil {
				sessionID = s.ID()
			}

			result, err := next(ctx, method, req)

			ev := zl.Info()
			if err != nil {
				ev = zl.Error().Err(err)
			}
			ev.Str("session", sessionID).
				Str("method", method).
				Dur("duration", time.Since(start)).
				Msg("mcp request")
			return result, err
		}
	}
}

// ---------- run cycle ----------

func tick(ctx context.Context, cfg Config, c *resty.Client, run runnerFunc) {
	// A run may not take longer than the interval, so runs never pile up.
	ctx, cancel := context.WithTimeout(ctx, cfg.Interval)
	defer cancel()

	if err := execute(ctx, cfg, c, run); err != nil {
		log.Printf("run failed: %v", err) // log and keep going; one bad run shouldn't kill the process
	}
}

func execute(ctx context.Context, cfg Config, c *resty.Client, run runnerFunc) error {
	startedAt := time.Now().UTC()
	log.Printf("checking claim at %s", startedAt.Format(time.RFC3339))

	claim, err := postJSON[ClaimResponse](ctx, c, cfg.ClaimURL, nil, map[string]string{
		"X-CLAIM-TYPE": cfg.ClaimToken,
	})
	if err != nil {
		return fmt.Errorf("check claim: %w", err)
	}
	if !claim.Status {
		log.Println("no claim granted, skipping this run")
		return nil
	}

	data, err := run(ctx, startedAt)
	if err != nil {
		return fmt.Errorf("run %s: %w", cfg.ToolName, err)
	}

	payload := ResultPayload{
		Tool:      cfg.ToolName,
		RunToken:  claim.RunToken,
		StartedAt: startedAt,
		Data:      data,
	}
	if _, err := postJSON[struct{}](ctx, c, cfg.ResultURL, payload, map[string]string{
		"X-RUN-TOKEN": claim.RunToken,
	}); err != nil {
		return fmt.Errorf("push result: %w", err)
	}

	log.Printf("run %s pushed in %s", cfg.ToolName, time.Since(startedAt).Round(time.Millisecond))
	return nil
}

// ---------- tools ----------

func runNmap(ctx context.Context, startedAt time.Time) (any, error) {
	select {
	case res := <-service.RunNmap(startedAt.Format(time.RFC3339Nano)):
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Run, nil // TODO: rename to whatever field holds the NmapRun in your result struct
	case <-ctx.Done():
		return nil, fmt.Errorf("nmap timed out: %w", ctx.Err())
	}
}

// ---------- HTTP helper (generic) ----------

// postJSON sends body as JSON and decodes a JSON response into T.
func postJSON[T any](ctx context.Context, c *resty.Client, url string, body any, headers map[string]string) (*T, error) {
	var out T
	req := c.R().
		SetContext(ctx).
		SetHeaders(headers).
		SetResult(&out)
	if body != nil {
		req.SetBody(body)
	}

	res, err := req.Post(url)
	if err != nil {
		return nil, err
	}
	if !res.IsStatusSuccess() {
		body := res.String()
		if len(body) > 512 {
			body = body[:512] + "…"
		}
		return nil, fmt.Errorf("POST %s: %s: %s", url, res.Status(), strings.TrimSpace(body))
	}
	return &out, nil
}

// ---------- config ----------

func loadConfig() (Config, error) {
	cfg := Config{
		ToolName:   os.Getenv("TOOL_NAME"),
		ClaimURL:   os.Getenv("HUB_URL"),
		ResultURL:  os.Getenv("RESULT_URL"),
		ClaimToken: os.Getenv("CLAIM_TOKEN"),
		Interval:   time.Hour,
	}

	for name, v := range map[string]string{
		"TOOL_NAME": cfg.ToolName, "HUB_URL": cfg.ClaimURL,
		"RESULT_URL": cfg.ResultURL, "CLAIM_TOKEN": cfg.ClaimToken,
	} {
		if v == "" {
			return cfg, fmt.Errorf("%s is required", name)
		}
	}

	if s := os.Getenv("RUN_INTERVAL"); s != "" {
		d, err := time.ParseDuration(s) // e.g. "1h", "15m"
		if err != nil {
			return cfg, fmt.Errorf("RUN_INTERVAL: %w", err)
		}
		cfg.Interval = d
	}
	return cfg, nil
}
