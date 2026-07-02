package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/telepathy/loom/job"
	"github.com/telepathy/loom/model"
	"github.com/telepathy/loom/store"
)

// CleanupHandler 处理清理未完成 K8s Job 和关联数据库数据的请求。
type CleanupHandler struct {
	store     *store.Store
	persister store.Persister
	executor  job.Executor
}

// NewCleanupHandler 创建清理处理器。
func NewCleanupHandler(store *store.Store, persister store.Persister, executor job.Executor) *CleanupHandler {
	return &CleanupHandler{store: store, persister: persister, executor: executor}
}

// Cleanup 处理 POST /das/cleanup。
//
// 三阶段清理：
//  1. 删除所有未完成的 K8s Job（label=app=gps-das）
//  2. 移除内存 store 中 IN_PROGRESS 状态的所有 plan
//  3. 删除数据库中 IN_PROGRESS 计划在 loom_* 表的所有数据
//
// 各阶段错误不中断后续阶段，收集到响应的 errors 字段。
func (h *CleanupHandler) Cleanup(c *gin.Context) {
	ctx := c.Request.Context()
	resp := model.CleanupResponse{}

	// Phase 1: 清理 K8s Job
	deleted, err := h.executor.CleanupIncompleteJobs(ctx)
	if err != nil {
		resp.Errors = append(resp.Errors, "k8s: "+err.Error())
		log.Printf("[cleanup] k8s job cleanup error: %v", err)
	}
	resp.K8sJobsDeleted = deleted

	// Phase 2: 清理内存 store 中 IN_PROGRESS 的 plan
	inProgressIDs := h.store.GetInProgressPlanIDs()
	for _, planID := range inProgressIDs {
		h.store.DeletePlan(planID)
		resp.PlansRemoved++
		log.Printf("[cleanup] store: removed plan %s", planID)
	}

	// Phase 3: 清理数据库中关联数据
	for _, planID := range inProgressIDs {
		if err := h.persister.DeletePlanData(ctx, planID); err != nil {
			resp.Errors = append(resp.Errors, "db: plan "+planID+": "+err.Error())
			log.Printf("[cleanup] db: delete plan %s: %v", planID, err)
			continue
		}
		resp.DBPlansDeleted++
		log.Printf("[cleanup] db: deleted plan %s", planID)
	}

	c.JSON(http.StatusOK, resp)
}
