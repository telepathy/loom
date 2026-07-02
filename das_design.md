# DAS 详细设计：依赖分析系统（Dependency Analysis System）

> 本文是 GPS 设计文档（`design.md` §5.2）中 DAS 的详细设计，以及 `docs/design-module-identity.md` 中"模块标识与依赖归一化"的落地方案。
>
> **职责**：给定一组仓库及其本次发布的 tag，分析出**模块级依赖图**（节点为 GA、边为 `GA → GA`），供 GPS 做拓扑排序、环检测与并发池发布编排。

---

## 1. 总览

### 1.1 职责边界

| | DAS | GPS |
|---|---|---|
| 职责 | 拉起 K8s Job、聚合原始 Gradle 模型 | 归一化、节点分类、拓扑、环检测、落库 |
| 数据 | 无数据库，状态仅在内存（请求生命周期内） | 持有 MySQL，所有计划级数据持久化 |
| 交互 | HTTP：接收 GPS 请求、接收 Job 回调、返回结果 | HTTP：调用 DAS、接收结果 |
| K8s | 直接操作 Job（create/watch/delete） | 不接触 K8s |

### 1.2 架构

```
GPS                        DAS (Go + Gin, 无 DB)          K8s 集群
 │                                                       
 │ ① POST /das/analyze ─────▶│                           
 │   (异步,立即返回 plan_id)   │ ② 创建 K8s Job ──────────▶ Job: das-<repo_id>-<plan_id>
 │                            │                              init(git): clone --depth 1 --branch <tag>
 │ ③ GET /das/analyze/:pid ──▶│                              analyze(jdk): gradlew --init-script
 │   (轮询,返回各 repo 状态)   │ ◀── ④ Job curl POST ────────    └ apply from akasha
 │                            │    /das/callback              └ 写 das-output.json
 │ ⑤ GET /das/analyze/:pid ──▶│ ⑥ 聚合完成,返回原始结果      
 │ ◀── JSON ──────────────────│   (不归一化、不落库)         
 │ ⑦ 归一化 + 分类 + 拓扑 + 环检测                         
 │ ⑧ 落库 plan_module / plan_dep_edge / plan_topo_order    
```

---

## 2. API 规范

### 2.1 `POST /das/analyze` — 发起分析

GPS 调用，提交一批仓库的分析任务。DAS 为每个 repo 创建一个 K8s Job，立即返回 `plan_id` 供 GPS 后续轮询。

**请求体**：

```json
{
  "plan_id": "plan-001",
  "akasha_branch": "202603",
  "callback_base_url": "http://gps-das.gps.svc:8080",
  "repos": [
    {
      "repo_id": "repo-0012",
      "repo_url": "ssh://git@codeup.devops.csdc.com:9022/f6e73c53/payment/issuance.git",
      "tag": "v2026.03",
      "jdk": "17"
    },
    {
      "repo_id": "repo-0007",
      "repo_url": "ssh://git@codeup.devops.csdc.com:9022/f6e73c53/settlement/settle.git",
      "tag": "v2026.03",
      "jdk": "8"
    }
  ]
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `plan_id` | string | ✅ | GPS 侧的计划 ID，用于关联分析结果 |
| `akasha_branch` | string | ✅ | akasha 依赖分支，Gradle `apply from` 需要 |
| `callback_base_url` | string | ✅ | DAS 自身的回调地址（Job 内 curl 回传用） |
| `repos` | array | ✅ | 待分析仓库列表 |
| `repos[].repo_id` | string | ✅ | 仓库 ID（GPS 侧标识） |
| `repos[].repo_url` | string | ✅ | Git SSH 克隆地址 |
| `repos[].tag` | string | ✅ | 本次发布的 tag（源码已冻结） |
| `repos[].jdk` | string | | JDK 大版本（`8`/`17`/`21`），默认 `17` |

**响应**（`202 Accepted`）：

```json
{
  "plan_id": "plan-001",
  "status": "ACCEPTED",
  "repo_count": 2,
  "message": "Analysis started. Poll GET /das/analyze/plan-001 for status."
}
```

**错误响应**：

| HTTP 状态码 | 场景 | 示例 |
|---|---|---|
| 400 | 请求体校验失败 | `{"error": "plan_id is required"}` |
| 409 | 同一 plan_id 已有进行中的分析 | `{"error": "plan plan-001 already in progress"}` |
| 500 | K8s Job 创建失败 | `{"error": "failed to create job: ..."}` |

---

### 2.2 `GET /das/analyze/:plan_id` — 查询分析状态

GPS 轮询此端点获取分析进度和结果。

**路径参数**：

| 参数 | 说明 |
|---|---|
| `plan_id` | 发起分析时传入的计划 ID |

**响应 — 分析进行中**（`200 OK`）：

```json
{
  "plan_id": "plan-001",
  "status": "IN_PROGRESS",
  "repos": [
    {
      "repo_id": "repo-0012",
      "status": "DONE",
      "subprojects": [
        {"gradle_path": ":core:api", "group": "com.csdc.spot", "artifact": "issuance-core-api"},
        {"gradle_path": ":core:model", "group": "com.csdc.spot", "artifact": "issuance-core-model"}
      ],
      "edges": [
        {"from": ":core:model", "to": ":core:api", "type": "project"},
        {"from": "com.csdc.settle:settle-client:1.4.0", "to": ":core:api", "type": "external"}
      ]
    },
    {
      "repo_id": "repo-0007",
      "status": "RUNNING",
      "message": "Job running (elapsed: 45s)"
    }
  ]
}
```

**响应 — 分析完成**（`200 OK`）：

```json
{
  "plan_id": "plan-001",
  "status": "COMPLETED",
  "repos": [
    {
      "repo_id": "repo-0012",
      "status": "DONE",
      "subprojects": [...],
      "edges": [...]
    },
    {
      "repo_id": "repo-0007",
      "status": "DONE",
      "subprojects": [...],
      "edges": [...]
    }
  ]
}
```

**响应 — 分析失败**（`200 OK`，部分 repo 失败）：

```json
{
  "plan_id": "plan-001",
  "status": "COMPLETED",
  "repos": [
    {
      "repo_id": "repo-0012",
      "status": "DONE",
      "subprojects": [...],
      "edges": [...]
    },
    {
      "repo_id": "repo-0007",
      "status": "FAILED",
      "error": "gradle evaluate failed: Could not resolve dependencies..."
    }
  ]
}
```

**响应 — 计划不存在**（`404 Not Found`）：

```json
{
  "error": "plan plan-999 not found or already expired"
}
```

**repo 状态枚举**：

| 状态 | 说明 |
|---|---|
| `PENDING` | Job 已创建，等待调度 |
| `RUNNING` | Job 正在执行（clone + gradle 评估） |
| `DONE` | 分析成功，`subprojects` 和 `edges` 可用 |
| `FAILED` | 分析失败，`error` 字段包含原因 |

**计划状态枚举**：

| 状态 | 说明 |
|---|---|
| `IN_PROGRESS` | 至少一个 repo 仍在分析中 |
| `COMPLETED` | 所有 repo 已终态（DONE 或 FAILED），结果可消费 |

**GPS 消费规则**：
- `status=IN_PROGRESS` → 继续轮询（建议间隔 2-5s）
- `status=COMPLETED` + 所有 repo `DONE` → 拿到完整结果，进入归一化
- `status=COMPLETED` + 任一 repo `FAILED` → GPS 让 Phase 2 失败，展示失败原因

---

### 2.3 `POST /das/callback` — Job 结果回传

K8s Job 内的 `curl` 调用，回传单个 repo 的 `das-output.json`。DAS 在内存中按 `plan_id` 暂存。

**查询参数**：

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `plan_id` | string | ✅ | 关联的计划 ID |
| `repo_id` | string | ✅ | 关联的仓库 ID |
| `tag` | string | ✅ | 分析的 tag（幂等校验用） |

**请求体**（即 `das-output.json` 的内容）：

```json
{
  "repo_id": "repo-0012",
  "tag": "v2026.03",
  "subprojects": [
    {"gradle_path": ":core:api", "group": "com.csdc.spot", "artifact": "issuance-core-api"},
    {"gradle_path": ":core:model", "group": "com.csdc.spot", "artifact": "issuance-core-model"}
  ],
  "edges": [
    {"from": ":core:model", "to": ":core:api", "type": "project"},
    {"from": "com.csdc.settle:settle-client:1.4.0", "to": ":core:api", "type": "external"},
    {"from": "org.springframework:spring-core:6.1.0", "to": ":core:model", "type": "external"}
  ]
}
```

**响应**（`200 OK`）：

```json
{"status": "received", "plan_id": "plan-001", "repo_id": "repo-0012"}
```

**错误响应**：

| HTTP 状态码 | 场景 |
|---|---|
| 400 | 缺少 plan_id/repo_id 查询参数 |
| 404 | plan_id 不存在（可能已过期或从未发起） |

---

### 2.4 `GET /das/health` — 健康检查

```json
{"status": "ok", "version": "0.1.0"}
```

---

## 3. 核心原则：驱动 Gradle 自报模型，不静态解析

**不解析 build.gradle 文本**。Gradle 构建脚本是图灵完备的 Groovy/Kotlin 代码，存在条件依赖、`apply from:` 远程脚本（akasha）、version catalog、`subprojects {}` 批量配置等，静态文本解析必然失真。

**唯一权威做法：让 Gradle 自己评估完项目后导出模型。** 通过 `--init-script` 注入一段只读脚本，在 `projectsEvaluated` 阶段遍历所有子项目，导出：
- 每个子项目的坐标（`gradle_path` + `group` + `artifact`）；
- 每个子项目**声明的**（declared，非解析传递闭包）依赖，区分 `ProjectDependency`（项目内）与 `ExternalModuleDependency`（项目间/三方）。

即便依赖写成 `libraries["spring-core"]`（akasha 注入版本），在 Gradle 内部它已被解析为带 `group:name` 的 `ExternalModuleDependency`，脚本照样能拿到 GA——这是"跑进 Gradle"相对静态解析的决定性优势。

### 3.1 提取用 init script（`das.gradle`）

```groovy
import org.gradle.api.artifacts.ProjectDependency
import org.gradle.api.artifacts.ExternalModuleDependency
import groovy.json.JsonOutput

gradle.projectsEvaluated {
    def out = [subprojects: [], edges: []]

    rootProject.allprojects.each { p ->
        // ---- 坐标 GA ----
        def artifact = p.name
        def pub = p.extensions.findByName('publishing')
        if (pub) {
            try {
                def mp = pub.publications.find { it.hasProperty('artifactId') }
                if (mp) artifact = mp.artifactId
            } catch (ignored) {}
        }
        out.subprojects << [
            gradle_path: p.path,
            group      : p.group?.toString(),
            artifact   : artifact,
            version    : p.version?.toString()
        ]

        // ---- 声明依赖（只读 declared，不触发解析）----
        def seen = [] as Set
        p.configurations.each { c ->
            if (c.name.toLowerCase().contains('test')) return
            c.dependencies.each { d ->
                if (d instanceof ProjectDependency) {
                    def path = d.hasProperty('path') ? d.path : d.dependencyProject.path
                    if (seen.add("P:$path"))
                        out.edges << [from: path, to: p.path, type: 'project']
                } else if (d instanceof ExternalModuleDependency) {
                    if (seen.add("E:${d.group}:${d.name}"))
                        out.edges << [from: "${d.group}:${d.name}:${d.version}", to: p.path, type: 'external']
                }
            }
        }
    }
    new File(gradle.rootProject.projectDir, 'das-output.json').text = JsonOutput.toJson(out)
}
```

**触发命令**：`./gradlew --init-script /scripts/das.gradle -PdepBranch=$AKASHA_BRANCH help -q`

- `help -q` 只完成 configuration 阶段（`projectsEvaluated` 即触发脚本），不编译、不执行业务任务。
- `ProjectDependency.dependencyProject` 在 Gradle 8.11+ 废弃，已优先 `d.path` 并回退兼容。

### 3.2 DAS 每仓库原始输出（`das-output.json`）

```json
{
  "repo_id": "repo-0012",
  "tag": "v2026.03",
  "subprojects": [
    {"gradle_path": ":core:api",   "group": "com.csdc.spot", "artifact": "issuance-core-api"},
    {"gradle_path": ":core:model", "group": "com.csdc.spot", "artifact": "issuance-core-model"}
  ],
  "edges": [
    {"from": ":core:model",                          "to": ":core:api",   "type": "project"},
    {"from": "com.csdc.settle:settle-client:1.4.0",   "to": ":core:api",   "type": "external"},
    {"from": "org.springframework:spring-core:6.1.0", "to": ":core:model", "type": "external"}
  ]
}
```

注意原始边的 `from` 形式不统一（项目内是 gradlePath、跨项目是 GAV），**归一化是 GPS 的责任**（§5）。

---

## 4. K8s 执行方案

### 4.1 镜像：职责分离

代码拉取与依赖分析用**两个独立镜像**，经共享卷传递代码：

**(a) clone 镜像（initContainer）** — 纯 git/ssh：

```dockerfile
FROM alpine/git:latest
# 无需额外内容
```

**(b) analyze 镜像（主容器）** — 纯净 openjdk + curl，不装 git/ssh：

```dockerfile
FROM eclipse-temurin:17-jdk
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*
ENV GRADLE_USER_HOME=/gradle-cache
WORKDIR /work
```

**多 JDK 版本**：每个常用 JDK 大版本一个镜像 tag：
```
registry/gps-das-jdk:8
registry/gps-das-jdk:17
registry/gps-das-jdk:21
```

### 4.2 ConfigMap（init script 注入）

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: das-init-script
  namespace: gps
data:
  das.gradle: |
    # §3.1 的完整脚本内容
```

### 4.3 Job 模板

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: das-{{repo_id}}-{{plan_id}}
  namespace: gps
  labels:
    app: gps-das
    plan: "{{plan_id}}"
    repo: "{{repo_id}}"
spec:
  backoffLimit: 1
  activeDeadlineSeconds: 600
  ttlSecondsAfterFinished: 600
  template:
    metadata:
      labels:
        app: gps-das
        plan: "{{plan_id}}"
    spec:
      restartPolicy: Never
      volumes:
        - name: workspace
          emptyDir: {}
        - name: init-script
          configMap:
            name: das-init-script
        - name: ssh-key
          secret:
            secretName: codeup-ssh
            defaultMode: 0400
        - name: gradle-cache
          persistentVolumeClaim:
            claimName: gradle-dist-cache
            readOnly: true
      initContainers:
        - name: clone
          image: registry/gps-das-git:latest
          command: ["sh", "-c"]
          args:
            - |
              export GIT_SSH_COMMAND="ssh -i /keys/id_rsa -o StrictHostKeyChecking=no"
              git clone --depth 1 --branch {{tag}} {{repo_url}} /work/src
          volumeMounts:
            - name: workspace
              mountPath: /work
            - name: ssh-key
              mountPath: /keys
              readOnly: true
      containers:
        - name: analyze
          image: registry/gps-das-jdk:{{jdk}}
          workingDir: /work/src
          env:
            - name: DEP_BRANCH
              value: "{{akasha_branch}}"
            - name: GRADLE_USER_HOME
              value: /gradle-cache
            - name: CALLBACK_URL
              value: "{{callback_base_url}}/das/callback?plan_id={{plan_id}}&repo_id={{repo_id}}&tag={{tag}}"
          command: ["sh", "-c"]
          args:
            - |
              set -e
              ./gradlew --init-script /scripts/das.gradle \
                        -PdepBranch=$DEP_BRANCH \
                        help -q 2>/tmp/gradle-stderr.log || {
                # gradle 失败：回传错误
                ESCAPED=$(cat /tmp/gradle-stderr.log | head -200 | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')
                curl -sf -X POST "$CALLBACK_URL" \
                  -H "Content-Type: application/json" \
                  -d "{\"error\": $ESCAPED}" || true
                exit 1
              }
              # 成功：回传 das-output.json
              curl -sf -X POST "$CALLBACK_URL" \
                -H "Content-Type: application/json" \
                --data-binary @/work/src/das-output.json
          volumeMounts:
            - name: workspace
              mountPath: /work
            - name: init-script
              mountPath: /scripts
              readOnly: true
            - name: gradle-cache
              mountPath: /gradle-cache
              readOnly: true
          resources:
            requests:
              cpu: "500m"
              memory: "1Gi"
            limits:
              cpu: "2"
              memory: "2Gi"
```

### 4.4 RBAC 与 Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: codeup-ssh
  namespace: gps
type: Opaque
data:
  id_rsa: <base64 私钥>
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: das
  namespace: gps
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: das-job-runner
  namespace: gps
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods", "pods/log"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: das-job-runner-binding
  namespace: gps
subjects:
  - kind: ServiceAccount
    name: das
roleRef:
  kind: Role
  name: das-job-runner
  apiGroup: rbac.authorization.k8s.io
```

### 4.5 网络与离线权衡

| 需求 | 是否需要网络 | 处理 |
|------|-------------|------|
| 拉 git tag | 需（ssh） | ssh key Secret + `StrictHostKeyChecking=no` |
| `apply from: akasha/dependency?branch=` | **需** | akasha 同集群，走 service DNS |
| 解析传递依赖 / Maven 仓库 | **不需** | 只读 declared 依赖，可 `--offline` |
| 下载 Gradle wrapper dist | 需（除非缓存） | PVC 缓存或烤进镜像 |

---

## 5. 归一化（GPS 侧，纯逻辑）

GPS 收到 DAS 返回的聚合原始结果后：

1. 用每个 repo 的 `subprojects` 建表 `gradlePath → GA`（GA = `group:artifact`）。
2. 逐边把两端解析为 GA：
   - `type=project` → `from` 是 gradlePath，查**本 repo** 表 → GA；
   - `type=external` → 剥掉版本，`group:name` 即 GA。
3. 给每个 GA 节点分类：
   - **internal**：命中某 devops repo 的 subproject；
   - **pending-external**：GA 属自研 group 命名空间（如 `com.csdc.*`）但不命中任何 devops repo；
   - **third-party**：其余（如 `org.springframework:*`）→ **丢弃，不进图**。
4. `cross_repo = (from 与 to 属于不同 repo)`。
5. 拓扑排序 + 环检测（模块级有环则 Phase 2 失败并定位环路径）。

`artifact` 即 akasha 的 join key；同一 akasha 分支内 artifact 须唯一，归一化时若发现冲突报错。

---

## 6. 落库 schema（GPS 侧，计划级快照）

> 以下表都是 GPS 的数据表，由 GPS 在归一化后写入。DAS 不读写这些表。

### 6.1 `gps_plan_modules` — 节点

```sql
CREATE TABLE gps_plan_modules (
    plan_id        VARCHAR(64)  NOT NULL,
    ga             VARCHAR(255) NOT NULL,   -- group:artifact
    group_id       VARCHAR(128) NOT NULL,
    artifact       VARCHAR(128) NOT NULL,   -- = akasha join key
    kind           VARCHAR(24)  NOT NULL,   -- internal | pending-external
    repo_id        VARCHAR(64),             -- NULL for pending-external
    silo_id        VARCHAR(64),
    target_version VARCHAR(32),
    PRIMARY KEY (plan_id, ga)
);
```

### 6.2 `gps_plan_dep_edges` — 边

```sql
CREATE TABLE gps_plan_dep_edges (
    plan_id    VARCHAR(64)  NOT NULL,
    from_ga    VARCHAR(255) NOT NULL,
    to_ga      VARCHAR(255) NOT NULL,
    cross_repo BOOLEAN      NOT NULL,
    PRIMARY KEY (plan_id, from_ga, to_ga)
);
```

### 6.3 `gps_plan_gradle_subprojects` — 映射（审计）

```sql
CREATE TABLE gps_plan_gradle_subprojects (
    plan_id     VARCHAR(64)  NOT NULL,
    repo_id     VARCHAR(64)  NOT NULL,
    gradle_path VARCHAR(255) NOT NULL,
    ga          VARCHAR(255) NOT NULL,
    PRIMARY KEY (plan_id, repo_id, gradle_path)
);
```

### 6.4 `gps_plan_topo_orders` — 排序

```sql
CREATE TABLE gps_plan_topo_orders (
    plan_id VARCHAR(64)  NOT NULL,
    seq     INT          NOT NULL,
    ga      VARCHAR(255) NOT NULL,
    PRIMARY KEY (plan_id, seq)
);
```

---

## 7. DAS 内部设计

### 7.1 项目结构

```
das/
├── main.go                     # 入口：Gin 路由注册、K8s client 初始化、配置加载
├── config/
│   └── config.go               # 配置结构体 + 环境变量读取
├── handler/
│   ├── analyze.go              # POST /das/analyze + GET /das/analyze/:plan_id
│   ├── callback.go             # POST /das/callback
│   └── health.go               # GET /das/health
├── job/
│   ├── template.go             # K8s Job YAML 渲染（Go template）
│   ├── manager.go              # Job 创建 / watch / 删除 / 超时处理
│   └── template_test.go        # 模板渲染单测
├── model/
│   └── model.go                # 请求/响应结构体、内部状态结构体
├── store/
│   └── store.go                # 内存状态存储（plan → repo 状态机）
├── go.mod
├── go.sum
├── Dockerfile
└── k8s/                        # K8s 部署清单（可选）
    ├── deployment.yaml
    ├── service.yaml
    ├── rbac.yaml
    ├── configmap.yaml
    └── secret.yaml
```

### 7.2 配置（环境变量）

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `DAS_PORT` | — | `8080` | HTTP 监听端口 |
| `DAS_K8S_NAMESPACE` | — | `gps` | Job 创建的 namespace |
| `DAS_GIT_IMAGE` | — | `registry/gps-das-git:latest` | initContainer clone 镜像 |
| `DAS_JDK_IMAGE_PREFIX` | — | `registry/gps-das-jdk` | analyze 镜像前缀，拼 `:<jdk>` |
| `DAS_DEFAULT_JDK` | — | `17` | 未指定 jdk 时的默认版本 |
| `DAS_MAX_PARALLEL` | — | `5` | 同时创建的 Job 数上限 |
| `DAS_JOB_TIMEOUT` | — | `600` | 单 Job 超时秒数（`activeDeadlineSeconds`） |
| `DAS_PLAN_TTL` | — | `3600` | 计划结果在内存中保留的秒数（过期自动清理） |
| `DAS_CONFIGMAP_NAME` | — | `das-init-script` | init script ConfigMap 名称 |
| `DAS_SSH_SECRET_NAME` | — | `codeup-ssh` | git ssh 私钥 Secret 名称 |
| `DAS_IMAGE_PULL_SECRETS` | — | — | 镜像拉取 Secret 名称列表，逗号分隔（如 `regcred,regcred2`） |
| `DAS_GRADLE_CACHE_PVC` | — | `gradle-dist-cache` | Gradle 发行版缓存 PVC 名称 |

### 7.3 内存状态模型

```go
// PlanState 跟踪一个 plan_id 下所有 repo 的分析状态。
type PlanState struct {
    PlanID        string
    AkashaBranch  string
    CallbackBase  string
    Repos         map[string]*RepoState  // repo_id → 状态
    Status        PlanStatus             // IN_PROGRESS | COMPLETED
    CreatedAt     time.Time
}

// RepoState 跟踪单个 repo 的分析状态。
type RepoState struct {
    RepoID     string
    RepoURL    string
    Tag        string
    JDK        string
    JobName    string           // K8s Job 名称
    Status     RepoStatus       // PENDING | RUNNING | DONE | FAILED
    Subprojects []Subproject    // DONE 时填充
    Edges      []RawEdge        // DONE 时填充
    Error      string           // FAILED 时填充
    StartedAt  *time.Time
    FinishedAt *time.Time
}

type Subproject struct {
    GradlePath string `json:"gradle_path"`
    Group      string `json:"group"`
    Artifact   string `json:"artifact"`
}

type RawEdge struct {
    From string `json:"from"`
    To   string `json:"to"`
    Type string `json:"type"` // "project" | "external"
}
```

### 7.4 并发控制

- **Job 创建**：信号量（`chan struct{}`，容量 = `DAS_MAX_PARALLEL`），超限时阻塞等待。
- **Job 监控**：每个 Job 启动一个 goroutine `watch`（`k8s.io/client-go` informer 或 poll），检测终态后更新 `RepoState`。
- **超时**：Job 自身有 `activeDeadlineSeconds`；DAS 侧另起一个 timer，超时后 `DELETE Job` + 标记 `FAILED`。
- **清理**：`DAS_PLAN_TTL` 到期后，删除 `PlanState`（内存释放）。已终态的 Job 由 K8s `ttlSecondsAfterFinished` 自动清理。

### 7.5 错误处理

| 场景 | DAS 处理 | GPS 看到的 |
|---|---|---|
| Job 创建失败 | `RepoState.Status = FAILED, Error = "k8s create failed: ..."` | repo `status=FAILED` |
| Job 超时 | `DELETE Job` + `RepoState.Status = FAILED, Error = "timeout after 600s"` | repo `status=FAILED` |
| Job Pod OOM/Crash | watch Pod 状态，检测 `Failed` phase | repo `status=FAILED` |
| Job 成功但 curl 回调失败 | Job 会重试（`backoffLimit:1`）；若仍失败，超时兜底 | repo `status=FAILED` |
| 回调 JSON 格式错误 | HTTP 400，Job 视为失败（回传的不是合法结果） | repo `status=FAILED` |
| Gradle 评估失败 | Job 内捕获 stderr，curl 回传 `{"error": "..."}` | repo `status=FAILED, error=...` |
| GPS 重复调用 analyze | 同一 plan_id 若仍在 IN_PROGRESS → 409；若已 COMPLETED → 清理旧状态，重新开始 | 409 或重新分析 |

### 7.6 本地开发模式

提供 `DAS_LOCAL_MODE=true` 环境变量，跳过 K8s，直接 `exec` 调用 Gradle：

```go
// 本地模式：直接 exec gradlew，不创建 K8s Job
if cfg.LocalMode {
    cmd := exec.Command("./gradlew", "--init-script", cfg.InitScriptPath,
        "-PdepBranch", plan.AkashaBranch, "help", "-q")
    cmd.Dir = cloneDir
    // ... 读 das-output.json
}
```

适用场景：本地开发调试、CI 环境、无 K8s 的单机部署。

---

## 8. 部署

### 8.1 Dockerfile

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o das-server .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/das-server /usr/local/bin/das-server
EXPOSE 8080
ENTRYPOINT ["das-server"]
```

### 8.2 K8s Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gps-das
  namespace: gps
spec:
  replicas: 1
  selector:
    matchLabels:
      app: gps-das
  template:
    metadata:
      labels:
        app: gps-das
    spec:
      serviceAccountName: das
      containers:
        - name: das
          image: registry/gps-das:latest
          ports:
            - containerPort: 8080
          env:
            - name: DAS_K8S_NAMESPACE
              value: "gps"
            - name: DAS_MAX_PARALLEL
              value: "5"
          readinessProbe:
            httpGet:
              path: /das/health
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /das/health
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 30
---
apiVersion: v1
kind: Service
metadata:
  name: gps-das
  namespace: gps
spec:
  selector:
    app: gps-das
  ports:
    - port: 8080
      targetPort: 8080
```

---

## 9. GPS 侧对接

### 9.1 DAS 客户端接口

```go
// internal/das/client.go
package das

type Client struct {
    baseURL    string
    httpClient *http.Client
}

type AnalyzeRequest struct {
    PlanID          string      `json:"plan_id"`
    AkashaBranch    string      `json:"akasha_branch"`
    CallbackBaseURL string      `json:"callback_base_url"`
    Repos           []RepoInput `json:"repos"`
}

type RepoInput struct {
    RepoID  string `json:"repo_id"`
    RepoURL string `json:"repo_url"`
    Tag     string `json:"tag"`
    JDK     string `json:"jdk,omitempty"`
}

type AnalyzeResponse struct {
    PlanID    string         `json:"plan_id"`
    Status    string         `json:"status"`      // IN_PROGRESS | COMPLETED
    Repos     []RepoResult   `json:"repos"`
}

type RepoResult struct {
    RepoID      string      `json:"repo_id"`
    Status      string      `json:"status"` // DONE | FAILED | RUNNING | PENDING
    Subprojects []Subproject `json:"subprojects,omitempty"`
    Edges       []RawEdge    `json:"edges,omitempty"`
    Error       string      `json:"error,omitempty"`
    Message     string      `json:"message,omitempty"`
}

// Analyze 发起分析（异步，返回 plan_id）。
func (c *Client) Analyze(req AnalyzeRequest) error

// Status 查询分析状态和结果。
func (c *Client) Status(planID string) (*AnalyzeResponse, error)
```

### 9.2 GPS ConfirmPlan 流程变更

```
ConfirmPlan(planID):
  ① 收集计划内所有 repo 的 {repo_id, repo_url, tag, jdk}
  ② 调用 das.Client.Analyze(...)
  ③ 轮询 das.Client.Status(planID) 直到 COMPLETED
  ④ 对每个 DONE 的 repo 收集 subprojects + edges
  ⑤ 若有 FAILED 的 repo → 返回错误
  ⑥ GPS 归一化（§5）
  ⑦ 拓扑排序 + 环检测
  ⑧ 落库
```

---

## 10. 边界情形

| 情形 | 处理 |
|---|---|
| artifact ≠ 项目名（archivesBaseName / publication artifactId） | 优先读 publication 的 artifactId（init script 已处理） |
| `platform()/enforcedPlatform()` BOM | 也是 ExternalModuleDependency；按 group 分类，通常落 third-party 被丢弃 |
| 测试依赖（testImplementation） | 已按配置名含 `test` 跳过 |
| 仅 repo 内引用、从不发布的子项目 | 仍生成 GA 进图，不回写 akasha |
| `dependencyConstraints` / 版本对齐 | 非真实依赖，不取 |
| composite build（`includeBuild`） | 罕见；按 external 处理或单列为暂不支持 |
| 同分支内 artifact 冲突 | 归一化阶段报错（join key 歧义） |
| Gradle wrapper 版本不一 | gradle-dist 缓存按版本共存 |
| akasha 不可达 | Gradle `apply from` 失败 → Job 失败 → repo `FAILED` |
| git clone 失败（网络/权限） | initContainer 失败 → Job 失败 → repo `FAILED` |
| Job Pod 被 evict | watch 检测 → 标记 `FAILED`，GPS 可单 repo 重跑 |

---

## 11. 实现检查清单

### Phase 1：核心骨架

- [ ] `main.go`：Gin 路由 + K8s client 初始化 + 配置加载
- [ ] `config/config.go`：环境变量解析
- [ ] `model/model.go`：请求/响应/内部状态结构体
- [ ] `store/store.go`：内存 PlanState 存储（sync.RWMutex）
- [ ] `handler/health.go`：GET /das/health

### Phase 2：分析流程

- [ ] `job/template.go`：Job YAML 渲染（Go template）
- [ ] `job/manager.go`：CreateJob / WatchJob / DeleteJob / 超时处理
- [ ] `handler/analyze.go`：POST /das/analyze（创建 Job）+ GET /das/analyze/:plan_id（查询状态）
- [ ] `handler/callback.go`：POST /das/callback（接收 Job 回传）
- [ ] 信号量并发控制
- [ ] Plan TTL 过期清理

### Phase 3：K8s 资源

- [ ] `das.gradle` ConfigMap
- [ ] RBAC（ServiceAccount + Role + RoleBinding）
- [ ] Dockerfile（多阶段构建）
- [ ] Deployment + Service YAML

### Phase 4：本地开发

- [ ] `DAS_LOCAL_MODE`：直接 exec gradlew
- [ ] 单元测试：模板渲染、状态机转换、归一化逻辑

### Phase 5：GPS 对接

- [ ] `internal/das/client.go`：GPS 侧 DAS 客户端
- [ ] `ConfirmPlan` 改为调用 DAS（替代 mock.GenerateEdges）
