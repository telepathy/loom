package db

import (
	"database/sql"
	"fmt"
	"log"
)

// Migrate creates the loom_* tables if they don't exist.
// Safe to call on every startup (uses IF NOT EXISTS).
func Migrate(db *sql.DB) error {
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS loom_analysis_plans (
			plan_id        VARCHAR(128) PRIMARY KEY,
			akasha_branch  VARCHAR(128) NOT NULL DEFAULT '',
			status         VARCHAR(16)  NOT NULL DEFAULT 'IN_PROGRESS',
			repo_count     INT          NOT NULL DEFAULT 0,
			created_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at   DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS loom_analysis_repos (
			plan_id     VARCHAR(128) NOT NULL,
			repo_id     VARCHAR(64)  NOT NULL,
			repo_url    VARCHAR(512) NOT NULL DEFAULT '',
			ref         VARCHAR(128) NOT NULL DEFAULT '',
			status      VARCHAR(16)  NOT NULL DEFAULT 'PENDING',
			error       TEXT,
			started_at  DATETIME,
			finished_at DATETIME,
			PRIMARY KEY (plan_id, repo_id)
		)`,
		`CREATE TABLE IF NOT EXISTS loom_subprojects (
			plan_id     VARCHAR(128) NOT NULL,
			repo_id     VARCHAR(64)  NOT NULL,
			gradle_path VARCHAR(255) NOT NULL DEFAULT '',
			group_name  VARCHAR(255) NOT NULL DEFAULT '',
			artifact    VARCHAR(255) NOT NULL DEFAULT '',
			version     VARCHAR(64)  NOT NULL DEFAULT '',
			PRIMARY KEY (plan_id, repo_id, gradle_path)
		)`,
		`CREATE TABLE IF NOT EXISTS loom_edges (
			plan_id  VARCHAR(128) NOT NULL,
			repo_id  VARCHAR(64)  NOT NULL,
			from_ref VARCHAR(255) NOT NULL DEFAULT '',
			to_ref   VARCHAR(255) NOT NULL DEFAULT '',
			type     VARCHAR(16)  NOT NULL DEFAULT '',
			PRIMARY KEY (plan_id, repo_id, from_ref, to_ref(191))
		)`,
	}

	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("migrate: %w\n%s", err, ddl)
		}
	}

	log.Println("db: loom_* tables migrated")
	return nil
}
