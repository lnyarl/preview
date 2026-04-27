-- Phase 9 down: previews 자연키 (repo, sha) → (repo, pr_number) 복원 + is_adhoc 제거.
--
-- 경고: (repo, pr) 그룹에 다중 sha row 가 누적되어 있으면 UNIQUE (repo, pr) 위반이 발생한다.
-- 따라서 down 은 각 (repo, pr) 그룹의 가장 최근 1건만 보존하고 나머지를 삭제한다.
-- 이 동작은 데이터 손실을 동반한다 — 운영 가이드의 경고를 따르고 백업 후 실행할 것.
PRAGMA foreign_keys = OFF;

-- 1) (repo, pr) 그룹별 최신 row 만 유지 (UNIQUE 위반 회피).
DELETE FROM previews
WHERE id NOT IN (
  SELECT id FROM (
    SELECT id, ROW_NUMBER() OVER (
      PARTITION BY repo_full_name, pr_number ORDER BY created_at DESC
    ) AS rn
    FROM previews
  )
  WHERE rn = 1
);

-- 2) 구 스키마(commit_sha NOT NULL DEFAULT '', UNIQUE (repo, pr)) 의 임시 테이블 생성.
CREATE TABLE previews_old (
    id                 TEXT PRIMARY KEY NOT NULL,
    repo_full_name     TEXT NOT NULL,
    pr_number          INTEGER NOT NULL,
    commit_sha         TEXT NOT NULL DEFAULT '',
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
    UNIQUE (repo_full_name, pr_number),
    FOREIGN KEY (assigned_agent_id) REFERENCES agents(id) ON DELETE SET NULL
);

INSERT INTO previews_old
  (id, repo_full_name, pr_number, commit_sha, branch, status,
   assigned_agent_id, container_id, agent_host, agent_port,
   labels, error_message, created_at, updated_at,
   repo_clone_url, preview_urls)
SELECT id, repo_full_name, pr_number,
  COALESCE(commit_sha, ''), branch, status,
  assigned_agent_id, container_id, agent_host, agent_port,
  labels, error_message, created_at, updated_at,
  repo_clone_url, preview_urls
FROM previews;

DROP TABLE previews;
ALTER TABLE previews_old RENAME TO previews;

CREATE INDEX idx_previews_status   ON previews(status);
CREATE INDEX idx_previews_repo_pr  ON previews(repo_full_name, pr_number);
CREATE INDEX idx_previews_assigned ON previews(assigned_agent_id, status);

PRAGMA foreign_keys = ON;
