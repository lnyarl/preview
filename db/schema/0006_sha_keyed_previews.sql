-- Phase 9: previews 의 자연키를 (repo_full_name, pr_number) → (repo_full_name, commit_sha)
-- 로 변경하고, webhook 자동/Adhoc 수동 구분용 is_adhoc 컬럼을 추가한다.
--
-- 이 파일은 sqlc 의 입력 스키마이므로 "Phase 9 적용 후" 의 최종 상태를 기술한다.
-- 실제 마이그레이션 SQL 은 internal/db/sqlite/migrations/0006_*.sql 참조.
--
-- commit_sha 는 NULLABLE — SQLite/Postgres 모두 NULL UNIQUE 는 distinct 로 취급되므로
-- 빈 sha (= NULL) 의 다중 row 가 허용된다. 도메인 측 store.Preview.CommitSha 는 string
-- 으로 유지하고, store 변환 레이어에서 빈 문자열 ↔ NULL 매핑한다.
DROP TABLE IF EXISTS previews;

CREATE TABLE IF NOT EXISTS previews (
    id                 TEXT PRIMARY KEY NOT NULL,
    repo_full_name     TEXT NOT NULL,
    pr_number          INTEGER NOT NULL,
    commit_sha         TEXT,
    branch             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'queued',
    assigned_agent_id  TEXT,
    container_id       TEXT,
    agent_host         TEXT,
    agent_port         INTEGER,
    labels             TEXT NOT NULL DEFAULT '{}',
    error_message      TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    repo_clone_url     TEXT NOT NULL DEFAULT '',
    preview_urls       TEXT NOT NULL DEFAULT '',
    is_adhoc           INTEGER NOT NULL DEFAULT 0,
    UNIQUE (repo_full_name, commit_sha),
    FOREIGN KEY (assigned_agent_id) REFERENCES agents(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_previews_status   ON previews(status);
CREATE INDEX IF NOT EXISTS idx_previews_repo_pr  ON previews(repo_full_name, pr_number);
CREATE INDEX IF NOT EXISTS idx_previews_assigned ON previews(assigned_agent_id, status);
-- (repo_full_name, commit_sha) 자동 인덱스는 UNIQUE 제약이 만들어주므로 명시 생성 안 함.
