package handler

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/telepathy/loom/model"
	"github.com/telepathy/loom/store"
)

// CallbackHandler 处理 K8s Job 的回调请求。
type CallbackHandler struct {
	store     *store.Store
	persister store.Persister
}

// NewCallbackHandler 创建回调处理器。
func NewCallbackHandler(store *store.Store, persister store.Persister) *CallbackHandler {
	return &CallbackHandler{store: store, persister: persister}
}

// Callback 处理 POST /das/callback。
//
// K8s Job 内的 curl 调用，回传单个 repo 的 das-output.json。
// Loom 在内存中按 plan_id 暂存结果，并持久化到 DB。
//
// 查询参数：
//   - plan_id: 关联的计划 ID
//   - repo_id: 关联的仓库 ID
//   - tag:     分析的 tag（幂等校验用）
//
// 请求体即 das-output.json 的内容（CallbackRequest）。
func (h *CallbackHandler) Callback(c *gin.Context) {
	planID := c.Query("plan_id")
	repoID := c.Query("repo_id")
	tag := c.Query("tag")

	if planID == "" || repoID == "" {
		c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: "plan_id and repo_id query parameters are required"})
		return
	}

	// 校验 plan 存在
	plan, ok := h.store.GetPlan(planID)
	if !ok {
		c.JSON(http.StatusNotFound, model.ErrorResponse{Error: "plan " + planID + " not found or already expired"})
		return
	}

	// 解析请求体（可能是 das-output.json 或 error 对象）
	var cb model.CallbackRequest
	if err := c.ShouldBindJSON(&cb); err != nil {
		c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: "invalid callback body: " + err.Error()})
		return
	}

	// 校验 tag 一致性（幂等校验）
	if tag != "" && cb.Tag != "" && tag != cb.Tag {
		c.JSON(http.StatusBadRequest, model.ErrorResponse{Error: "tag mismatch: query=" + tag + " body=" + cb.Tag})
		return
	}

	// 更新 repo 状态
	updated := h.store.UpdateRepoState(planID, repoID, func(rs *model.RepoState) {
		if cb.Error != "" {
			// Gradle 评估失败
			rs.Status = model.RepoFailed
			rs.Error = cb.Error
		} else {
			// 分析成功
			rs.Status = model.RepoDone
			rs.Subprojects = cb.Subprojects
			rs.Edges = cb.Edges
		}
		now := time.Now()
		rs.FinishedAt = &now
	})

	if !updated {
		c.JSON(http.StatusNotFound, model.ErrorResponse{Error: "repo " + repoID + " not found in plan " + planID})
		return
	}

	// 持久化 repo 结果
	if h.persister != nil && plan != nil {
		if rs, ok := plan.Repos[repoID]; ok {
			if err := h.persister.PersistRepoResult(c.Request.Context(), planID, rs); err != nil {
				log.Printf("[callback] persist repo %s/%s: %v", planID, repoID, err)
			}
		}
	}

	// 检查是否所有 repo 都已终态 → 更新 PlanStatus 为 COMPLETED
	if allDone, exists := h.store.AllInTerminalState(planID); exists && allDone {
		h.store.SetPlanCompleted(planID)
		if h.persister != nil {
			if err := h.persister.MarkPlanCompleted(c.Request.Context(), planID); err != nil {
				log.Printf("[callback] persist mark completed %s: %v", planID, err)
			}
		}
	}

	c.JSON(http.StatusOK, model.CallbackResponse{
		Status: "received",
		PlanID: planID,
		RepoID: repoID,
	})
}
