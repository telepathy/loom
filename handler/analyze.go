package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/telepathy/loom/db"
	"github.com/telepathy/loom/model"
	"github.com/telepathy/loom/store"
)

// AnalyzeHandler 处理依赖分析请求。
// 职责：
//   - POST /das/analyze      — 接收 GPS 提交的仓库列表，为每个 repo 创建 K8s Job
//   - POST /das/analyze/self — DAS 自驱动模式，从 gps_repos 表读仓库列表并分析发布分支
//   - GET  /das/analyze/:plan_id — 返回指定 plan 的分析进度和结果
type AnalyzeHandler struct {
	store       *store.Store
	maxParallel int
	jobManager  JobManager // 由 main.go 注入具体实现
	db          *sql.DB    // nil 时自驱动模式不可用
}

// JobManager 是 Loom 操作 K8s Job 的接口抽象，便于 handler 层调用和测试。
type JobManager interface {
	// CreateJobs 为 plan 下所有 repo 创建 K8s Job。
	// 受信号量并发控制（maxParallel），超限时阻塞等待。
	CreateJobs(plan *model.PlanState) error
}

// NewAnalyzeHandler 创建分析处理器。
func NewAnalyzeHandler(store *store.Store, maxParallel int, jm JobManager, db *sql.DB) *AnalyzeHandler {
	return &AnalyzeHandler{
		store:       store,
		maxParallel: maxParallel,
		jobManager:  jm,
		db:          db,
	}
}

// Analyze 处理 POST /das/analyze。
//
// GPS 提交一批仓库的分析任务。Loom 校验请求后为每个 repo 创建一个 K8s Job，
// 立即返回 plan_id 供 GPS 后续轮询。
//
// 错误响应：
//   - 400：请求体校验失败
//   - 409：同一 plan_id 已有进行中的分析
//   - 500：K8s Job 创建失败
func (h *AnalyzeHandler) Analyze(c *gin.Context) {
	var req model.AnalyzeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: err.Error()})
		return
	}

	// 构建内部状态
	repos := make(map[string]*model.RepoState, len(req.Repos))
	for _, r := range req.Repos {
		jdk := r.JDK
		if jdk == "" {
			jdk = "17" // 默认 JDK 版本，对应 DAS_DEFAULT_JDK
		}
		// Tag 和 Branch 至少需要一个
		if r.Tag == "" && r.Branch == "" {
			c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: "repo " + r.RepoID + ": tag or branch is required"})
			return
		}
		repos[r.RepoID] = &model.RepoState{
			RepoID:  r.RepoID,
			RepoURL: r.RepoURL,
			Tag:     r.Tag,
			Branch:  r.Branch,
			JDK:     jdk,
			Status:  model.RepoPending,
		}
	}

	plan := &model.PlanState{
		PlanID:       req.PlanID,
		AkashaBranch: req.AkashaBranch,
		CallbackBase: req.CallbackBaseURL,
		Repos:        repos,
		Status:       model.PlanInProgress,
		CreatedAt:    time.Now(),
	}

	// 尝试写入 store。若已存在且为 IN_PROGRESS 则返回 409。
	if ok := h.store.CreatePlan(plan); !ok {
		if existing, exists := h.store.GetPlan(req.PlanID); exists && existing.Status == model.PlanInProgress {
			c.JSON(http.StatusConflict, model.ErrorResponse{Error: "plan " + req.PlanID + " already in progress"})
			return
		}
		// 已 COMPLETED → 覆盖重新开始
		h.store.ReplacePlan(plan)
	}

	// 异步创建 K8s Job（后台 goroutine，受信号量控制）。
	go func() {
		if err := h.jobManager.CreateJobs(plan); err != nil {
			// Job 创建失败由 job.Manager 内部处理（标记 repo FAILED）
			// handler 层不需要额外动作，GPS 轮询时会看到 FAILED 状态。
		}
	}()

	c.JSON(http.StatusAccepted, model.AnalyzeResponse{
		PlanID:    req.PlanID,
		Status:    "ACCEPTED",
		RepoCount: len(req.Repos),
		Message:   "Analysis started. Poll GET /das/analyze/" + req.PlanID + " for status.",
	})
}

// SelfAnalyze 处理 POST /das/analyze/self。
//
// DAS 自驱动模式：从 gps_repos 表读取仓库列表，以 GPS 维护的发布分支为分析目标。
// 无需 GPS 事先打 tag，直接分析发布分支 HEAD。
//
// 请求体：
//
//	{
//	  "silo_ids": ["silo-001"],       // 可选，空则分析全部
//	  "akasha_branch": "202603"       // 必填
//	}
//
// 响应：202 {plan_id, status: "ACCEPTED", repo_count, message}
//
// 错误响应：
//   - 400：请求体校验失败
//   - 503：数据库未配置（DAS_MYSQL_DSN 未设置）
//   - 500：数据库查询或 Job 创建失败
func (h *AnalyzeHandler) SelfAnalyze(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, model.ErrorResponse{Error: "self-analyze not available: DAS_MYSQL_DSN not configured"})
		return
	}

	var req model.SelfAnalyzeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: err.Error()})
		return
	}

	// 查询 gps_repos 表
	repoRows, err := db.QueryRepos(c.Request.Context(), h.db, req.SiloIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.ErrorResponse{Error: "failed to query repos: " + err.Error()})
		return
	}
	if len(repoRows) == 0 {
		c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: "no repos found (check silo_ids filter or gps_repos table)"})
		return

		// 如果指定了 repo_ids，精确过滤
		if len(req.RepoIDs) > 0 {
			idSet := make(map[string]bool, len(req.RepoIDs))
			for _, id := range req.RepoIDs {
				idSet[id] = true
			}
			filtered := repoRows[:0]
			for _, r := range repoRows {
				if idSet[r.ID] {
					filtered = append(filtered, r)
				}
			}
			repoRows = filtered
		}

		if len(repoRows) == 0 {
			c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: "no repos found (check filter or gps_repos table)"})
			return
		}
	}

	// 生成 plan_id
	planID := fmt.Sprintf("das-self-%s", time.Now().Format("20060102-150405"))

	// 构建内部状态：每个 repo 以 release_branch 为分析目标
	repos := make(map[string]*model.RepoState, len(repoRows))
	for _, r := range repoRows {
		repos[r.ID] = &model.RepoState{
			RepoID:  r.ID,
			RepoURL: r.URL,
			Branch:  r.ReleaseBranch, // 发布分支作为分析目标
			JDK:     r.JDK,           // 从 gps_repos 读取的 JDK 版本
			Status:  model.RepoPending,
		}
	}

	plan := &model.PlanState{
		PlanID:       planID,
		AkashaBranch: req.AkashaBranch,
		Repos:        repos,
		Status:       model.PlanInProgress,
		CreatedAt:    time.Now(),
	}

	// 写入 store（自驱动 plan_id 由时间戳生成，冲突概率极低）
	h.store.ReplacePlan(plan)

	// 异步执行分析
	go func() {
		if err := h.jobManager.CreateJobs(plan); err != nil {
			// 错误由 Manager 内部记录
		}
	}()

	c.JSON(http.StatusAccepted, model.AnalyzeResponse{
		PlanID:    planID,
		Status:    "ACCEPTED",
		RepoCount: len(repoRows),
		Message:   "Self-analyze started. Poll GET /das/analyze/" + planID + " for status.",
	})
}

// Status 处理 GET /das/analyze/:plan_id。
//
// GPS 轮询此端点获取分析进度和结果。Loom 从内存 store 中读取当前状态返回。
//
// 响应：
//   - status=IN_PROGRESS → 继续轮询（建议间隔 2-5s）
//   - status=COMPLETED + 所有 repo DONE → 完整结果可消费
//   - status=COMPLETED + 任一 repo FAILED → GPS 让 Phase 2 失败
//   - 404：plan_id 不存在或已过期
func (h *AnalyzeHandler) Status(c *gin.Context) {
	planID := c.Param("plan_id")
	plan, ok := h.store.GetPlan(planID)
	if !ok {
		c.JSON(http.StatusNotFound, model.ErrorResponse{Error: "plan " + planID + " not found or already expired"})
		return
	}

	repos := make([]model.RepoResult, 0, len(plan.Repos))
	for _, rs := range plan.Repos {
		r := model.RepoResult{
			RepoID: rs.RepoID,
			Status: rs.Status,
		}
		switch rs.Status {
		case model.RepoDone:
			r.Subprojects = rs.Subprojects
			r.Edges = rs.Edges
		case model.RepoFailed:
			r.Error = rs.Error
		case model.RepoRunning:
			r.Message = "Job running"
		}
		repos = append(repos, r)
	}

	c.JSON(http.StatusOK, model.AnalyzeStatusResponse{
		PlanID: planID,
		Status: plan.Status,
		Repos:  repos,
	})
}
