-- Phase 9: previews 자연키 (repo, pr_number) → (repo, commit_sha) 변경 + is_adhoc 컬럼 추가.
--
-- 핵심 보장:
--   1) preview_events 테이블 데이터 전량 보존
--      - preview_events.preview_id 는 previews(id) 에 FK CASCADE.
--      - PRAGMA foreign_keys = OFF 로 마이그레이션 구간 동안 CASCADE 무력화.
--      - DROP/RENAME 후에도 previews(id) 의 ID 공간이 그대로 유지되므로 events 의 FK 정합성 유지.
--   2) commit_sha = '' → NULL 변환 (NULL UNIQUE 미발동 → 빈 sha 다중 row 안전).
--   3) is_adhoc DEFAULT 0 (기존 row 는 모두 webhook-origin 으로 보수 가정).
--
-- SQLite 는 DROP CONSTRAINT 미지원이라 테이블 재생성이 표준.
PRAGMA foreign_keys = OFF;

CREATE TABLE previews_new (
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

INSERT INTO previews_new
  (id, repo_full_name, pr_number, commit_sha, branch, status,
   assigned_agent_id, container_id, agent_host, agent_port,
   labels, error_message, created_at, updated_at,
   repo_clone_url, preview_urls, is_adhoc)
SELECT
  id, repo_full_name, pr_number,
  CASE WHEN commit_sha = '' THEN NULL ELSE commit_sha END,
  branch, status,
  assigned_agent_id, container_id, agent_host, agent_port,
  labels, error_message, created_at, updated_at,
  repo_clone_url, preview_urls,
  0
FROM previews;

DROP TABLE previews;
ALTER TABLE previews_new RENAME TO previews;

CREATE INDEX idx_previews_status   ON previews(status);
CREATE INDEX idx_previews_repo_pr  ON previews(repo_full_name, pr_number);
CREATE INDEX idx_previews_assigned ON previews(assigned_agent_id, status);
-- (repo_full_name, commit_sha) 인덱스는 UNIQUE 제약이 자동 생성하는 sqlite_autoindex 사용.

PRAGMA foreign_keys = ON;
