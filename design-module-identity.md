# 设计:Gradle 模块标识方案(DAG 节点身份)

> 目标:为"按 DAG 构建/发布的 Gradle 模块"设计一套统一的标识,既能表达**项目内**(同一 build 内子项目间)依赖,也能表达**项目间**(跨仓库)依赖。

## 1. 问题本质:模块有两个命名空间

一个 Gradle 子项目同时存在两种身份:

| 身份 | 形式 | 作用域 | 用于哪种依赖 |
|------|------|--------|-------------|
| **Gradle project path** | `:core:api` | **仅在单个 build 内唯一**(由 `settings.gradle` 定义) | 项目内:`implementation(project(":core:api"))` |
| **Maven 坐标 GAV** | `com.csdc.spot:issuance-core:1.2.3` | **全局唯一** | 项目间:`implementation("com.csdc.spot:issuance-core:1.2.3")` |

二者的桥梁:每个可发布子项目都声明了 Maven 坐标(通常通过 `maven-publish` 插件的 `group` + `artifact`)。因此 `:core:api` **可解析为** `com.csdc.spot:issuance-core-api`。

## 2. 决策:以 GA 作为模块的全局规范主键

- **模块规范 ID = GA = `group:artifact`**(剥离版本),全局唯一。
- **版本由 GPS 管理**(沿用现有"仓库级版本"机制),不进 ID。所以 ID 是 GA 不是 GAV。
- 每个仓库/build 携带一张**本地解析表** `gradlePath → GA`。

### 为什么是 GA 而不是 GAV / gradlePath

- **GAV 做 ID 不行**:GPS 会自动递增/人工覆盖版本,同一模块的 ID 会随版本变动,与仓库级版本机制冲突。
- **gradlePath 做 ID 不行**:`:core:api` 只在单个 build 内唯一,跨仓库会碰撞,无法表达项目间依赖。
- **GA 恰好统一两种场景**:
  - 项目间依赖:build 文件里的 `g:a:v` → 剥离版本 → GA。
  - 项目内依赖:`project(":core:api")` → 查本仓库 `gradlePath→GA` 表 → GA。
  - 两条路径落到同一个 GA 键空间,依赖图天然统一。

### 2.1 基础原则:DAG 节点是模块,发版是模块粒度(不是 repo 粒度)

**repo 级别可能存在循环依赖,但模块级别保证无环。** 例如 repo A 的模块 `a-api` 依赖 repo B 的 `b-model`,同时 repo B 的 `b-service` 依赖 repo A 的 `a-common`——在 repo 粒度看 A↔B 成环,但在模块粒度 `a-api / b-model / b-service / a-common` 之间并无环。

由此推出贯穿全文的硬约束:

- **DAG 的节点必须是模块(GA),不能是 repo。** 以 repo 为节点会因 repo 级环导致拓扑排序失败。
- **拓扑排序、并发池调度、上游就绪判断都在模块(GA)粒度进行。**
- **发版动作是模块粒度的动作**——逐模块构建/发布、逐模块向 akasha 回写版本、逐模块解锁下游。
- repo 仅作为**打 tag / 版本归属**的粒度(沿用"repo 级 tag、模块级发布"原则),**不是构建或拓扑的单元**。同一 repo 的不同模块可能落在 DAG 的不同位置、不同时刻发布。

> 这条原则覆盖并修正后文任何"以 repo 为构建/发布单元"的表述:tag 是 repo 级的,但构建与发布编排是模块级的。

### 2.2 发布的总体时序:统一打 tag(冻结快照)→ 依赖分析 → 模块级发布

一次统一发布分三步,顺序固定:

1. **统一打 tag(冻结源码快照)**:对 dalaran 中**所有启用 devops 的 repo**,在各自的**发布分支**上统一打一个**新 tag**。
   - tag 版本可在当前版本基础上**自动 +1**,也可**人工指定**。
   - 这是"为什么要维护发布分支(release branch)"的根本原因——tag 永远打在发布分支上。
   - 这一组 tag 一旦打完,**本次发布涉及的全部源码即被冻结**。
2. **基于这组 tag 做依赖分析**:DAS 基于**这一组 tag 对应的源码**(而非分支 HEAD)分析依赖,产出模块级 DAG(§3)。
3. **按模块级 DAG 发布**:后续所有构建/发布都**以这组 tag 为准**(checkout 该 tag 构建),按拓扑顺序逐模块进行(§8.4)。

关键推论(解决"repo tag 时机 vs 模块分时发布"的矛盾):

- **源码在第 1 步就全部冻结了**,不存在"靠后的模块用了更新的源码"问题——所有模块都基于同一组 tag 的源码构建。
- **tag 时机不再是疑问**:不是"模块发布时才打 tag",而是发布**开始前一次性对所有 repo 打 tag**。
- 唯一在发布过程中**动态变化**的,是通过 akasha 传播的**跨 repo 依赖的版本号**(§8);源码不变、依赖坐标的版本随上游模块发布而更新。
- 版本号是 **repo 级**的(一组 tag = 一个版本),与 dalaran/现有"仓库级版本"模型一致;模块继承其所属 repo 的本次 tag 版本。

## 3. 依赖如何归一化成单一 DAG

DAS 分析每个仓库的 build,对每个子项目产出:
- 该子项目自身的 GA + gradlePath + 所属 repo;
- 其依赖列表,每条依赖归一化为 GA:
  - **项目依赖** `project(":core:api")` → 查**本仓库**解析表 → GA;
  - **外部依赖** `com.csdc.x:y:1.2.3` → 剥离版本 → GA。

于是每条边都是 `GA → GA`,无论项目内/项目间形式一致。`DepEdge{From, To}` 结构不变,只是 from/to 的语义从合成 id 变为 GA 字符串。节点恒为模块(GA),从不为 repo(见 §2.1)。

### 3.1 边的两种类型:repo 内 vs 跨 repo(决定是否走 akasha)

归一化后,每条 `GA → GA` 边按两端是否属于同一 repo 分两类,二者的处理完全不同:

| 边类型 | 含义 | 谁来解析 | 是否经 akasha | 对发布顺序的影响 |
|--------|------|----------|---------------|------------------|
| **repo 内边** | from/to 属于同一 repo(`project(":core:api")`) | Gradle 在单个 build 内自行解析 | **否** | 仍在模块粒度参与拓扑;版本随构建确定,不需经 akasha 传播 |
| **跨 repo 边** | from/to 属于不同 repo(`g:a:v` 二方包) | 通过 akasha 记录与传播版本 | **是** | 在模块之间形成发布先后约束(可能两端 repo 互相成环,但模块级无环,见 §2.1) |

要点:
- **akasha 只关心跨 repo 边**。repo 内依赖由构建自身保证,版本随同一次 build 确定,不向 akasha 登记、也不从 akasha 拉取。
- 因此 **akasha 中登记的模块是"全部 repo 模块"的子集**——只有"被其他 repo 以二方包形式消费的已发布模块"才会成为 akasha 条目。
- repo 内的子项目即便不回写 akasha,仍作为 GA 节点出现在图里(保证图统一、可视化完整),只是它的版本传播不依赖 akasha。
- 第三方公共库(spring/guava 等)虽以二方包形式被消费,但它们在 akasha 已稳定可用、无需更新,**既不进 DAG 也不参与确认**(见 §4)。

## 4. 节点分类:三类节点

全局发布**一定涉及全量启用 devops 的模块**,不存在"本次未选中的 devops 模块"。归一化后,每个 GA 节点按其来源分三类:

| 类别 | 含义 | 是否进 DAG | 是否需人工确认 | 版本来源 |
|------|------|-----------|----------------|----------|
| **internal(本次发布)** | 属于产品树内**启用 devops** 的 repo 的模块 | ✅ 作为可发布节点 | 否 | 本次 GPS 发布产出,回写 akasha |
| **pending-external(自研未 devops)** | 自研、但所在 repo **未纳入 devops**,本次不由 GPS 发布,却被 internal 模块依赖 | ✅ 作为边界节点 | **✅ 需确认** | 发布时须确保以**正确版本**出现在 akasha 中 |
| **third-party(第三方公共库)** | spring、guava 等非自研库 | ❌ **不进 DAG / 不展示** | 否 | 已在 akasha 中**直接可用、无需更新** |

判定依据:
- **internal**:GA 能映射到产品树内启用 devops 的某 repo(DAS 返回的 repo→子项目清单直接给出归属,无需猜测)。
- **third-party**:GA 不属于自研 group 命名空间(如非 `com.csdc.*`),视为公共库;它们在 akasha 已有稳定条目,GPS 既不发布也不要求确认,**直接当作已就绪依赖,不纳入 DAG**。
- **pending-external**:GA 属于自研命名空间、但映射不到任何启用 devops 的 repo(即自研却未上 devops)。这类必须保留在图里并经人工确认其在 akasha 中已是正确版本——否则下游 internal 模块会拉到过期/缺失的版本。

> 术语对齐:后文出现的 "external" 一律指 **pending-external**(需确认的自研未-devops 模块),不含第三方库。

### 4.1 internal 节点再细分:是否被跨 repo 消费(是否回写 akasha)

internal 节点按"是否参与跨 repo 边"再分两类(与 §3.1 对应):

- **published(跨 repo 已发布)**:被其他 repo 以二方包消费 → 发布成功后**回写 akasha**(在指定的 akasha 分支上登记新 GAV),下游从 akasha 拉取其最新版本。
- **repo-internal(仅 repo 内)**:只被同 repo 的子项目 `project()` 引用、从不被跨 repo 消费 → **不回写 akasha**;随所在 repo 的本次 tag 构建,版本由该次 build 决定。

> 这是 internal 节点的"akasha 可见性"维度,与 §4 的三分类正交。

### 边界子项目的特例:仓库内未发布的子项目
某些子项目只被同仓库 `project()` 引用、从不发布到 Maven(即上面的 repo-internal)。它仍需要一个 GA 以保证图统一:取其声明的 `group:artifact`;若确无坐标,则合成 `<repo-group>:<gradlePath 派生 artifact>`,标记为"仓库内、不单独发布、不回写 akasha"。这类节点参与拓扑但随仓库一起打 tag,不单独触发流水线。

## 5. pending-external 节点处理:保留 + 人工预发布确认

- DAG **保留 internal 与 pending-external 节点**(第三方库不进图)。pending-external 参与拓扑排序,作为下游 internal 模块的前置约束。
- **预发布确认动作**:在进入并发池发布(Phase 3)前,GPS 列出本次涉及的所有 pending-external 节点,要求人工确认"这些自研但未 devops 的模块已以**正确版本**出现在(本次指定的)akasha 分支中"。
- 确认后,这些节点被置为**已满足上游**(pre-satisfied),使 `waitForUpstream` 放行其下游;GPS **不会**对它们打 tag 或触发流水线。
- 未确认前,依赖了 pending-external 节点的 internal 模块不得开始发布。
- 第三方库不在此流程内:它们在 akasha 已直接可用,既不进图也不需确认。

### 5.1 akasha 分支是发布前置(必填)

akasha **按分支管理依赖**(每个分支是一份独立的依赖清单)。因此:
- **每次发布必须先指定 akasha 分支**(对应 ReleasePlan 的 `DmsBranch`,提升为创建计划时的必填前置)。
- 拉取依赖清单(下游构建注入)、回写新版本(上游发布后登记)、pending-external 的"正确版本"确认,**全部针对该指定分支**。
- 不同分支的依赖快照相互隔离,支撑并行的多套发布线 / 闪回。

## 6. 对现有实现的影响(实现要点)

### 6.1 数据模型 `model.Module`
```go
type Module struct {
    ID             string // = GA, 规范 "group:artifact"
    Group          string
    Artifact       string // = akasha 的短名 Name(join key,见 §8.3)
    GradlePath     string // ":core:api",仓库内定位符;pending-external 为 ""
    RepoID         string // pending-external 为 ""
    SiloID         string // pending-external 为 ""
    Name           string // 展示名
    Kind           string // "internal" | "pending-external"  (third-party 不进图)
    CurrentVersion string // internal 由本次发布产出;pending-external 为 akasha 中已确认版本
}
```
- `DepEdge{From,To}`、`DependencyGraph` 结构不变,节点 id 改为 GA。
- 新增模块状态用于 pending-external 预确认,例如 `StatusExternalConfirmed`(在 `waitForUpstream` 中等价于 SUCCESS 放行),或复用 SUCCESS + `Kind=="pending-external"` 区分渲染。
- third-party 库在归一化阶段即被过滤,不产生 Module 节点。

### 6.2 DAS 接口(design.md §5.2)细化
- 输入:本次 plan 范围的 repo 列表(本次 tag + 发布分支)+ 指定的 akasha 分支。
- 输出:每个 repo 的子项目清单 `[{gradle_path, group, artifact}]` + 已归一化的 `edges:[{from_ga, to_ga}]` + 引用到的非 internal GA 集合。
- GPS 合并所有 repo 输出,按 GA 归类三种节点(§4):internal / pending-external / third-party;third-party 直接丢弃不进图。
- **环检测(必做)**:构图后对模块级 DAG 做环检测;**若存在环,则不允许进入后续发布**,Phase 2 直接失败并定位环上的模块链(见 §6.4)。
- akasha 坐标(group/artifact/version)与 GA 对齐,Phase 3 拉取/回写时直接复用。

### 6.3 拓扑与调度 `simulator`
- `waitForUpstream` 逻辑不变;只需在发布开始前,把已确认的 pending-external 节点状态预置为"已满足"。
- 拓扑排序 `TopologicalSort` 对 GA 节点无差别运行;排序前若发现剩余未入队节点(存在环)即判定失败。

### 6.4 发布流程新增门控
- **环检测门控**:Phase 2 构图后若检测到模块级环,直接报错并展示环路径(`A → B → … → A`),阻止进入 Phase 3。
- **pending-external 确认门控**:在 Phase 2 之后、Phase 3(并发池)之前,列出全部 pending-external 节点要求人工确认其已在指定 akasha 分支中为正确版本。
- 前端发版监控页:DAG 中 pending-external 节点用不同样式(如虚线/灰色)标注,提供"确认已就绪"操作;third-party 不出现在图中。

### 6.5 mock 数据
- 现有 `GenerateModulesForRepos` 改为产出带 GA 的 internal 模块:`group` 由 silo/repo 派生(如 `com.csdc.<silo>`),`artifact` 由 repo + gradlePath 派生;再合成少量 **pending-external** 节点(自研未-devops)用于演示确认门控。third-party 依赖在 mock 中可直接略去(或仅用于演示"被忽略")。

## 7. 标识串示例

| 场景 | build 文件中的写法 | 归一化结果 |
|------|-------------------|-----------|
| 项目间依赖(internal) | `implementation("com.csdc.spot:issuance-core:1.2.3")` | 节点 `com.csdc.spot:issuance-core`(Kind=internal) |
| 项目内依赖 | `implementation(project(":core:api"))` | 查表 → `com.csdc.spot:issuance-core-api`(internal,repo 内边) |
| 第三方依赖 | `implementation("org.springframework:spring-core:6.1.0")` | **丢弃,不进图**(third-party,akasha 直接可用) |
| 自研未-devops 模块 | `implementation("com.csdc.legacy:foo:2.0.0")` | 节点 `com.csdc.legacy:foo`(Kind=pending-external,需确认) |

## 8. 版本传播:akasha 作为 DMS(发布时引用前序模块最新版本)

发布是按 DAG 顺序进行的,先发布的模块产出新版本后,**靠后的模块在构建时必须引用这些前序模块此刻的最新版本**。这一能力由 `workspaces/akasha` 项目承担——它正是设计文档里 **DMS(依赖管理系统)** 的真实实现。

### 8.1 akasha 是什么

集中式 Gradle 依赖版本登记中心(Go + Gin + MySQL)。它**不改写任何 build 文件**,而是作为**跨 repo 二方包**(被其他 repo 消费的已发布 internal 模块)GAV 版本的"单一事实来源",按分支输出一份 Gradle 可直接 `apply from:` 的依赖清单。

**边界(关键)**:akasha 中登记的模块是"全部 repo 模块"的**子集**——只有跨 repo 被消费的已发布模块才进 akasha(见 §3.1 / §4.1)。**repo 内部依赖不经过 akasha**,由 Gradle 在单个 build 内自行解析、随同一次构建确定版本。

核心数据模型:
- `Dependency`:`Name`(短名,如 `spring-core`)+ `GroupID` + `Artifact` + `Version` + `Branch` + `CreatedAt`;`MavenCoord()` = `group:artifact:version`。
- `Branch`:依赖集合的命名空间(如 `main`、`202603`),与 GPS 的发布分支 / DMS 分支同层概念。
- **append-only**:更新版本不改旧记录,而是 INSERT 一条新 `CreatedAt` 记录(`CreateOrUpdate`),旧版本全保留 → 天然版本历史 + "闪回"能力(`deps-at?at=时间` 取某时间点快照)。

### 8.2 版本传播机制("先写库 → 后读库")

```
模块 A 发布成功 ──POST /api/v1/dependencies──▶ akasha(A 的新 GAV 立即成为该分支最新)
                                                    │
模块 B(A 的下游)构建前 ──GET .../deps-text──────────┘  拉到含 A 最新版本的清单
                       注入构建,B 引用 A 的最新版本
```

- 登记:模块发布成功后 `POST /api/v1/dependencies`,把自己的 `group:artifact:version` 写进目标分支。
- 拉取:下游模块构建时 `GET /api/v1/branches/{branch}/deps-text`(或 `apply from: .../dependency?branch=xxx`)拿到:
  ```
  ext.libraries = [
    "spring-core": "org.springframework:spring-core:6.2.7",
    ...
  ]
  ```
  `FindLatestByBranch` 用 `MAX(id) GROUP BY name` 取每个依赖的最新记录。
- build.gradle 里依赖写成 `libraries["xxx"]`,**版本不写死**,全部由这份动态清单注入。于是"前序模块影响后序模块"通过先写后读自然完成,无需改写下游 build 文件。

### 8.3 与 GA 标识方案的衔接(join key = artifact 短名)

- akasha 用 `GroupID + Artifact` 标识依赖、版本独立成列 —— 与本设计"**GA 为主键、版本 GPS 管理**"完全一致。
- 一条 akasha `Dependency` = GPS DAG 中一个 published internal 节点的"已发布版本坐标"。
- **join key 已定 = artifact 短名(GAV 里的 `a`)**:akasha 的 `Name` 即 artifact。GPS 用 GA(`group:artifact`)做节点主键,与 akasha 关联时以 **artifact(a)** 为键。
  - 约束:同一 akasha 分支内 artifact 必须唯一(GPS 侧 internal 模块的 artifact 也应全局唯一),否则 join 会歧义。构图时若发现 artifact 冲突需报错。
  - 拉取的 `libraries["<artifact>"]` 键与 build 文件引用一致;回写时以 artifact 定位条目、追加新版本。

### 8.4 Phase 3 发布单元(每个模块 = 每个 DAG 节点)

发布编排的单元是**模块(GA 节点)**,不是 repo(见 §2.1:repo 级可能成环,模块级无环)。tag 仍是 repo 级的,但不构成构建/拓扑单元。每个 internal 模块按拓扑就绪后执行:

```
1. 拉取依赖清单:GET akasha /branches/{branch}/deps-text  → 注入本模块构建
                 (仅解析跨 repo 依赖;repo 内依赖由 Gradle 自身解析)
2. checkout 本次 tag → 构建 / 测试 / 发布该模块制品(CI/CD)
3. 回写版本:若该模块是 published(被跨 repo 消费),POST akasha /dependencies 登记新 GAV
            (repo-internal 模块不回写)
4. 标记 SUCCESS → 解锁下游模块(waitForUpstream 放行)
```

- tag 不在此处打:本次发布的全部 repo tag 已在发布开始前统一打完(§2.2 第 1 步),所有模块都基于这组 tag 的源码构建。
- repo-internal 节点(§4.1):不拉取、不回写 akasha,但仍作为模块节点参与拓扑与构建。
- external 节点:GPS 不执行上述单元,其版本由人工预确认其"已在 akasha / 制品库中存在可解析版本"(见 §5)。
- GPS 拓扑 + 并发池保证"上游模块先发布、版本先登记",akasha 保证"下游模块构建时读到最新的跨 repo 版本",两者咬合。源码由 §2.2 的统一 tag 冻结,过程中只有跨 repo 依赖版本在变。

## 9. 本设计待落地的文档/代码改动清单

**文档**
- `design.md` §3.2 依赖图、§5.2 DAS 接口(+环检测)、§5.4 DMS(明确由 akasha 承担、artifact 为 join key、按分支管理)、§6 数据结构、新增"统一打 tag 冻结快照"与"pending-external 确认"流程阶段、Phase 3 发布单元补充"拉清单→构建→回写版本"。

**代码(后续实现,本阶段仅设计)**
- `internal/model/model.go`:扩展 `Module`(Group/Artifact/GradlePath/Kind=internal|pending-external),新增 pending-external 确认相关状态/请求体;ReleasePlan 的 `DmsBranch`(= akasha 分支)提升为创建必填。
- `internal/mock/generator.go`:产出带 GA 的 internal 模块 + 合成少量 pending-external 节点;third-party 不进图。
- DAS 客户端(未来真实对接):基于本次 tag 返回子项目清单 + 归一化边 + 非 internal GA 集合;含模块级环检测。
- akasha(DMS)客户端(未来真实对接):按指定分支拉取依赖清单(键=artifact)+ 回写 published 模块新 GAV。
- `internal/mock/simulator.go` / store:Phase 1 统一打 tag;Phase 2 构图 + 环检测(有环则失败);Phase 2→3 之间 pending-external 确认门控;每个模块发布单元内模拟"拉清单→构建→回写";确认后预置 pending-external 节点状态。
- 前端 `dag-graph.js` / `release-monitor.js`:pending-external 节点样式 + 确认操作;环检测失败的提示与环路径展示。

> 本文档只确立**标识方案与流程思路**;具体编码在后续迭代逐项落地。
