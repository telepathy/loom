// Loom — 依赖分析系统（DAS, Dependency Analysis System）
//
// Loom 是 GPS（全局版本发布系统）的依赖分析子系统。
// 职责：给定一组仓库及其本次发布的 tag，拉起 K8s Job 运行 Gradle init script，
// 分析出每个仓库的子项目清单和原始依赖边，供 GPS 做归一化、拓扑排序与环检测。
//
// 执行模式：
//
//	本地模式（DAS_LOCAL_MODE=true）:
//	  直接 exec gradlew，结果返回给 Manager 写入 store。
//	  无需 K8s，无需 HTTP 回调，适合本地开发调试。
//
//	K8s 模式（默认，Phase 3 实现）:
//	  创建 K8s Job（initContainer clone + container gradle evaluate）。
//	  Job 完成后 curl 回调 /das/callback 将结果写入 store。
//	  Manager watch Job 完成，无需关心回调细节。
//
// 驱动模式：
//
//	GPS 驱动（POST /das/analyze）:
//	  GPS 在统一打 tag 后调用，传入 repo 列表和 tag，DAS 以 tag 为目标分析。
//
//	自驱动（POST /das/analyze/self）:
//	  DAS 从共享数据库 gps_repos 表读取仓库列表及其发布分支，
//	  以分支 HEAD 为目标分析，无需 GPS 事先打 tag。
package main

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/telepathy/loom/config"
	"github.com/telepathy/loom/db"
	"github.com/telepathy/loom/handler"
	"github.com/telepathy/loom/job"
	"github.com/telepathy/loom/model"
	"github.com/telepathy/loom/store"
)

//go:embed all:static
var staticFiles embed.FS

// version 由构建时 -ldflags 注入，默认 "dev"。
var version = "dev"

func main() {
	cfg := config.Load()
	log.Printf("Loom DAS %s starting (port=%d, max_parallel=%d, local_mode=%v)\n",
		version, cfg.Port, cfg.MaxParallel, cfg.LocalMode)

	// ── 内存存储 ──
	memStore := store.New(time.Duration(cfg.PlanTTL) * time.Second)

	// ── 数据库（可选，用于持久化 + 自驱动分析） ──
	var database *sql.DB
	var persister store.Persister
	if cfg.MySQLDSN != "" {
		var err error
		database, err = db.Open(cfg.MySQLDSN)
		if err != nil {
			log.Fatalf("db: failed to open MySQL: %v", err)
		}
		log.Println("db: MySQL connected (persistence + self-analyze enabled)")
		defer database.Close()

		// 自动建表
		if err := db.Migrate(database); err != nil {
			log.Fatalf("db: migration failed: %v", err)
		}

		persister = store.NewSQLPersister(database)
	} else {
		log.Println("db: DAS_MYSQL_DSN not set — persistence disabled")
		persister = store.NewSQLPersister(nil) // noop
	}

	// ── Executor（本地模式 或 K8s 模式） ──
	var exe job.Executor
	if cfg.LocalMode {
		exe = job.NewLocalExecutor("", cfg.AkashaAPIURL)
		log.Println("using LOCAL executor (direct gradlew)")
	} else {
		kubeconfig := os.Getenv("DAS_KUBECONFIG")
		k8sExe, err := job.NewK8sExecutor(cfg, kubeconfig)
		if err != nil {
			log.Printf("WARNING: K8s executor unavailable: %v — falling back to stub", err)
			exe = newK8sExecutor()
		} else {
			exe = k8sExe
			log.Println("using K8s executor (client-go)")
		}
	}

	jobManager := job.NewManager(cfg, memStore, exe, persister)

	// ── Handlers ──
	healthH := handler.NewHealthHandler(version)
	analyzeH := handler.NewAnalyzeHandler(memStore, cfg.MaxParallel, jobManager, database)
	callbackH := handler.NewCallbackHandler(memStore, persister)
	planQH := handler.NewPlanQueryHandler(memStore, database)

	// ── 前端静态文件 ──
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("static: failed to sub filesystem: %v", err)
	}

	// ── Gin 路由 ──
	r := gin.Default()

	// 前端
	r.GET("/", serveIndex(staticFS))
	r.GET("/css/*filepath", func(c *gin.Context) {
		c.FileFromFS("css"+c.Param("filepath"), http.FS(staticFS))
	})
	r.GET("/js/*filepath", func(c *gin.Context) {
		c.FileFromFS("js"+c.Param("filepath"), http.FS(staticFS))
	})
	r.NoRoute(serveIndex(staticFS))

	// API — 分析
	r.GET("/das/health", healthH.Health)
	r.POST("/das/analyze", analyzeH.Analyze)
	r.POST("/das/analyze/self", analyzeH.SelfAnalyze)
	r.GET("/das/analyze/:plan_id", analyzeH.Status)
	r.POST("/das/callback", callbackH.Callback)

	// API — 历史查询
	r.GET("/das/plans", planQH.ListPlans)
	r.GET("/das/plans/:plan_id", planQH.PlanDetail)

	// ── HTTP 服务 ──
	srv := &http.Server{
		Addr:    ":" + itoa(cfg.Port),
		Handler: r,
	}

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Fatalf("forced shutdown: %v", err)
		}
	}()

	log.Printf("listening on :%d\n", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen error: %v", err)
	}
	log.Println("shutdown complete")
}

// serveIndex 返回 index.html。
func serveIndex(staticFS fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		data, err := fs.ReadFile(staticFS, "index.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "index.html not found")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ── K8s Executor 桩（Phase 3 实现真实 client-go） ──

func newK8sExecutor() job.Executor {
	return &k8sExecutorStub{}
}

// k8sExecutorStub 模拟 K8s Job 的创建和监控。
// 仅打印日志模拟执行，然后等待 HTTP callback 更新 store。
type k8sExecutorStub struct{}

func (e *k8sExecutorStub) Execute(ctx context.Context, planID string, rs *model.RepoState, akashaBranch string) (*job.AnalysisResult, error) {
	log.Printf("[K8s stub] simulating analysis for repo %s (tag=%s, branch=%s, jdk=%s)\n",
		rs.RepoID, rs.Tag, rs.Branch, rs.JDK)

	// 模拟分析耗时，可被 ctx 取消
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// K8s 模式：Execute 返回 nil result，store 由 HTTP callback 更新
	log.Printf("[K8s stub] repo %s: simulation complete (awaiting callback)\n", rs.RepoID)
	return nil, nil
}
