package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthHandler 处理健康检查请求。
type HealthHandler struct {
	Version string
}

// NewHealthHandler 创建健康检查处理器。
func NewHealthHandler(version string) *HealthHandler {
	return &HealthHandler{Version: version}
}

// Health 响应 GET /das/health。
//
// 用于 K8s 存活探针和就绪探针。
// 响应示例: {"status": "ok", "version": "0.1.0"}
func (h *HealthHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": h.Version,
	})
}
