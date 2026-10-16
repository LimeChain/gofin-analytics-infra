# GoFin analytics infrastructure

A Caddy container on Google Cloud Run forwards GoFin telemetry to PostHog EU.
GitHub Actions tests changes, plans pull requests and deploys `main` with Terraform.

| Setting | Value |
| --- | --- |
| Google project | `gofin-analytics-511817` |
| Cloud Run region | `europe-west1` (Belgium) |
| Upstream | `https://eu.i.posthog.com` |
| Service | `gofin-analytics-proxy` |
| Capacity | Request-based billing, 1 vCPU, 512 MiB, 0–3 instances, concurrency 80 |
| Authentication | Public ingestion; GitHub deployments use Workload Identity Federation |
| State | Separate private, versioned GCS buckets for bootstrap and proxy |

## Proxy behavior and privacy

The CLI sends PostHog's native capture payload, including its public project token,
to the proxy URL using the same API path (for example `/i/v0/e/`). The proxy does
not store a PostHog token, parse events, validate their schema, transform payloads,
queue data, or retry failed requests. It preserves upstream response status/body.
`/healthz` is a local readiness endpoint and does not contact PostHog.

Caddy removes conventional client-IP forwarding headers, cookies, authorization
and tracing headers, and uses a fixed upstream User-Agent. It suppresses access
and request-error logs. Terraform excludes this service's automatic Cloud Run
request logs from the project's default log sink. System/deployment logs and
aggregate Cloud Run metrics remain available. Google still sees the connecting IP;
exclusions do not control any organization-level log sinks. Payloads and query
strings are forwarded unchanged, so the CLI remains responsible for safe fields,
disabling IP-based enrichment, and excluding secrets, financial identifiers and
persistent user/install identifiers. A proxy cannot sanitize an unsafe payload.

The endpoint is public and forwards to one configured upstream. Other clients can
submit traffic too; the instance limit reduces capacity but is not a hard cost cap.
There is no load balancer, VPC connector, database or always-running instance.

## One-time bootstrap

Prerequisites: Terraform 1.10+ (CI uses 1.15.1), Google Cloud CLI, project billing,
and an administrator able to enable APIs and provision IAM, storage and registry
resources. Docker and Python 3 are needed for local proxy checks.

Run once with an administrator's local Google credentials:

```sh
gcloud auth login --update-adc
gcloud auth application-default set-quota-project gofin-analytics-511817
terraform -chdir=bootstrap init
terraform -chdir=bootstrap plan -out=bootstrap.tfplan
terraform -chdir=bootstrap apply bootstrap.tfplan
cp bootstrap/backend.tf.example bootstrap/backend.tf
terraform -chdir=bootstrap init -migrate-state
```

The first apply creates the state buckets using local state. The last command
migrates that state to GCS; retain local state backups until migration is verified.
On subsequent checkouts, copy `backend.tf.example` to `backend.tf` **before**
initializing bootstrap. Never start a second local bootstrap state for the same project.

Bootstrap owns API enablement, state buckets, Artifact Registry, the runtime and
CI service accounts, their IAM bindings, and GitHub federation. Its changes require
the administrator to plan/apply this stack; ordinary CI cannot change its own trust
or permissions. The runtime account has no project roles. The deploy account can
deploy Cloud Run, manage log exclusions, push images and update proxy state. The
plan account can read Cloud Run, logging configuration and proxy state.
Neither CI identity can access the bootstrap state bucket through these grants.

Federation pins this repository and organization by numeric IDs. Deployment
credentials are available only to `main` push/manual runs. Pull requests from the
same repository use the read-only plan account; fork PRs receive offline checks only.
The workflow contains public resource identifiers and needs no GitHub secrets.

## Changes and deployment

1. Change `proxy/` or `infra/` in a branch and open a pull request.
2. Checks validate Terraform and exercise the real proxy with a synthetic upstream.
   The PR's Terraform plan uses the current deployed image; a new image is produced
   after merge. Bootstrap changes are validated but require the admin apply above.
3. Merge to `main`. Actions builds and tests the image, pushes it, plans and applies
   the exact digest, and verifies the public health endpoint. Runs are serialized.
4. Find the service URL and image digest in the workflow's deployment summary.

`infra/` owns Cloud Run and its request-log exclusion. The GCS backend locks writes;
PR plans use `-lock=false` because their identity is read-only. Deployment always
creates a fresh plan under the state lock. Images are retained for rollback; prune
old images deliberately, keeping currently deployed and desired rollback digests.

For local checks:

```sh
make validate
make test
```

Terraform provider versions and checksums, GitHub Actions and the Caddy base image
are pinned. Update their pins through a tested pull request. Do not commit state,
plan files, credentials or private analytics data.

## Rollback and verification

Run **Proxy infrastructure** manually on `main` with a previous `sha256:…` image
digest to roll back only the container while keeping current infrastructure.
To revert Terraform changes, revert the relevant commit through a pull request.
Cloud Run deletion protection and state bucket deletion guards protect against
accidental removal; teardown requires deliberate configuration changes.

The health endpoint verifies the proxy process, not PostHog availability. A live
ingestion check should send one explicitly marked synthetic event using the public
project token. A successful capture response proves ingestion acceptance, not
that an event is already queryable in PostHog. Verify persistence in PostHog separately.
CLI instrumentation and `gofin analytics enable|disable|status` belong in the GoFin
CLI repository and are not implemented by this infrastructure repository.
