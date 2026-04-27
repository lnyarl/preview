-- Phase 10: 저장소(repo_full_name) 단위 빌드 시 환경변수(plaintext) 묶음.
--
-- 이 파일은 sqlc 의 입력 스키마이므로 Phase 10 적용 후 의 최종 상태를 기술한다.
-- 실제 마이그레이션 SQL 은 internal/db/sqlite/migrations/0007_*.sql 참조.
--
-- WARNING: value 는 plaintext (Phase 10 한정, 결정 7). 후속 Phase 에서 envelope
-- encryption 으로 in-place 마이그레이션 예정.
CREATE TABLE IF NOT EXISTS repo_secrets (
    repo_full_name TEXT NOT NULL,
    key            TEXT NOT NULL,
    value          TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (repo_full_name, key)
);

CREATE INDEX IF NOT EXISTS idx_repo_secrets_repo ON repo_secrets(repo_full_name);
