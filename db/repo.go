// Package db 提供对 GPS 共享数据库的只读查询。
// Loom 与 GPS 部署在同一 MySQL schema 中，可直接读取 gps_repos 等表。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

// RepoRow 是 gps_repos 表的一行记录。
type RepoRow struct {
	ID            string
	SiloID        string
	Name          string
	URL           string
	ReleaseBranch string
	JDK           string // JDK 大版本（"8"/"17"/"21"），默认 "17"
}

// Open 打开 MySQL 连接并验证可达性。
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return db, nil
}

// QueryRepos 查询 gps_repos 表，返回所有仓库及其发布分支。
// siloIDs 为空时返回全部仓库，否则按 silo_id 过滤。
func QueryRepos(ctx context.Context, db *sql.DB, siloIDs []string) ([]RepoRow, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if len(siloIDs) == 0 {
		rows, err = db.QueryContext(ctx,
			"SELECT id, silo_id, name, url, release_branch, COALESCE(jdk,'17') FROM gps_repos ORDER BY silo_id, name")
	} else {
		placeholders := make([]string, len(siloIDs))
		args := make([]interface{}, len(siloIDs))
		for i, id := range siloIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"SELECT id, silo_id, name, url, release_branch, COALESCE(jdk,'17') FROM gps_repos WHERE silo_id IN (%s) ORDER BY silo_id, name",
			strings.Join(placeholders, ","))
		rows, err = db.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("query gps_repos: %w", err)
	}
	defer rows.Close()

	var out []RepoRow
	for rows.Next() {
		var r RepoRow
		if err := rows.Scan(&r.ID, &r.SiloID, &r.Name, &r.URL, &r.ReleaseBranch, &r.JDK); err != nil {
			return nil, fmt.Errorf("scan gps_repos row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return out, nil
}
