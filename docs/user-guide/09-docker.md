# 9. Docker guide

The repository ships a production-grade container setup: a multi-stage
`Dockerfile` producing a distroless, non-root image, and a hardened
`docker-compose.yml` for controlled execution. This chapter covers building,
running, secrets, signals, and the honest verification status of the image.

## 9.1 What the image is

| Property | Value |
|---|---|
| Build stage | `golang:1.27.2-alpine`, `CGO_ENABLED=0` static build, `-trimpath -s -w`, version injected from build arg |
| Runtime stage | `gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager, runs as uid `65532` |
| Entrypoint | `["/iam-perf"]` (exec form; signals reach the process directly) |
| Healthcheck | `HEALTHCHECK CMD ["/iam-perf", "version"]` — process liveness, not target health, by design |
| Volumes | `/reports` declared for report output; config mounted read-only at run time |
| Secrets | **Never baked in.** Injected at run time via environment (`-e IAMPERF_TEST_PASSWORD`) |
| Labels | OCI standard (`title`, `description`, `version`, `licenses`, `base.name`) |

The distroless runtime is the security story: with no shell and no package
manager inside the image, the usual container escape-and-persist playbook has
nothing to work with, and the image's attack surface is a single static binary.

## 9.2 Build

```bash
make docker-build          # = docker build --build-arg VERSION=$(cat VERSION) -t iam-perf:$(VERSION) .
# or directly:
docker build --build-arg VERSION=0.1.0 -t iam-perf:0.1.0 .
```

Then smoke-test the image — proves non-root execution, no shell, and a working
binary:

```bash
make docker-smoke
# = docker run --rm --user 65532:65532 iam-perf:0.1.0 version
#   docker run --rm --read-only iam-perf:0.1.0 validate -c /dev/null || true
```

> **Verification status (read this once).** The Dockerfile and compose file
> were authored and statically reviewed during development, but the build
> environment had **no Docker daemon**, so these builds have not been executed
> by the developer. Before first production use: run `make docker-build
> docker-smoke` on an operator machine, and **pin base images by digest** —
> replace both `FROM` tags with `image@sha256:…` digests (obtain with
> `docker images --digests` or `crane digest`), because tags are mutable and
> digests are not. The Dockerfile header repeats this checklist.

`.dockerignore` excludes reports, local configs with secrets, and other
working-tree noise from the build context, so nothing sensitive can leak into
an image layer.

## 9.3 Run

The essential pattern — configs and reports via mounts, secret via runtime
environment:

```bash
docker run --rm \
  -v "$PWD/configs:/configs:ro" \
  -v "$PWD/reports:/reports" \
  -e IAMPERF_TEST_PASSWORD \
  iam-perf:0.1.0 \
  run -c /configs/example-closed.json --iam-accept-risk
```

Notes that matter in practice:

- **`-e IAMPERF_TEST_PASSWORD` without `=value`** forwards the variable from
  your shell into the container — the secret never appears in the command
  line (and thus not in shell history or `docker inspect` of the *command*).
- **Reports path**: the container's working directory is `/`; the config's
  `report.output_dir` must therefore be an absolute in-container path such as
  `/reports` (override per run with `run --out /reports` if you reuse host
  configs).
- **Config paths inside the container**: with configs mounted at `/configs`,
  the `-c` argument is `/configs/....json`, not the host path.
- **The two authorization keys are unchanged**: `authorization.accepted=true`
  in the mounted config, plus `--iam-accept-risk` (or `-e
  IAMPERF_ACCEPT_RISK=true`). Containers do not weaken the gate.
- **Dry-run first**: `docker run --rm -v "$PWD/configs:/configs:ro"
  iam-perf:0.1.0 validate -c /configs/example-closed.json` — zero network,
  confirms config + mounts.

### Prometheus from a container

With `metrics.prometheus_enabled` and `prometheus_addr: 0.0.0.0:9537` in the
config (in-container bind), publish the port deliberately:

```bash
docker run --rm -p 127.0.0.1:9537:9537 ... iam-perf:0.1.0 run -c /configs/x.json --iam-accept-risk
```

The `127.0.0.1:` prefix on `-p` keeps the scrape port off the network — scrape
locally only. If you keep `prometheus_addr: 127.0.0.1:9537` inside the
container, no publish will reach it (loopback is loopback), which is the safe
default.

## 9.4 Signals in containers

`iam-perf` handles SIGINT/SIGTERM gracefully (§3.7): immediate stop of new
attempts, bounded drain (≤ 10 s), full report, exit `130`. For that to work in
containers:

- The exec-form `ENTRYPOINT` ensures signals go straight to the binary (no
  shell wrapper to eat them).
- With plain `docker run`, add `--init` if you want the zombie-reaper and
  clean signal forwarding; with compose, the file sets
  `stop_grace_period: 15s` so Docker waits out the drain before escalating to
  SIGKILL. If you override the grace below ~12 s you may truncate report
  writing on cancellation — keep it ≥ 15 s.
- `docker compose run --rm iam-perf ...` attaches interactively; `Ctrl-C`
  there is SIGINT to the container process — same graceful path.

## 9.5 Compose (hardened local/lab execution)

```bash
export IAMPERF_TEST_PASSWORD='...'
docker compose run --rm iam-perf validate -c /configs/example-closed.json
docker compose run --rm iam-perf run -c /configs/example-closed.json --iam-accept-risk
```

The shipped `docker-compose.yml` builds/pins `iam-perf:0.1.0` and applies
hardening worth copying into your own deployments:

| Setting | Effect |
|---|---|
| `read_only: true` | Immutable root filesystem; reports go to the mounted volume. |
| `cap_drop: [ALL]` | No Linux capabilities at all. |
| `security_opt: [no-new-privileges:true]` | Blocks privilege escalation paths. |
| `tmpfs: /tmp:size=64m,noexec,nosuid` | Small, non-executable scratch space. |
| `environment: [IAMPERF_TEST_PASSWORD, IAMPERF_ACCEPT_RISK]` | Both secrets/keys forwarded from your shell at run time — never stored in the file. |
| `volumes: ./configs:/configs:ro, ./reports:/reports` | Read-only config, writable reports. |
| `stop_grace_period: 15s` | Room for graceful drain + report write. |
| no `ports:` by default | Nothing listens externally unless you opt in (Prometheus pattern above). |

The compose header states its own scope: it exists for **controlled local/lab
execution** and never points at the production IAM service by itself — the
authorization keys still gate every run.

## 9.6 Resource limits and generator honesty

A container is a convenient load generator but a modest one: CPU quota caps
the generator before it caps most targets. A 1-CPU container cannot sustain
multi-thousand-RPS generation; check the report's `environment.num_cpu` (which
reflects the container's view) and the §5.5 saturation signals
(`backpressure_drops`, `observed_rps` vs `configured_rps`) before attributing
results to the target. Practical guidance:

- Give the generator at least 2 CPUs for login flows with meaningful rates;
  more for open-model rates beyond a few hundred RPS.
- `--cpus` / `--memory` explicitly rather than relying on node defaults, and
  record them — a result without generator resource context is hard to
  compare later.
- Scale out (one container per host, separate reports) rather than
  fighting one container's limits; the tool does not coordinate across
  generators, by design.

## 9.7 Image ↔ binary equivalence

The image runs the same static binary `make build` produces on any Linux host
(same build flags, same version injection). Behavior, exit codes, signals, and
reports are identical; only the filesystem around the process differs. When
filings results, record which one you used — the report's `environment.goos/
goarch/go_version` covers the toolchain; your run log should cover the
container/runtime (e.g. `docker version`).
