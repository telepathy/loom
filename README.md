# Loom — Dependency Analysis System

Loom is a dependency analysis subsystem for GPS (Global Publishing System). It analyzes Java Gradle multi-project repositories to extract module coordinates and declared dependency edges, producing raw data for GPS to normalize, topo-sort, and orchestrate releases.

## How It Works

```
GPS / Self ──POST /das/analyze──▶  Loom  ──create K8s Job──▶  Job
                                       │                        │
                                       │  ┌─ initContainer: clone + akasha
                                       │  └─ container: gradlew --init-script
                                       │                        │
                                       ◀── curl callback ───────┘
                                       │
                                       ▼
                                   Memory + MySQL (loom_*)
```

Loom **does not parse `build.gradle` text**. Instead, it runs Gradle with an init script (`das.gradle`) that hooks into `projectsEvaluated` and exports the resolved project model to `das-output.json`. This produces accurate results even with conditional dependencies, version catalogs, and `apply from:` remote scripts.

## Quick Start

```bash
# Local development (no K8s, no DB)
DAS_LOCAL_MODE=true go run main.go

# With MySQL persistence
DAS_MYSQL_DSN="root:pass@tcp(127.0.0.1:3306)/loom?parseTime=true" \
DAS_LOCAL_MODE=true \
go run main.go

# K8s mode (requires in-cluster config or DAS_KUBECONFIG)
go run main.go
```

Open http://localhost:8080 to view the analysis dashboard.

## API

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/das/analyze` | Submit repos with tags for analysis (GPS-driven) |
| `POST` | `/das/analyze/self` | Self-driven analysis (reads `gps_repos` from DB) |
| `GET` | `/das/analyze/:plan_id` | Poll analysis status (in-memory) |
| `POST` | `/das/callback` | Receive results from K8s Job |
| `GET` | `/das/plans` | List all analysis plans (from DB) |
| `GET` | `/das/plans/:plan_id` | Get full plan detail with repos, subprojects, edges |
| `GET` | `/das/health` | Health check |

### Request Examples

**GPS-driven analysis:**
```json
POST /das/analyze
{
  "plan_id": "plan-001",
  "akasha_branch": "202603",
  "callback_base_url": "http://gps-das.gps.svc:8080",
  "repos": [
    {"repo_id": "repo-0012", "repo_url": "ssh://git@...", "tag": "v2026.03", "jdk": "17"}
  ]
}
```

**Self-driven analysis:**
```json
POST /das/analyze/self
{
  "silo_ids": ["silo-payment"],
  "akasha_branch": "202603"
}
```

## Configuration

All settings via environment variables.

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `DAS_PORT` | `8080` | HTTP listen port |
| `DAS_LOCAL_MODE` | `false` | `true` = exec gradlew directly; `false` = create K8s Jobs |

### K8s Cluster

| Variable | Default | Description |
|----------|---------|-------------|
| `DAS_K8S_NAMESPACE` | `gps` | Job namespace |
| `DAS_GIT_IMAGE` | `registry/gps-das-git:latest` | Clone init-container image |
| `DAS_JDK_IMAGE_PREFIX` | `registry/gps-das-jdk` | Analyze image prefix, suffixed with `:<jdk>` |
| `DAS_DEFAULT_JDK` | `17` | Default JDK major version |
| `DAS_CONFIGMAP_NAME` | `das-init-script` | das.gradle ConfigMap name |
| `DAS_SSH_SECRET_NAME` | `codeup-ssh` | Git SSH private key Secret name |
| `DAS_KUBECONFIG` | — | Local dev kubeconfig path (empty = in-cluster) |
| `DAS_DEBUG_JOB_YAML` | `false` | Write rendered Job YAML to `/tmp` |

### Runtime

| Variable | Default | Description |
|----------|---------|-------------|
| `DAS_MAX_PARALLEL` | `5` | Max concurrent Jobs |
| `DAS_JOB_TIMEOUT` | `600` | Single Job timeout in seconds |
| `DAS_PLAN_TTL` | `3600` | In-memory plan retention in seconds |

### External Systems

| Variable | Default | Description |
|----------|---------|-------------|
| `DAS_MYSQL_DSN` | — | MySQL DSN — enables persistence + self-driven analysis |
| `DAS_AKASHA_API_URL` | — | Akasha gradle.properties API URL (e.g. `http://akasha:8080/api/v1/gradle-properties`) |

## K8s Deployment

```bash
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/mysql-secret.yaml
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/rbac.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

## Database

Loom shares the GPS MySQL schema. It reads `gps_repos` for self-driven analysis and writes to its own `loom_*` tables for persistence.

| Table | Purpose |
|-------|---------|
| `loom_analysis_plans` | Analysis plan metadata |
| `loom_analysis_repos` | Per-repo analysis status |
| `loom_subprojects` | Discovered Gradle subprojects (GA coordinates) |
| `loom_edges` | Raw dependency edges (project + external) |

## Build from Source

```bash
# Local
go build -ldflags="-X main.version=v0.0.1" -o loom-server .

# Docker
docker build --build-arg VERSION=v0.0.1 -t loom-server .
```

## License

MIT
