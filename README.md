# infra-mcp-gateway

[![CI](https://github.com/SwanHtetAungPhyo/infra-mcp-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/SwanHtetAungPhyo/infra-mcp-gateway/actions/workflows/ci.yml)

An [MCP](https://modelcontextprotocol.io) gateway that gives an AI agent safe, validated access to infrastructure security scanners and a read-only view of a Kubernetes cluster.

The agent decides **what** to look at. The gateway decides **what is allowed**, runs the scan and returns compact, typed findings the model can reason about. The agent itself never gets scanner binaries, network reach, cluster credentials or other secrets. It only gets this server's URL and a token.

```
 infra agent ──tools/call + Bearer──▶ infra-mcp-gateway ──▶ nmap · nmap-formatter · trivy ──▶ allowed targets
 (Claude)                               auth · scope · limits                               hosts · images · git repos
                                                         └──▶ Kubernetes API (read-only) ──▶ nodes · workloads · pods · services · CRDs

 runner (cron) ──claim / push──▶ your hub        same binary, no MCP: a scheduled baseline scan
```

## Tools

### `nmap_scan`

Port and service discovery on hosts inside `NMAP_ALLOWED_CIDRS`.

```json
{ "targets": ["10.0.1.0/24"], "ports": "1-1024", "service_detection": true }
```

Returns one summary line plus each host's open ports:

```json
{ "summary": "1 of 1 hosts up, 2 open ports, 0.03s",
  "hosts": [{ "address": "10.0.1.5", "state": "up",
              "open_ports": [{ "port": 22, "protocol": "tcp", "service": "ssh" }] }] }
```

### `trivy_scan`

Vulnerabilities, leaked secrets and IaC misconfigurations.

| `scan_type` | `target` | Finds | Default scanners |
|---|---|---|---|
| `repo` | `https://` git URL, optional `ref` | dependency CVEs, secrets, Dockerfile / Terraform / Kubernetes / Helm misconfigurations | `vuln,secret,misconfig` |
| `image` | `alpine:3.19`, `registry/app@sha256:…` | OS and language package CVEs, secrets | `vuln,secret` |
| `fs` | absolute path under `TRIVY_ALLOWED_ROOTS` | dependency CVEs, secrets | `vuln,secret` |
| `config` | absolute path under `TRIVY_ALLOWED_ROOTS` | IaC misconfigurations | n/a |

```json
{ "scan_type": "repo", "target": "https://github.com/your-org/infra",
  "ref": "release/1.4", "severities": ["HIGH", "CRITICAL"] }
```

- `ref` takes a branch, a tag (with `"ref_type": "tag"`) or a full 40-character commit SHA.
- Findings come back sorted by severity, with fix versions and remediation.
- **Secret values are never returned**, only the rule, file and line.

### `k8s_list_contexts`

The Kubernetes contexts (clusters) the agent may target. Every k8s tool takes an optional `context` argument; without it the gateway's default context is used.

```json
{ "check_connectivity": true }
```

```json
{ "source": "kubeconfig", "default": "kind-dev",
  "summary": "2 contexts available from kubeconfig, default kind-dev; 1 of 2 reachable; 3 more not allowed by K8S_ALLOWED_CONTEXTS",
  "hidden": 3,
  "contexts": [
    { "name": "kind-dev", "default": true, "cluster": "kind-dev", "server": "https://127.0.0.1:6443",
      "user": "kind-dev", "auth": "client-certificate", "reachable": true, "version": "v1.33.1" },
    { "name": "prod", "cluster": "prod", "server": "https://prod.example.com", "user": "prod-ro",
      "namespace": "payments", "auth": "exec:aws", "reachable": false, "error": "…connection refused" }] }
```

- `auth` says how the context logs in (`exec:aws`, `token`, `client-certificate`, `auth-provider:<name>`, `service-account`…). Tokens, keys, certificates and exec arguments are never returned.
- `check_connectivity` asks each API server for its version, 4 at a time with a 5 s timeout each.
- `name_contains` narrows the list, e.g. `prod`.
- Contexts outside `K8S_ALLOWED_CONTEXTS` are only counted in `hidden`. Calling a tool with one gets the same error as an unknown name, so names don't leak.
- `namespace` is the kubeconfig default for kubectl; the k8s tools ignore it and treat an empty `namespace` argument as all namespaces.

### `k8s_list_images`

Container images used by pods in the cluster, read from the Kubernetes API (no binaries needed). With no arguments it lists every unique image across all namespaces, the most widely used first.

| Argument | Effect |
|---|---|
| `context` | Context from `k8s_list_contexts`; default is the gateway's default |
| `namespace` | One namespace; empty means all |
| `label_selector` | e.g. `app=api,tier!=cache` |
| `field_selector` | e.g. `status.phase=Running`, `spec.nodeName=node-1` |
| `image_contains` | Only images whose reference contains this text, e.g. `ghcr.io/acme` |
| `container_kind` | `container`, `init` or `ephemeral`; default all |
| `group_by` | `image` (default): one row per unique image. `pod`: each pod with its containers, readiness and restarts |
| `max_results` | 1–1000, default 200 |

```json
{ "namespace": "prod", "field_selector": "status.phase=Running" }
```

```json
{ "summary": "2 unique images across 3 pods in namespace prod; 1 use :latest or no tag",
  "pods_scanned": 3, "total_images": 2,
  "images": [{ "image": "ghcr.io/acme/api:1.2.0", "registry": "ghcr.io", "repository": "acme/api", "tag": "1.2.0",
               "running_digests": ["sha256:…"], "pods": 2, "containers": 2, "kinds": ["container"],
               "namespaces": ["prod"], "workloads": ["prod/Deployment/api"] }] }
```

- `running_digests` are the digests the kubelet actually pulled, so a moving tag shows up as several digests.
- Workloads are named `namespace/Kind/name`. Pods from a ReplicaSet are reported as their Deployment.
- Selectors, namespace and enums are checked before the API is called, so bad input comes back as a clear tool error.
- A natural next step is `trivy_scan` with `scan_type: image` on each image it returns.

### `k8s_list_namespaces`

Namespaces with their phase, age, labels and pod health. With no arguments it lists every namespace, sorted by name.

| Argument | Effect |
|---|---|
| `context` | Context from `k8s_list_contexts`; default is the gateway's default |
| `label_selector` | Selector on namespace labels, e.g. `team=payments` |
| `name_contains` | Only namespaces whose name contains this text, e.g. `prod` |
| `phase` | `Active` or `Terminating`; default both |
| `exclude_system` | Hide `kube-system`, `kube-public`, `kube-node-lease` and `default` |
| `skip_pod_counts` | Don't list pods: faster on very large clusters, but `pods` is left out |
| `max_results` | 1–1000, default 200 |

```json
{ "exclude_system": true }
```

```json
{ "summary": "3 namespaces, 12 pods; failing, crash-looping or not-ready pods in: prod",
  "total": 3, "pods_scanned": 12,
  "namespaces": [{ "name": "prod", "phase": "Active", "age": "41d", "created_at": "2026-10-13T09:12:00Z",
                   "labels": { "team": "payments" },
                   "pods": { "total": 8, "running": 7, "pending": 1, "not_ready": 1, "crash_looping": 1 } }] }
```

- The summary names every namespace with failed, crash-looping or not-ready pods, so the agent knows where to look next.
- `kubernetes.io/metadata.name` is left out of `labels`, since it always equals the name.
- Pod counts come from one paged, cluster-wide pod list (500 per page, up to 20,000), not one call per namespace.
- A natural next step is `k8s_list_images` with `namespace` set to a namespace it returns.

### Kubernetes inventory

More read-only tools with the same pattern: optional `context`, filters, `max_results` (1–1000, default 200), paging through the API, and a `summary` that names whatever needs attention. Bad input is rejected before the API is called.

| Tool | Returns | Main filters | Summary flags |
|---|---|---|---|
| `k8s_list_nodes` | Ready/NotReady, cordoned, roles, taints, pressure conditions, kubelet/OS/kernel/runtime, IPs, zone, instance type, CPU/memory capacity vs allocatable, pod requests per node with % | `label_selector`, `role`, `status` (`ready`/`not_ready`/`cordoned`), `name_contains`, `skip_pod_counts`, `include_labels` | unhealthy nodes, kubelet version skew, cluster-wide request % |
| `k8s_list_workloads` | Deployments, StatefulSets, DaemonSets, CronJobs, Jobs: status, desired/ready/updated/available, images, why it isn't healthy; CronJob schedule and last runs | `namespace`, `kinds`, `label_selector`, `name_contains`, `unhealthy_only`, `include_cronjob_jobs` | degraded, failed and progressing workloads |
| `k8s_list_pods` | kubectl-style status (`CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`, `Init:…`, `Evicted`), ready, restarts, last termination (reason, exit code, time), node, workload, IP, QoS | `namespace`, `label_selector`, `field_selector`, `node`, `name_contains`, `problems_only` | problem pods |
| `k8s_list_services` | type, cluster IP, external IPs/hostnames, ports (`80:30080/TCP->8080`), selector, ready endpoints | `namespace`, `type`, `exposed_only`, `label_selector`, `name_contains` | services exposed outside the cluster, services with no ready endpoints |
| `k8s_list_ingresses` | class, hosts, rules (`host/path -> service:port`), TLS hosts and secret names, load balancer addresses | `namespace`, `host_contains`, `label_selector` | hosts served without TLS |
| `k8s_get_resource` | any object, CRDs included, as YAML; without `name`, a list of names | `kind` (`Deployment`, `svc`, `certificates.cert-manager.io`), `api_version`, `name`, `namespace`, `label_selector` | what was redacted |
| `k8s_list_crds` | CRDs: group, kind, scope, versions (storage / not served / deprecated), short names, established | `group_contains` | CRDs that aren't established |
| `k8s_list_helm_releases` | Helm 3 releases: latest revision, status, revisions kept, last update | `namespace`, `name_contains`, `problems_only` | failed and stuck `pending-*` releases |

```json
{ "tool": "k8s_list_pods", "arguments": { "namespace": "prod", "problems_only": true } }
```

```json
{ "summary": "1 pods in namespace prod (1 CrashLoopBackOff); 1 need attention: prod/api-7f9c-x2 (CrashLoopBackOff)",
  "pods": [{ "namespace": "prod", "name": "api-7f9c-x2", "workload": "prod/Deployment/api", "node": "w2",
             "status": "CrashLoopBackOff", "problem": true, "ready": "0/1", "restarts": 7,
             "last_termination": { "container": "api", "reason": "OOMKilled", "exit_code": 137 } }] }
```

What these tools never return:

- **`k8s_get_resource`** drops `managedFields`, and replaces with `<redacted>` the values of Secret `data`/`stringData`, every literal `env[].value` (in any pod template), and the `kubectl.kubernetes.io/last-applied-configuration` annotation, which can hold a full copy of a Secret. `valueFrom` references are kept. Output is capped at 64 KiB. Listing without `name` uses the metadata API, so object bodies never leave the API server.
- **`k8s_list_helm_releases`** reads only the labels on Helm's release Secrets through the metadata API. Chart contents and values are never fetched. Releases stored in ConfigMaps (Helm's `configmap` driver) aren't listed.
- A workload kind or the EndpointSlices that the identity can't list are reported in `errors` / `warnings` instead of failing the call.

Config is loaded on the first k8s call: the in-cluster service account first (one context, `in-cluster`), then `$KUBECONFIG`, then `~/.kube/config`. The default context is `K8S_CONTEXT`, else kubeconfig's `current-context`. Only contexts in `K8S_ALLOWED_CONTEXTS` can be used; when it's unset, that's just the default context. Each context gets its own client, created when first used and then reused. Kubeconfig is read once, so restart the gateway after editing it. The server starts without a cluster; calls just return an error.

## Safety model

The rules live in code, not in a prompt. A confused agent or a prompt injection can't widen them.

| Question | Enforced by |
|---|---|
| Who may call? | `Authorization: Bearer` checked against `MCP_AUTH_TOKEN` (constant-time). The server won't start in http mode without a 32+ character token |
| What may be scanned? | `NMAP_ALLOWED_CIDRS` (fails closed when unset, at most `/24` per target, hostnames resolved once and the checked IPs passed to nmap), `TRIVY_ALLOWED_ROOTS` (symlinks resolved, never `/`), `K8S_ALLOWED_CONTEXTS` (only the default context when unset), `https://` repos without credentials, the Kubernetes RBAC granted to the gateway's identity, `ENABLED_TOOLS` |
| What may a request contain? | JSON schemas checked by the SDK, then strict validators. The binary and its flags are fixed; targets and refs come after `--` and can't start with `-` |
| How much may it use? | Per-tool timeouts (nmap 5 min, trivy 10 min, k8s 2 min), `MAX_CONCURRENT_SCANS`, a 64 MiB output cap; a timeout kills the scanner's whole process group. The k8s tools page 500 items at a time, stop at 20,000 pods and are rate-limited to 20 QPS |
| What comes back? | Compact typed results; secret matches stripped; only pod images, pod status and namespace metadata from Kubernetes, never env vars or Secrets |

All tools are read-only. Anything that changes infrastructure would be a separate tool, marked destructive and gated behind human approval.

## Modes

One binary, three modes:

| Mode | Use | Transport |
|---|---|---|
| `-mode=http` | the gateway (Kubernetes, shared use) | Streamable HTTP, stateless, `POST /mcp`; probes at `/healthz` and `/readyz` |
| `-mode=stdio` | local clients: Claude Desktop, MCP Inspector | stdin/stdout |
| `-mode=runner` (default) | scheduled baseline: claim from the hub, full nmap scan of localhost, push the result | outbound HTTPS to `HUB_URL` / `RESULT_URL` |

`/readyz` returns 503 until every binary the enabled tools need is on `PATH`, and until `TRIVY_SERVER` answers when that's set.

## Configuration

Copy `.env.example` to `.env` for local runs. In Kubernetes, set these from ConfigMaps and Secrets.

| Variable | Mode | Purpose |
|---|---|---|
| `MCP_AUTH_TOKEN` | http | Required. Bearer token clients must send (`openssl rand -hex 32`) |
| `NMAP_ALLOWED_CIDRS` | http, stdio, runner | Comma-separated IPv4 ranges nmap may scan. Unset means nothing |
| `TRIVY_ALLOWED_ROOTS` | http, stdio | Colon-separated folders `fs`/`config` scans may read. Default `$HOME`; `/` is refused |
| `ENABLED_TOOLS` | http, stdio | e.g. `trivy_scan`. Empty means all tools |
| `MAX_CONCURRENT_SCANS` | all | Scans running at once. Default `2` |
| `TRIVY_SERVER` | http | Optional Trivy server that `/readyz` checks |
| `GITHUB_TOKEN` / `GITLAB_TOKEN` | http, stdio | Read-only token Trivy uses to clone private repos. The agent never sees it |
| `KUBECONFIG` | http, stdio | Kubeconfig for the k8s tools outside a cluster. Default `~/.kube/config`; ignored in-cluster |
| `K8S_CONTEXT` | http, stdio | Default kubeconfig context instead of `current-context` |
| `K8S_ALLOWED_CONTEXTS` | http, stdio | Comma-separated contexts the agent may pass as `context`, or `*` for all. Unset means only the default context |
| `TOOL_NAME`, `HUB_URL`, `RESULT_URL`, `CLAIM_TOKEN`, `RUN_INTERVAL` | runner | Hub endpoints and schedule (default `1h`) |

## Run locally

Requirements: Go 1.26.11+, [nmap](https://nmap.org), [nmap-formatter](https://github.com/vdjagilev/nmap-formatter) v3, [Trivy](https://trivy.dev).

```sh
brew install nmap trivy
go install github.com/vdjagilev/nmap-formatter/v3@v3.1.5
go build -o bin/runner ./cmd
```

**HTTP gateway**

```sh
export MCP_AUTH_TOKEN=$(openssl rand -hex 32) NMAP_ALLOWED_CIDRS=127.0.0.1/32
./bin/runner -mode=http -addr=127.0.0.1:8080

curl -s localhost:8080/readyz
curl -s -X POST localhost:8080/mcp \
  -H "Authorization: Bearer $MCP_AUTH_TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

**MCP Inspector**

```sh
NMAP_ALLOWED_CIDRS=127.0.0.1/32 npx @modelcontextprotocol/inspector ./bin/runner -mode=stdio
```

**Claude Desktop** (`~/Library/Application Support/Claude/claude_desktop_config.json`)

```json
{
  "mcpServers": {
    "infra-mcp-gateway": {
      "command": "/absolute/path/to/bin/runner",
      "args": ["-mode=stdio"],
      "env": {
        "PATH": "/opt/homebrew/bin:/Users/you/go/bin:/usr/bin:/bin",
        "NMAP_ALLOWED_CIDRS": "127.0.0.1/32"
      }
    }
  }
}
```

Claude Desktop doesn't load your shell's `PATH`, so set it explicitly or the scanners won't be found. For the k8s tools, also set `KUBECONFIG` if your kubeconfig isn't at `~/.kube/config`, and make sure any exec credential plugin it uses (`aws`, `gcloud`, `kubelogin`) is on that `PATH`.

## Deploying to Kubernetes

The target layout, namespace `infra-agent`:

- **Gateway:** a Deployment running `-mode=http -addr=:8080`, 2+ replicas (the HTTP handler is stateless), behind a ClusterIP Service with no public Ingress.
- **Agent:** a separate workload (Claude Agent SDK) that calls `http://<service>.infra-agent.svc:8080/mcp` with the token from a Secret.
- **Runner:** a CronJob running `-mode=runner`.
- **Pod security:** `runAsNonRoot`, `readOnlyRootFilesystem` with an `emptyDir` at `/tmp`, `capabilities: drop: [ALL]` (nmap connect scans need no raw sockets), seccomp `RuntimeDefault`.
- **RBAC for the k8s tools:** a dedicated ServiceAccount bound to a ClusterRole with only `get`/`list` on the kinds the tools read, plus a few common kinds for `k8s_get_resource` (see below; trim it to taste). The cluster decides what the gateway can see: `k8s_get_resource` can only read kinds you grant, so leave `secrets` out of it. Nodes, namespaces and CRDs are cluster-scoped, so their tools need the ClusterRole. If the agent should only see some namespaces, turn those tools off in `ENABLED_TOOLS`, use namespaced Roles, and have the agent always pass `namespace`.
- **Helm releases (optional):** Helm stores releases in Secrets, and Kubernetes RBAC can't grant "labels only": `list` on `secrets` also allows reading their data. The gateway only asks the metadata API, but the ServiceAccount token itself could read the values. Grant it only where you need `k8s_list_helm_releases`, ideally per namespace, or turn the tool off.
- **NetworkPolicy:** ingress only from the agent; egress only to `NMAP_ALLOWED_CIDRS`, the Kubernetes API server, your registries and your git host. This is the same scope the code enforces, applied a second time at the network level.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: infra-mcp-gateway-k8s-read }
rules:
  - apiGroups: [""]
    resources: ["pods", "namespaces", "nodes", "services", "configmaps", "serviceaccounts", "persistentvolumeclaims", "persistentvolumes", "events"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "daemonsets", "replicasets"]
    verbs: ["get", "list"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["get", "list"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses", "ingressclasses", "networkpolicies"]
    verbs: ["get", "list"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list"]
  - apiGroups: ["apiextensions.k8s.io"]
    resources: ["customresourcedefinitions"]
    verbs: ["get", "list"]
  # Add the custom resources k8s_get_resource should read, e.g.:
  # - apiGroups: ["cert-manager.io"]
  #   resources: ["certificates", "issuers"]
  #   verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: infra-mcp-gateway-k8s-read }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: infra-mcp-gateway-k8s-read }
subjects:
  - { kind: ServiceAccount, name: infra-mcp-gateway, namespace: infra-agent }
---
# Optional, for k8s_list_helm_releases in one namespace (see the note above).
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: infra-mcp-gateway-helm-releases, namespace: prod }
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: infra-mcp-gateway-helm-releases, namespace: prod }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: infra-mcp-gateway-helm-releases }
subjects:
  - { kind: ServiceAccount, name: infra-mcp-gateway, namespace: infra-agent }
```

Probes:

```yaml
livenessProbe:  { httpGet: { path: /healthz, port: 8080 }, periodSeconds: 10 }
readinessProbe: { httpGet: { path: /readyz,  port: 8080 }, periodSeconds: 5, failureThreshold: 2 }
```

Dockerfiles and Terraform live in git, not in the cluster, so the gateway scans them with `scan_type: repo`. Trivy clones into a temp folder inside the pod and deletes it afterwards. Checking those files at pull-request time belongs in CI (`trivy config .`). The gateway's job is watching main branches, registries and the live cluster.

## Development

```sh
go vet ./...
go test ./...                                     # unit and local integration tests
TRIVY_NETWORK_TESTS=1 go test ./internal/service  # also clones a small public repo
```

Tests that need `nmap` or `trivy` skip themselves when the binary isn't installed. The k8s tool tests use client-go's fake clientset, so they need no cluster.

CI (`.github/workflows/ci.yml`) runs on every push and pull request:

- **Build and test:** gofmt, `go vet`, build, and `go test -race` with nmap, nmap-formatter and Trivy installed at pinned versions (the Trivy download is checked against its SHA-256).
- **Security scans:** `govulncheck`, and a Trivy self-scan of this repo that fails on HIGH or CRITICAL findings.

Actions are pinned to commit SHAs, and Dependabot keeps them and the Go modules current.

### Layout

```
cmd/main.go                 mode switch, HTTP server, bearer auth, probes, runner loop
internal/tools/             MCP tool definitions, input schemas, registry; k8s_inventory_tools.go shares one handler for the inventory tools
internal/service/           validation, scope checks, scanner invocation, result shaping
  command.go                RunCommand: slots, output cap, process-group kill
  nmap.go                   ScanNmap, resolveTargets (CIDR scope), SummarizeNmap
  trivy.go                  ScanTrivy, allowed roots, refs, default scanners
  k8s.go                    ListK8sImages, ListK8sNameSpace, ParseImageRef
  k8s_contexts.go           K8sSource: kubeconfig/in-cluster contexts, allowlist, per-context typed/dynamic/metadata clients
  k8s_nodes.go              ListK8sNodes: health, capacity, pod requests per node
  k8s_workloads.go          ListK8sWorkloads: Deployment/StatefulSet/DaemonSet/CronJob/Job status
  k8s_pods.go               ListK8sPods: kubectl-style status, restarts, last termination
  k8s_network.go            ListK8sServices (with EndpointSlices), ListK8sIngresses
  k8s_resource.go           GetK8sResource (discovery + dynamic, redaction), ListK8sCRDs, ListK8sHelmReleases
internal/types/             input and output structs shared by every layer
```

### Adding a tool

1. Create `internal/tools/<name>.go` with an `mcp.Tool` and a typed handler.
2. Put validation and execution in `internal/service/<name>.go`, running binaries through `RunCommand` (or calling an API client, as `k8s.go` does).
3. Add one entry to `registry` in `internal/tools/registry.go`, listing the binaries it needs so `/readyz` checks them.
