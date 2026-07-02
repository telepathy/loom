// Package job 定义了分析任务的执行契约，以及本地模式和 K8s 模式的执行器。
//
// 执行模式：
//
//	Local 模式（DAS_LOCAL_MODE=true）:
//	  调用 git clone → gradlew --init-script → 解析 das-output.json
//	  Execute 返回分析结果 → Manager 更新 store
//	  无需 K8s，无需 HTTP 回调，全部在当前进程内完成。
//
//	K8s 模式（默认）:
//	  渲染 K8s Job YAML → 创建 Job → watch 完成
//	  Job 内 curl 回调 /das/callback 将结果写入 store
//	  Execute 返回时 store 已为终态（由 callback handler 更新）。
package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/telepathy/loom/config"
	"github.com/telepathy/loom/model"
	"github.com/telepathy/loom/store"
)

// ──────────────────────────────────────────────
// Executor 接口
// ──────────────────────────────────────────────

// AnalysisResult 是单个 repo 分析完成后的结果。
type AnalysisResult struct {
	Subprojects []model.Subproject `json:"subprojects"`
	Edges       []model.RawEdge    `json:"edges"`
}

// Executor 执行单个 repo 的依赖分析任务。
//   - LocaMode: Execute 阻塞直到分析完成，返回 AnalysisResult。Manager 据此更新 store。
//   - K8sMode:  Execute 阻塞直到 K8s Job 完成。结果通过 HTTP 回调写入 store（handler/callback.go），
//     Execute 返回 (nil, nil) 表示成功，(nil, error) 表示失败。
type Executor interface {
	Execute(ctx context.Context, planID string, rs *model.RepoState, akashaBranch string) (*AnalysisResult, error)

	// CleanupIncompleteJobs 删除所有未完成的 K8s Job，返回删除数量。
	// 非 K8s 模式返回 (0, nil)。
	CleanupIncompleteJobs(ctx context.Context) (int, error)
}

// ──────────────────────────────────────────────
// Manager — 计划级协调
// ──────────────────────────────────────────────

// Manager 协调一个 plan 下所有 repo 的分析任务。
// 通过信号量控制并发数，每个 repo 在独立 goroutine 中执行。
type Manager struct {
	cfg       *config.Config
	store     *store.Store
	sem       chan struct{} // 信号量，容量 = DAS_MAX_PARALLEL
	executor  Executor
	persister store.Persister // nil 时跳过持久化
}

// NewManager 创建 Job 管理器。
func NewManager(cfg *config.Config, store *store.Store, executor Executor, persister store.Persister) *Manager {
	return &Manager{
		cfg:       cfg,
		store:     store,
		sem:       make(chan struct{}, cfg.MaxParallel),
		executor:  executor,
		persister: persister,
	}
}

// CreateJobs 为 plan 下所有 repo 执行分析。
//
// 执行模型：
//  1. 为每个 repo 启动一个 goroutine（受信号量控制并发数）
//  2. 每个 goroutine 调用 executor.Execute
//  3. LocalMode: Execute 返回结果 → Manager 更新 store 为 DONE
//  4. K8sMode:   结果由 HTTP 回调写入 store，Execute 仅返回成功/失败
//  5. 全部完成后将 plan 标记为 COMPLETED
func (m *Manager) CreateJobs(plan *model.PlanState) error {
	// 持久化计划初始状态
	if m.persister != nil {
		if err := m.persister.PersistPlan(context.Background(), plan); err != nil {
			log.Printf("[job] persist plan %s: %v", plan.PlanID, err)
		}
	}

	var wg sync.WaitGroup

	for _, rs := range plan.Repos {
		rs := rs

		// 获取信号量槽位（阻塞直到有可用槽位）
		m.sem <- struct{}{}
		wg.Add(1)

		go func() {
			defer wg.Done()
			defer func() { <-m.sem }() // 释放信号量

			// 标记 RUNNING
			now := time.Now()
			m.store.UpdateRepoState(plan.PlanID, rs.RepoID, func(r *model.RepoState) {
				r.Status = model.RepoRunning
				r.StartedAt = &now
			})

			// 带超时的执行上下文
			ctx, cancel := context.WithTimeout(context.Background(),
				time.Duration(m.cfg.JobTimeout)*time.Second)
			defer cancel()

			result, err := m.executor.Execute(ctx, plan.PlanID, rs, plan.AkashaBranch)
			if err != nil {
				m.store.UpdateRepoState(plan.PlanID, rs.RepoID, func(r *model.RepoState) {
					r.Status = model.RepoFailed
					r.Error = err.Error()
					finished := time.Now()
					r.FinishedAt = &finished
				})
				// 持久化失败结果
				if m.persister != nil {
					rs.Status = model.RepoFailed
					rs.Error = err.Error()
					failedAt := time.Now()
					rs.FinishedAt = &failedAt
					if e := m.persister.PersistRepoResult(ctx, plan.PlanID, rs); e != nil {
						log.Printf("[job] persist repo %s/%s FAILED: %v", plan.PlanID, rs.RepoID, e)
					}
				}
				log.Printf("[job] repo %s/%s FAILED: %s\n", plan.PlanID, rs.RepoID, err)
				return
			}

			// Local 模式：result 非空，写入 store
			if result != nil {
				finished := time.Now()
				m.store.UpdateRepoState(plan.PlanID, rs.RepoID, func(r *model.RepoState) {
					r.Status = model.RepoDone
					r.Subprojects = result.Subprojects
					r.Edges = result.Edges
					r.FinishedAt = &finished
				})
				// 持久化成功结果
				if m.persister != nil {
					rs.Status = model.RepoDone
					rs.Subprojects = result.Subprojects
					rs.Edges = result.Edges
					rs.FinishedAt = &finished
					if e := m.persister.PersistRepoResult(ctx, plan.PlanID, rs); e != nil {
						log.Printf("[job] persist repo %s/%s DONE: %v", plan.PlanID, rs.RepoID, e)
					}
				}
			}
			// K8s 模式：result 为 nil，store 已由 callback handler 更新

			log.Printf("[job] repo %s/%s DONE\n", plan.PlanID, rs.RepoID)
		}()
	}

	wg.Wait()

	// 所有 repo 已终态 → 标记 plan COMPLETED
	m.store.SetPlanCompleted(plan.PlanID)
	if m.persister != nil {
		if err := m.persister.MarkPlanCompleted(context.Background(), plan.PlanID); err != nil {
			log.Printf("[job] persist mark completed %s: %v", plan.PlanID, err)
		}
	}
	return nil
}

// ──────────────────────────────────────────────
// LocalExecutor — 本地模式
// ──────────────────────────────────────────────

// LocalExecutor 在本地进程中直接执行分析：
// git clone → chmod 可写 → curl akasha API → 写入 gradle.properties
// → 写入 das.gradle → gradlew --init-script → 解析 das-output.json → 返回结果。
type LocalExecutor struct {
	workDirBase  string // 克隆工作目录基础路径，默认 os.TempDir()
	akashaAPIURL string // akasha gradle.properties API URL，空则跳过
}

// NewLocalExecutor 创建本地执行器。
func NewLocalExecutor(workDirBase, akashaAPIURL string) *LocalExecutor {
	if workDirBase == "" {
		workDirBase = os.TempDir()
	}
	return &LocalExecutor{workDirBase: workDirBase, akashaAPIURL: akashaAPIURL}
}

// Execute 执行本地分析。
func (e *LocalExecutor) Execute(ctx context.Context, planID string, rs *model.RepoState, akashaBranch string) (*AnalysisResult, error) {
	workDir := filepath.Join(e.workDirBase, "loom", planID, rs.RepoID)

	// 清理可能存在的旧目录
	_ = os.RemoveAll(workDir)
	defer os.RemoveAll(workDir)

	// Step 1: git clone --depth 1 --branch <ref>
	// 优先 Branch（自驱动模式），其次 Tag（GPS 驱动模式）
	ref := rs.Branch
	if ref == "" {
		ref = rs.Tag
	}
	log.Printf("[local] cloning %s @ %s into %s\n", rs.RepoURL, ref, workDir)
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1",
		"--branch", ref, rs.RepoURL, workDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %w\n%s", err, string(out))
	}

	// Step 2: 设置目录可写（git clone 的文件默认只读），然后拉取 akasha gradle.properties
	log.Printf("[local] chmod -R u+w %s\n", workDir)
	chmodCmd := exec.CommandContext(ctx, "chmod", "-R", "u+w", workDir)
	if out, err := chmodCmd.CombinedOutput(); err != nil {
		log.Printf("[local] chmod warning: %v\n%s", err, string(out))
	}

	if e.akashaAPIURL != "" {
		propsURL := e.akashaAPIURL + "?depBranch=" + akashaBranch
		propsPath := filepath.Join(workDir, "gradle.properties")
		log.Printf("[local] fetching akasha gradle.properties: %s\n", propsURL)
		curlCmd := exec.CommandContext(ctx, "curl", "-sf", "-o", propsPath, propsURL)
		if out, err := curlCmd.CombinedOutput(); err != nil {
			log.Printf("[local] akasha gradle.properties warning: %v\n%s", err, string(out))
			// 不中断：akasha 不可达时 Gradle 使用 build.gradle 中的版本
		}
	} else {
		log.Printf("[local] DAS_AKASHA_API_URL not set, skipping gradle.properties fetch")
	}

	// Step 3: 写入 das.gradle init script
	initScript := filepath.Join(workDir, "das.gradle")
	if err := os.WriteFile(initScript, []byte(DasInitScript), 0644); err != nil {
		return nil, fmt.Errorf("write das.gradle failed: %w", err)
	}

	// Step 4: 执行 gradlew（不再需要 -PdepBranch，akasha 分支已通过 gradle.properties 注入）
	log.Printf("[local] running gradlew --init-script das.gradle\n")
	gradlew := filepath.Join(workDir, "gradlew")
	_ = os.Chmod(gradlew, 0755) // 确保可执行

	gradleCmd := exec.CommandContext(ctx, gradlew,
		"--init-script", initScript,
		"help", "-q")
	gradleCmd.Dir = workDir

	if out, err := gradleCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gradlew failed: %w\n%s", err, string(out))
	}

	// Step 5: 读取 das-output.json
	outputPath := filepath.Join(workDir, "das-output.json")
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read das-output.json failed: %w", err)
	}

	// Step 5: 解析并返回结果
	var result AnalysisResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse das-output.json failed: %w", err)
	}

	log.Printf("[local] repo %s: %d subprojects, %d edges\n",
		rs.RepoID, len(result.Subprojects), len(result.Edges))
	return &result, nil
}

// CleanupIncompleteJobs 在本地模式下为空操作（无 K8s Job 需要清理）。
func (e *LocalExecutor) CleanupIncompleteJobs(ctx context.Context) (int, error) {
	return 0, nil
}

// ──────────────────────────────────────────────
// DasInitScript — Gradle init script
// ──────────────────────────────────────────────

// DasInitScript 是注入 Gradle 的 init script，在 projectsEvaluated 阶段
// 遍历所有子项目，导出 GA 坐标和 declared 依赖边。
//
// 对应 das_design.md §3.1。与 k8s/configmap.yaml 中内容一致。
const DasInitScript = `
import org.gradle.api.artifacts.ProjectDependency
import org.gradle.api.artifacts.ExternalModuleDependency
import groovy.json.JsonOutput

gradle.projectsEvaluated {
    def out = [subprojects: [], edges: []]

    rootProject.allprojects.each { p ->
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
`
