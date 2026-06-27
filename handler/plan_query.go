package handler

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/telepathy/loom/db"
	"github.com/telepathy/loom/model"
	"github.com/telepathy/loom/store"
)

// PlanQueryHandler 处理历史分析查询请求。
type PlanQueryHandler struct {
	store    *store.Store
	db       *sql.DB // nil 时仅查询内存
}

// NewPlanQueryHandler 创建查询处理器。
func NewPlanQueryHandler(store *store.Store, db *sql.DB) *PlanQueryHandler {
	return &PlanQueryHandler{store: store, db: db}
}

// ListPlans 处理 GET /das/plans。
// 优先从 DB 查询历史数据，DB 不可用时返回内存中的计划。
func (h *PlanQueryHandler) ListPlans(c *gin.Context) {
	if h.db != nil {
		plans, err := store.QueryPlans(c.Request.Context(), h.db)
		if err == nil {
			c.JSON(http.StatusOK, gin.H{"plans": plans})
			return
		}
		// DB 查询失败，fallback 到内存
	}

	// 内存 fallback：遍历 store 中的计划（简化版，仅含基本信息）
	type memPlan struct {
		PlanID       string `json:"plan_id"`
		AkashaBranch string `json:"akasha_branch"`
		Status       string `json:"status"`
		RepoCount    int    `json:"repo_count"`
	}
	// store 没有 list 方法，返回空列表提示配置 DB
	c.JSON(http.StatusOK, gin.H{
		"plans":  []memPlan{},
		"notice": "Configure DAS_MYSQL_DSN to persist and query analysis history.",
	})
}

// PlanDetail 处理 GET /das/plans/:plan_id。
// 优先从 DB 查询，DB 不可用时 fallback 到内存。
func (h *PlanQueryHandler) PlanDetail(c *gin.Context) {
	planID := c.Param("plan_id")

	if h.db != nil {
		detail, err := store.QueryPlanDetail(c.Request.Context(), h.db, planID)
		if err == nil && detail != nil {
			c.JSON(http.StatusOK, detail)
			return
		}
		if err == nil && detail == nil {
			c.JSON(http.StatusNotFound, model.ErrorResponse{Error: "plan " + planID + " not found"})
			return
		}
		// DB 查询失败，fallback 到内存
	}

	// 内存 fallback
	plan, ok := h.store.GetPlan(planID)
	if !ok {
		c.JSON(http.StatusNotFound, model.ErrorResponse{Error: "plan " + planID + " not found"})
		return
	}

	// 将内存状态转为 PlanDetail 格式
	repos := make([]store.RepoDetail, 0, len(plan.Repos))
	for _, rs := range plan.Repos {
		ref := rs.Branch
		if ref == "" {
			ref = rs.Tag
		}
		repos = append(repos, store.RepoDetail{
			RepoID:      rs.RepoID,
			RepoURL:     rs.RepoURL,
			Ref:         ref,
			Status:      string(rs.Status),
			Error:       rs.Error,
			StartedAt:   rs.StartedAt,
			FinishedAt:  rs.FinishedAt,
			Subprojects: rs.Subprojects,
			Edges:       rs.Edges,
		})
	}

	c.JSON(http.StatusOK, store.PlanDetail{
		PlanSummary: store.PlanSummary{
			PlanID:       plan.PlanID,
			AkashaBranch: plan.AkashaBranch,
			Status:       string(plan.Status),
			RepoCount:    len(plan.Repos),
		},
		Repos: repos,
	})
}

// ListRepos 处理 GET /das/repos。
func (h *PlanQueryHandler) ListRepos(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, model.ErrorResponse{Error: "DAS_MYSQL_DSN not configured"})
		return
	}
	rows, err := db.QueryRepos(c.Request.Context(), h.db, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.ErrorResponse{Error: "failed to query repos: " + err.Error()})
		return
	}
	type repoItem struct {
		ID            string `json:"id"`
		SiloID        string `json:"silo_id"`
		Name          string `json:"name"`
		ReleaseBranch string `json:"release_branch"`
	}
	items := make([]repoItem, len(rows))
	for i, r := range rows {
		items[i] = repoItem{ID: r.ID, SiloID: r.SiloID, Name: r.Name, ReleaseBranch: r.ReleaseBranch}
	}
	c.JSON(http.StatusOK, gin.H{"repos": items})
}
