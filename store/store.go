// Package store 提供内存 PlanState 存储（sync.RWMutex）。
//
// Loom 无数据库，状态仅存在于请求生命周期内。所有 PlanState 在 TTL 过期后自动清理。
package store

import (
	"sync"
	"time"

	"github.com/telepathy/loom/model"
)

// Store 是线程安全的内存状态存储。
type Store struct {
	mu     sync.RWMutex
	plans  map[string]*model.PlanState
	planTTL time.Duration // 计划完成后在内存中保留的时长
}

// New 创建一个新的 Store，并启动后台 TTL 清理 goroutine。
func New(planTTL time.Duration) *Store {
	s := &Store{
		plans:   make(map[string]*model.PlanState),
		planTTL: planTTL,
	}
	go s.reapLoop()
	return s
}

// ──────────────────────────────────────────────
// CRUD
// ──────────────────────────────────────────────

// CreatePlan 创建一个新的 PlanState。若 planID 已存在则返回 false。
func (s *Store) CreatePlan(state *model.PlanState) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.plans[state.PlanID]; exists {
		return false
	}
	s.plans[state.PlanID] = state
	return true
}

// GetPlan 根据 planID 获取 PlanState。第二个返回值为是否存在。
func (s *Store) GetPlan(planID string) (*model.PlanState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[planID]
	return p, ok
}

// UpdateRepoState 更新指定 plan 下某个 repo 的状态。若 plan 不存在则返回 false。
func (s *Store) UpdateRepoState(planID, repoID string, update func(rs *model.RepoState)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok {
		return false
	}
	rs, ok := plan.Repos[repoID]
	if !ok {
		return false
	}
	update(rs)
	return true
}

// SetPlanCompleted 将 plan 标记为 COMPLETED 并记录完成时间。
func (s *Store) SetPlanCompleted(planID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if plan, ok := s.plans[planID]; ok {
		plan.Status = model.PlanCompleted
		now := time.Now()
		plan.CompletedAt = &now
	}
}

// DeletePlan 删除指定 plan。用于 TTL 过期清理或手动重置。
func (s *Store) DeletePlan(planID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plans, planID)
}

// ReplacePlan 删除并重新创建 plan。用于 GPS 重复调用 analyze 时重置。
func (s *Store) ReplacePlan(state *model.PlanState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[state.PlanID] = state
}

// ──────────────────────────────────────────────
// 辅助方法
// ──────────────────────────────────────────────

// AllInTerminalState 检查某 plan 下的所有 repo 是否都已达到终态（DONE | FAILED）。
// 若某 plan 不存在，第二个返回值为 false。
func (s *Store) AllInTerminalState(planID string) (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	plan, ok := s.plans[planID]
	if !ok {
		return false, false
	}
	for _, rs := range plan.Repos {
		if rs.Status != model.RepoDone && rs.Status != model.RepoFailed {
			return false, true
		}
	}
	return true, true
}

// GetInProgressPlanIDs 返回所有处于 IN_PROGRESS 状态的 plan ID 列表。
func (s *Store) GetInProgressPlanIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id, plan := range s.plans {
		if plan.Status == model.PlanInProgress {
			ids = append(ids, id)
		}
	}
	return ids
}

// ──────────────────────────────────────────────
// TTL 清理
// ──────────────────────────────────────────────

// reapLoop 定期扫描已完成的 plan，删除过期条目。
func (s *Store) reapLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.reap()
	}
}

func (s *Store) reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, plan := range s.plans {
		if plan.CompletedAt != nil && now.After(plan.CompletedAt.Add(s.planTTL)) {
			delete(s.plans, id)
		}
	}
}
