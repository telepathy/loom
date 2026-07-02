// Package model 定义了 Loom 的全部数据模型：API 请求/响应结构体、内部状态结构体。
package model

import "time"

// ──────────────────────────────────────────────
// 状态枚举
// ──────────────────────────────────────────────

// PlanStatus 表示一个分析计划的整体状态。
type PlanStatus string

const (
	PlanInProgress PlanStatus = "IN_PROGRESS" // 至少一个 repo 仍在分析中
	PlanCompleted  PlanStatus = "COMPLETED"   // 所有 repo 已终态（DONE 或 FAILED）
)

// RepoStatus 表示单个仓库的分析状态。
type RepoStatus string

const (
	RepoPending RepoStatus = "PENDING" // Job 已创建，等待调度
	RepoRunning RepoStatus = "RUNNING" // Job 正在执行（clone + gradle 评估）
	RepoDone    RepoStatus = "DONE"    // 分析成功，subprojects 和 edges 可用
	RepoFailed  RepoStatus = "FAILED"  // 分析失败，error 字段包含原因
)

// ──────────────────────────────────────────────
// API 请求/响应（§2）
// ──────────────────────────────────────────────

// AnalyzeRequest 是 POST /das/analyze 的请求体。
// GPS 提交一批仓库的分析任务，Loom 为每个 repo 创建一个 K8s Job。
type AnalyzeRequest struct {
	PlanID          string      `json:"plan_id" binding:"required"`
	AkashaBranch    string      `json:"akasha_branch" binding:"required"`
	CallbackBaseURL string      `json:"callback_base_url" binding:"required"`
	Repos           []RepoInput `json:"repos" binding:"required"`
}

// RepoInput 描述待分析的一个仓库。
type RepoInput struct {
	RepoID  string `json:"repo_id" binding:"required"`
	RepoURL string `json:"repo_url" binding:"required"`
	Tag     string `json:"tag"`           // 分析目标 tag。与 Branch 二选一：GPS 驱动模式用 tag
	Branch  string `json:"branch"`        // 分析目标分支。DAS 自驱动模式用 branch
	JDK     string `json:"jdk,omitempty"` // 默认为 DefaultJDK (17)
}

// SelfAnalyzeRequest 是 POST /das/analyze/self 的请求体。
// DAS 自驱动模式：从 gps_repos 表读取仓库列表，以发布分支为分析目标。
type SelfAnalyzeRequest struct {
	SiloIDs      []string `json:"silo_ids"`                        // 可选：按 silo 过滤
	RepoIDs      []string `json:"repo_ids"`                        // 可选：按 repo_id 精确选择
	AkashaBranch string   `json:"akasha_branch" binding:"required"` // akasha 依赖分支
}

// AnalyzeResponse 是 POST /das/analyze 成功时的响应体（202 Accepted）。
type AnalyzeResponse struct {
	PlanID    string `json:"plan_id"`
	Status    string `json:"status"`    // "ACCEPTED"
	RepoCount int    `json:"repo_count"`
	Message   string `json:"message"`
}

// AnalyzeStatusResponse 是 GET /das/analyze/:plan_id 的响应体。
// 轮询时返回分析进度和结果。
type AnalyzeStatusResponse struct {
	PlanID string       `json:"plan_id"`
	Status PlanStatus   `json:"status"` // IN_PROGRESS | COMPLETED
	Repos  []RepoResult `json:"repos"`
}

// RepoResult 描述单个仓库的分析结果。
type RepoResult struct {
	RepoID      string       `json:"repo_id"`
	Status      RepoStatus   `json:"status"` // DONE | FAILED | RUNNING | PENDING
	Subprojects []Subproject `json:"subprojects,omitempty"`
	Edges       []RawEdge    `json:"edges,omitempty"`
	Error       string       `json:"error,omitempty"`
	Message     string       `json:"message,omitempty"`
}

// Subproject 对应 das-output.json 中单个子项目的坐标信息。
type Subproject struct {
	GradlePath string `json:"gradle_path"`
	Group      string `json:"group"`
	Artifact   string `json:"artifact"`
	Version    string `json:"version,omitempty"` // 原始版本，仅作审计，GPS 以自己管理的版本为准
}

// RawEdge 是 Gradle init script 产出的原始边，类型不统一。
// GPS 负责归一化：type=project 查本 repo gradlePath→GA 表；type=external 剥版本取 GA。
//
// 注意：原始边 from 的格式不统一——
//
//	type=project:  from 是 gradlePath（如 ":core:model"）
//	type=external: from 是 GAV（如 "com.csdc.settle:settle-client:1.4.0"）
type RawEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"` // "project" | "external"
}

// CallbackRequest 是 K8s Job 通过 POST /das/callback 回传的内容。
// 即 das-output.json 的完整结构。
type CallbackRequest struct {
	RepoID      string       `json:"repo_id"`
	Tag         string       `json:"tag"`
	Subprojects []Subproject `json:"subprojects"`
	Edges       []RawEdge    `json:"edges"`
	// 当 Gradle 评估失败时，Job 回传 error 字段而非上述结构。
	Error string `json:"error,omitempty"`
}

// CallbackResponse 是 POST /das/callback 成功时的响应。
type CallbackResponse struct {
	Status string `json:"status"`
	PlanID string `json:"plan_id"`
	RepoID string `json:"repo_id"`
}

// ErrorResponse 是通用的错误响应体。
type ErrorResponse struct {
	Error string `json:"error"`
}

// CleanupResponse 是 POST /das/cleanup 的响应体。
type CleanupResponse struct {
	K8sJobsDeleted int      `json:"k8s_jobs_deleted"`
	PlansRemoved   int      `json:"plans_removed"`
	DBPlansDeleted int      `json:"db_plans_deleted"`
	Errors         []string `json:"errors,omitempty"`
}

// ──────────────────────────────────────────────
// 内部状态（§7.3）
// ──────────────────────────────────────────────

// PlanState 跟踪一个 plan_id 下所有 repo 的分析状态。
type PlanState struct {
	PlanID       string
	AkashaBranch string
	CallbackBase string
	Repos        map[string]*RepoState // repo_id → 状态
	Status       PlanStatus            // IN_PROGRESS | COMPLETED
	CreatedAt    time.Time
	// 所有 repo 达到终态的时间。用于计算 TTL 过期。
	CompletedAt *time.Time
}

// RepoState 跟踪单个 repo 的分析状态。
type RepoState struct {
	RepoID     string
	RepoURL    string
	Tag        string       // GPS 驱动模式：分析目标 tag
	Branch     string       // DAS 自驱动模式：分析目标发布分支
	JDK        string
	JobName    string       // K8s Job 名称
	Status     RepoStatus   // PENDING | RUNNING | DONE | FAILED
	Subprojects []Subproject // DONE 时填充
	Edges      []RawEdge    // DONE 时填充
	Error      string       // FAILED 时填充
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// JobName 构造该 repo 对应的 K8s Job 名称。
// 格式：das-<repo_id>-<plan_id>，其中不合 K8s 资源名规范的字符会被替换。
func (rs *RepoState) JobNameStr(planID string) string {
	// Job 名称要求符合 DNS-1123 子域名规范。
	// plan_id 和 repo_id 来自 GPS，通常已是合法标识符（如 "plan-001"、"repo-0012"）。
	return "das-" + rs.RepoID + "-" + planID
}
