# Roadmap: infra-mcp-gateway → multi-cloud infra MCP server

Goal: one MCP server that lets an AI agent **inspect, audit and (with approval) operate** Kubernetes, DigitalOcean, AWS and other providers, with the same rules the gateway already has: scope enforced in code, typed compact output, read-only by default.

Tool naming: `<provider>_<resource>_<verb>`, e.g. `k8s_pods_list`, `aws_ec2_instances_list`, `do_droplets_list`. Existing tools keep their names (`nmap_scan`, `trivy_scan`, `k8s_list_images`).

Legend: **P0** must have before adding more providers · **P1** core value · **P2** nice to have

---

## Phase 0 — Foundation (do first)

Everything else depends on these. Without them each new provider re-invents auth, limits and output shaping.

### Architecture
- [ ] **P0** Provider package layout: `internal/providers/<k8s|aws|do|gcp|azure>/` holding the client, scope checks and tools for each provider. `internal/service` keeps the shared pieces (command runner, limits, findings)
- [ ] **P0** Shared lazy client helper (the `sync.Once` pattern from `K8sToolsSuite`) so a missing credential never stops the server from starting
- [ ] **P0** Registry v2: entries carry `provider`, `bins`, `required env`, and a `ready()` check; `ENABLED_TOOLS` accepts globs (`k8s_*`, `aws_*`) and `ENABLED_PROVIDERS=k8s,aws`
- [ ] **P0** `/readyz` per provider: optional API reachability check (k8s `/version`, AWS `sts:GetCallerIdentity`, DO `account`), reported separately so one provider being down doesn't fail the whole pod (`READYZ_REQUIRE=k8s,aws`)
- [ ] **P0** A `MAX_CONCURRENT_SCANS`-style limit for API tools as well, not only `RunCommand` (today `k8s_list_images` skips it)
- [ ] **P1** Runner mode: register `trivy` and `k8s_list_images` in `cmd/main.go`'s `tools` map (only `nmap` today), or reuse the MCP registry so scheduled baselines can run any tool

### Shared output contracts
- [ ] **P0** Common `Finding` type (`provider, resource_id, kind, severity, title, evidence, remediation, url`) reused by Trivy, k8s audit, AWS/DO posture checks, so the agent can merge and rank results
- [ ] **P0** Common `ResourceRef` (`provider/account/region/kind/name/id`) used in every result
- [ ] **P0** Pagination contract: `max_results` + `cursor` in input, `next_cursor` + `truncated` in output, for every list tool
- [ ] **P0** Output budget: hard cap on result size (e.g. 64 KiB JSON), with `truncated` and a hint to narrow the query. Add a test that fails if a tool returns more
- [ ] **P1** `summary` line on every result (as nmap/trivy/k8s already do)

### Safety
- [ ] **P0** Scope allowlists per provider, fail closed: `K8S_ALLOWED_NAMESPACES`, `K8S_ALLOWED_CONTEXTS`, `AWS_ALLOWED_ACCOUNTS`, `AWS_ALLOWED_REGIONS`, `DO_ALLOWED_PROJECTS`
- [ ] **P0** Redaction layer applied to all output: never return Secret data, env values, user-data, connection strings, access keys; mask anything matching key/token patterns
- [ ] **P0** Audit log: one structured line per tool call (caller session, tool, args with redaction, resources touched, duration, result size). Optional sink to file/stdout JSON
- [ ] **P0** Write-tool policy: `DestructiveHint`/`IdempotentHint` annotations, disabled unless `ALLOW_WRITE_TOOLS=true`, plus `dry_run` default true
- [ ] **P1** Human approval for writes via MCP elicitation (confirm with a summary of exactly what will change), falling back to refusing when the client doesn't support it
- [ ] **P1** Per-caller tokens with scopes (read / write / provider list) instead of a single `MCP_AUTH_TOKEN`
- [ ] **P2** OAuth 2.1 auth for the HTTP transport (per MCP auth spec)

---

## Phase 1 — Kubernetes (finish what `k8s_list_images` started)

Read-only RBAC: `get/list/watch` on the resources below, never `secrets` data.

### Inventory
- [ ] **P1** `k8s_contexts_list` — kubeconfig contexts the server may use (respecting `K8S_ALLOWED_CONTEXTS`); add `context` input to every k8s tool
- [x] **P1** `k8s_list_namespaces` — with labels, phase, age, pod health counts
- [ ] **P1** `k8s_nodes_list` — version, OS/arch, capacity vs allocatable, conditions, taints
- [ ] **P1** `k8s_workloads_list` — Deployments, StatefulSets, DaemonSets, Jobs, CronJobs with desired/ready replicas and images
- [ ] **P1** `k8s_pods_list` — phase, restarts, last termination reason (CrashLoopBackOff, OOMKilled), node
- [ ] **P1** `k8s_services_list` / `k8s_ingresses_list` — type, ports, external IPs/hosts, TLS
- [ ] **P1** `k8s_resource_get` — generic get for any kind (incl. CRDs) via the dynamic client, returned as trimmed YAML (no `managedFields`, Secret data stripped)
- [ ] **P2** `k8s_helm_releases_list` — from Helm release secrets (metadata only)
- [ ] **P2** `k8s_crds_list`

### Troubleshooting
- [ ] **P1** `k8s_events_list` — warnings first, filter by namespace/object
- [ ] **P1** `k8s_pod_logs` — tail N lines / since duration, container select, previous container, size cap, redaction
- [ ] **P1** `k8s_diagnose_workload` — one call that combines status, events, recent logs and owner chain for a failing workload
- [ ] **P2** `k8s_top` — CPU/memory from metrics-server

### Security / posture
- [ ] **P1** `k8s_pod_security_audit` — privileged, hostPath, hostNetwork, runAsRoot, missing limits, `:latest`, no probes → `Finding`s
- [ ] **P1** `k8s_rbac_audit` — cluster-admin bindings, wildcard verbs, who can read secrets / exec into pods
- [ ] **P1** `k8s_network_policy_audit` — namespaces with no NetworkPolicy, pods not selected by any
- [ ] **P1** `k8s_images_scan` — `k8s_list_images` → `trivy_scan image` for each unique image, deduped by digest, with a per-call image cap
- [ ] **P2** Trivy `k8s` mode (`trivy k8s`) as a cluster-wide scan option

### Operations (Phase 4 rules apply)
- [ ] **P2** `k8s_rollout_restart`, `k8s_scale`, `k8s_rollout_undo`, `k8s_cordon` / `k8s_uncordon` — dry-run diff first, approval required

---

## Phase 2 — AWS

SDK: `aws-sdk-go-v2`. Credentials: default chain (IRSA / EKS Pod Identity in cluster, SSO/profile locally), `AWS_ASSUME_ROLE_ARN` per account, region list from `AWS_ALLOWED_REGIONS`. IAM policy: `ReadOnlyAccess` or `SecurityAudit`, never admin.

### Identity & setup
- [ ] **P0** `aws_whoami` — `sts:GetCallerIdentity`, account alias, allowed regions
- [ ] **P1** Multi-account: `account` input mapped to a role ARN from config; fan-out across regions with a concurrency limit

### Inventory
- [ ] **P1** `aws_ec2_instances_list` — state, type, AMI, public IP, SGs, tags (no user-data)
- [ ] **P1** `aws_vpcs_list` / `aws_subnets_list`
- [ ] **P1** `aws_s3_buckets_list` — region, public access block, encryption, versioning
- [ ] **P1** `aws_eks_clusters_list` + `aws_eks_kubeconfig` — feed EKS clusters into the k8s tools without a local kubeconfig
- [ ] **P1** `aws_rds_instances_list` — engine/version, public, encryption, backups
- [ ] **P1** `aws_ecr_repos_list` / `aws_ecr_image_findings` — scan results for pushed images
- [ ] **P1** `aws_lambda_functions_list` — runtime (flag deprecated runtimes), role, env var **names only**
- [ ] **P2** `aws_elb_list`, `aws_route53_zones_list`, `aws_cloudfront_list`, `aws_ecs_services_list`

### Security / posture
- [ ] **P1** `aws_security_groups_audit` — rules open to `0.0.0.0/0` / `::/0` on sensitive ports
- [ ] **P1** `aws_iam_audit` — root MFA, users without MFA, access keys older than N days, admin policies, unused roles
- [ ] **P1** `aws_s3_public_audit` — public ACLs/policies, missing block-public-access
- [ ] **P1** `aws_securityhub_findings` / `aws_guardduty_findings` — pull existing findings into the common `Finding` type
- [ ] **P2** `aws_cloudtrail_lookup` — who changed what, filtered by resource/user/time
- [ ] **P2** `aws_config_noncompliant` — AWS Config rule results

### Cost
- [ ] **P2** `aws_cost_summary` — Cost Explorer by service/month, top movers
- [ ] **P2** `aws_idle_resources` — unattached EBS, unused EIPs, stopped instances with volumes

---

## Phase 3 — DigitalOcean

SDK: `github.com/digitalocean/godo`. Credential: `DIGITALOCEAN_TOKEN` (read-scoped custom token). Scope: `DO_ALLOWED_PROJECTS`.

- [ ] **P0** `do_account_info` — account, team, droplet limit, balance
- [ ] **P1** `do_droplets_list` — region, size, image, IPs, tags, backups enabled
- [ ] **P1** `do_firewalls_audit` — inbound rules open to all, droplets without a firewall
- [ ] **P1** `do_kubernetes_clusters_list` + `do_kubernetes_kubeconfig` — feed DOKS clusters into the k8s tools
- [ ] **P1** `do_databases_list` — engine/version, trusted sources (flag "anywhere"), maintenance window
- [ ] **P1** `do_registry_repos_list` — images and tags in DOCR (→ `trivy_scan image`)
- [ ] **P1** `do_spaces_list` — via the S3-compatible API, public ACL check
- [ ] **P2** `do_load_balancers_list`, `do_domains_list`, `do_volumes_list` (unattached), `do_apps_list` (App Platform)
- [ ] **P2** `do_billing_summary` — balance, month-to-date, invoices

---

## Phase 4 — More providers (same pattern)

- [ ] **P2** GCP — `gcp_compute_instances_list`, `gcp_gke_clusters_list` (+ kubeconfig), `gcp_iam_audit`, `gcp_firewall_audit`, GCS public buckets. Workload Identity / ADC
- [ ] **P2** Azure — VMs, AKS, NSG audit, storage public access, Defender findings. Managed identity
- [ ] **P2** Cloudflare — zones, DNS records, WAF/TLS settings
- [ ] **P2** Terraform — `tf_state_resources` (read state from S3/GCS/Spaces backends), drift vs live (`terraform plan -refresh-only` via `RunCommand`)
- [ ] **P2** GitHub — repo security settings, Dependabot/code-scanning alerts (pairs with `trivy_scan repo`)
- [ ] **P2** Hetzner / Linode / Vultr if needed

---

## Phase 5 — Cross-provider tools and MCP features

- [ ] **P1** `infra_inventory` — one call listing clusters, VMs, databases, buckets across all enabled providers as `ResourceRef`s
- [ ] **P1** `infra_public_exposure` — everything reachable from the internet (public IPs, LBs, open SGs/firewalls, public buckets, Ingresses) → optional `nmap_scan` on the allowed subset
- [ ] **P1** `infra_findings` — run every enabled audit, merge into ranked `Finding`s
- [ ] **P1** MCP **resources**: `infra://inventory`, `k8s://<context>/namespaces`, `aws://<account>/regions` so clients can browse without tool calls
- [ ] **P1** MCP **prompts**: "audit this cluster", "why is this workload failing", "what is exposed to the internet", "monthly cost review"
- [ ] **P2** Progress notifications for long fan-outs (multi-region, image scans)
- [ ] **P2** Result caching with short TTL per provider to cut API calls during one agent session

---

## Phase 6 — Write operations (only after Phase 0 safety is done)

- [ ] **P2** Every write tool: `dry_run` default, diff/plan in output, approval via elicitation, audit log entry, `DestructiveHint: true`
- [ ] **P2** k8s: restart, scale, rollback, cordon/drain
- [ ] **P2** AWS: stop/start instance, revoke an SG rule, block public access on a bucket
- [ ] **P2** DO: power off/on droplet, add firewall rule, resize
- [ ] **P2** Separate deployment / token for write tools so a read-only agent can never reach them

---

## Phase 7 — Testing, packaging and operations

### Testing
- [ ] **P0** Fakes per provider: client-go fake (done for k8s), `httptest` server for godo, LocalStack or `aws-sdk-go-v2` middleware stubs for AWS
- [ ] **P0** Schema tests: every tool's input schema has descriptions and enums; bad input returns `IsError`, never a protocol error
- [ ] **P1** Golden-file tests for output shape and size
- [ ] **P1** Redaction tests: seeded secrets must never appear in any output
- [ ] **P2** Integration job against kind cluster in CI; optional nightly job against a sandbox AWS/DO account

### Packaging
- [ ] **P0** `Dockerfile` (distroless/nonroot, nmap + nmap-formatter + trivy pinned) — not in the repo yet
- [ ] **P1** Helm chart: Deployment, Service, ServiceAccount, read-only ClusterRole, NetworkPolicy, IRSA/Workload Identity annotations, Secrets for tokens
- [ ] **P1** Release workflow: multi-arch image, SBOM, cosign signature, Trivy scan of the image
- [ ] **P2** Publish to the MCP registry

### Observability
- [ ] **P1** Prometheus `/metrics`: calls, errors, latency and result size per tool/provider, upstream API errors and throttling
- [ ] **P2** OpenTelemetry traces across tool → provider API calls

### Docs
- [ ] **P1** README section per provider: credentials, minimal IAM/RBAC policy, env vars, example calls
- [ ] **P1** `docs/adding-a-provider.md`
- [ ] **P2** Threat model doc (prompt injection via resource names/logs, over-broad credentials, data exfiltration through output)

---

## Suggested order

1. Phase 0 (registry v2, shared `Finding`/pagination/output cap, allowlists, redaction, audit log, Dockerfile)
2. Phase 1 Kubernetes inventory + troubleshooting + posture
3. Phase 3 DigitalOcean (smaller API, quick win; DOKS reuses the k8s tools)
4. Phase 2 AWS (biggest surface; start with whoami, EC2, SG/IAM/S3 audits, EKS)
5. Phase 5 cross-provider tools, resources and prompts
6. Phase 7 Helm chart, metrics, release pipeline
7. Phase 6 write tools last, behind approval
