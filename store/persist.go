package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/telepathy/loom/model"
)

// Persister 将分析结果持久化到 MySQL（loom_* 表）。
// 所有方法在 DB 不可用时仅 log 错误，不中断分析流程。
type Persister interface {
	// PersistPlan 在计划创建时写入 plan + repos（状态 PENDING）。
	PersistPlan(ctx context.Context, plan *model.PlanState) error

	// PersistRepoResult 在单个 repo 分析完成时写入 subprojects + edges，更新 repo 状态。
	PersistRepoResult(ctx context.Context, planID string, rs *model.RepoState) error

	// MarkPlanCompleted 在所有 repo 终态后更新 plan 状态为 COMPLETED。
	MarkPlanCompleted(ctx context.Context, planID string) error
}

// --- SQL Persister 实现 ---

type sqlPersister struct {
	db *sql.DB
}

// NewSQLPersister 创建 MySQL 持久化器。db 为 nil 时所有方法为 no-op。
func NewSQLPersister(db *sql.DB) Persister {
	if db == nil {
		return &noopPersister{}
	}
	return &sqlPersister{db: db}
}

// --- 实现 ---

func (p *sqlPersister) PersistPlan(ctx context.Context, plan *model.PlanState) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("persist plan: begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO loom_analysis_plans (plan_id, akasha_branch, status, repo_count, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE status=VALUES(status), repo_count=VALUES(repo_count)`,
		plan.PlanID, plan.AkashaBranch, string(plan.Status), len(plan.Repos), plan.CreatedAt)
	if err != nil {
		return fmt.Errorf("persist plan: insert plan: %w", err)
	}

	// 写入每个 repo 的初始状态
	for _, rs := range plan.Repos {
		ref := rs.Branch
		if ref == "" {
			ref = rs.Tag
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO loom_analysis_repos (plan_id, repo_id, repo_url, ref, status)
			 VALUES (?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE status=VALUES(status), ref=VALUES(ref)`,
			plan.PlanID, rs.RepoID, rs.RepoURL, ref, string(rs.Status))
		if err != nil {
			return fmt.Errorf("persist plan: insert repo %s: %w", rs.RepoID, err)
		}
	}

	return tx.Commit()
}

func (p *sqlPersister) PersistRepoResult(ctx context.Context, planID string, rs *model.RepoState) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("persist repo result: begin tx: %w", err)
	}
	defer tx.Rollback()

	// 更新 repo 状态
	_, err = tx.ExecContext(ctx,
		`UPDATE loom_analysis_repos SET status=?, error=?, started_at=?, finished_at=?
		 WHERE plan_id=? AND repo_id=?`,
		string(rs.Status), sql.NullString{String: rs.Error, Valid: rs.Error != ""},
		rs.StartedAt, rs.FinishedAt,
		planID, rs.RepoID)
	if err != nil {
		return fmt.Errorf("persist repo result: update repo: %w", err)
	}

	// 写入 subprojects（先删后插，保证幂等）
	if len(rs.Subprojects) > 0 {
		_, _ = tx.ExecContext(ctx,
			`DELETE FROM loom_subprojects WHERE plan_id=? AND repo_id=?`, planID, rs.RepoID)
		for _, sp := range rs.Subprojects {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO loom_subprojects (plan_id, repo_id, gradle_path, group_name, artifact, version)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				planID, rs.RepoID, sp.GradlePath, sp.Group, sp.Artifact, sp.Version)
			if err != nil {
				return fmt.Errorf("persist repo result: insert subproject: %w", err)
			}
		}
	}

	// 写入 edges（先删后插）
	if len(rs.Edges) > 0 {
		_, _ = tx.ExecContext(ctx,
			`DELETE FROM loom_edges WHERE plan_id=? AND repo_id=?`, planID, rs.RepoID)
		for _, e := range rs.Edges {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO loom_edges (plan_id, repo_id, from_ref, to_ref, type)
				 VALUES (?, ?, ?, ?, ?)`,
				planID, rs.RepoID, e.From, e.To, e.Type)
			if err != nil {
				return fmt.Errorf("persist repo result: insert edge: %w", err)
			}
		}
	}

	return tx.Commit()
}

func (p *sqlPersister) MarkPlanCompleted(ctx context.Context, planID string) error {
	now := time.Now()
	_, err := p.db.ExecContext(ctx,
		`UPDATE loom_analysis_plans SET status='COMPLETED', completed_at=? WHERE plan_id=?`,
		now, planID)
	return err
}

// --- 查询（供 handler 使用） ---

// PlanSummary 是计划列表中的一行。
type PlanSummary struct {
	PlanID       string    `json:"plan_id"`
	AkashaBranch string    `json:"akasha_branch"`
	Status       string    `json:"status"`
	RepoCount    int       `json:"repo_count"`
	CreatedAt    time.Time `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// PlanDetail 是单个计划的完整数据（含 repos、subprojects、edges）。
type PlanDetail struct {
	PlanSummary
	Repos []RepoDetail `json:"repos"`
}

// RepoDetail 是单个 repo 的分析详情。
type RepoDetail struct {
	RepoID       string             `json:"repo_id"`
	RepoURL      string             `json:"repo_url"`
	Ref          string             `json:"ref"`
	Status       string             `json:"status"`
	Error        string             `json:"error,omitempty"`
	StartedAt    *time.Time         `json:"started_at,omitempty"`
	FinishedAt   *time.Time         `json:"finished_at,omitempty"`
	Subprojects  []model.Subproject `json:"subprojects,omitempty"`
	Edges        []model.RawEdge    `json:"edges,omitempty"`
}

// QueryPlans 查询所有分析计划（按创建时间倒序）。
func QueryPlans(ctx context.Context, db *sql.DB) ([]PlanSummary, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT plan_id, akasha_branch, status, repo_count, created_at, completed_at
		 FROM loom_analysis_plans ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PlanSummary
	for rows.Next() {
		var p PlanSummary
		if err := rows.Scan(&p.PlanID, &p.AkashaBranch, &p.Status, &p.RepoCount, &p.CreatedAt, &p.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// QueryPlanDetail 查询单个计划的完整详情。
func QueryPlanDetail(ctx context.Context, db *sql.DB, planID string) (*PlanDetail, error) {
	// 查询 plan
	var p PlanDetail
	err := db.QueryRowContext(ctx,
		`SELECT plan_id, akasha_branch, status, repo_count, created_at, completed_at
		 FROM loom_analysis_plans WHERE plan_id=?`, planID).
		Scan(&p.PlanID, &p.AkashaBranch, &p.Status, &p.RepoCount, &p.CreatedAt, &p.CompletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// 查询 repos
	repoRows, err := db.QueryContext(ctx,
		`SELECT repo_id, repo_url, ref, status, COALESCE(error,''), started_at, finished_at
		 FROM loom_analysis_repos WHERE plan_id=? ORDER BY repo_id`, planID)
	if err != nil {
		return nil, err
	}
	defer repoRows.Close()

	for repoRows.Next() {
		var rd RepoDetail
		if err := repoRows.Scan(&rd.RepoID, &rd.RepoURL, &rd.Ref, &rd.Status,
			&rd.Error, &rd.StartedAt, &rd.FinishedAt); err != nil {
			return nil, err
		}
		p.Repos = append(p.Repos, rd)
	}
	if err := repoRows.Err(); err != nil {
		return nil, err
	}

	// 查询 subprojects
	spRows, err := db.QueryContext(ctx,
		`SELECT repo_id, gradle_path, group_name, artifact, version
		 FROM loom_subprojects WHERE plan_id=? ORDER BY repo_id, gradle_path`, planID)
	if err != nil {
		return nil, err
	}
	defer spRows.Close()

	spMap := make(map[string][]model.Subproject)
	for spRows.Next() {
		var repoID string
		var sp model.Subproject
		if err := spRows.Scan(&repoID, &sp.GradlePath, &sp.Group, &sp.Artifact, &sp.Version); err != nil {
			return nil, err
		}
		spMap[repoID] = append(spMap[repoID], sp)
	}

	// 查询 edges
	edgeRows, err := db.QueryContext(ctx,
		`SELECT repo_id, from_ref, to_ref, type
		 FROM loom_edges WHERE plan_id=? ORDER BY repo_id, from_ref`, planID)
	if err != nil {
		return nil, err
	}
	defer edgeRows.Close()

	edgeMap := make(map[string][]model.RawEdge)
	for edgeRows.Next() {
		var repoID string
		var e model.RawEdge
		if err := edgeRows.Scan(&repoID, &e.From, &e.To, &e.Type); err != nil {
			return nil, err
		}
		edgeMap[repoID] = append(edgeMap[repoID], e)
	}

	// 将 subprojects 和 edges 挂到对应的 repo
	for i := range p.Repos {
		p.Repos[i].Subprojects = spMap[p.Repos[i].RepoID]
		p.Repos[i].Edges = edgeMap[p.Repos[i].RepoID]
	}

	return &p, nil
}

// --- noop persister（DB 未配置时使用） ---

type noopPersister struct{}

func (*noopPersister) PersistPlan(ctx context.Context, plan *model.PlanState) error          { return nil }
func (*noopPersister) PersistRepoResult(ctx context.Context, planID string, rs *model.RepoState) error { return nil }
func (*noopPersister) MarkPlanCompleted(ctx context.Context, planID string) error             { return nil }
