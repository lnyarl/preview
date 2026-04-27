// 이 파일의 책임:
//   - sqlc 가 생성한 Queries 를 감싸 store.RepoSecretStore 인터페이스를 구현 (Phase 10).
//   - 모든 진입점에서 repo_full_name 을 strings.ToLower 로 정규화한다 (결정 15).
//   - List: key ASC 정렬 + UpdatedAt 의 ISO8601 ↔ time.Time 변환.
//   - Upsert: ON CONFLICT(repo, key) DO UPDATE — 표준 SQL UPSERT (결정 12).
//   - Delete / DeleteAllFor: row 단위 / repo 단위 삭제. 미존재 row 는 NOOP (에러 아님).
//   - ListRepos: SELECT DISTINCT repo_full_name (결정 14, /admin/repos 인덱스 union 용).
//
// WARNING: value 는 plaintext 로 저장된다 (Phase 10 결정 7 / 리스크 R-2).
// 후속 Phase 에서 transparent encryption 으로 이행할 때 본 store 의
// public 시그니처는 그대로 유지된다.
//
// 참고: docs/specs/phase-10-repo-build-secrets.md §3 (결정 12, 15), §5-3.
package sqlitestore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

// RepoSecretStore 는 store.RepoSecretStore 를 만족하는 SQLite 구현체.
type RepoSecretStore struct {
	q *Queries
}

// NewRepoSecretStore 는 sqlc Queries 를 감싸 RepoSecretStore 를 만든다.
// 호출자는 sql.DB 의 생명주기를 직접 관리한다 (Close 는 호출자 책임).
func NewRepoSecretStore(q *Queries) *RepoSecretStore {
	return &RepoSecretStore{q: q}
}

// 컴파일 타임 인터페이스 만족 확인.
var _ store.RepoSecretStore = (*RepoSecretStore)(nil)

// normRepo 는 결정 15 에 따라 repo_full_name 을 lowercase 로 정규화한다.
// 모든 메서드의 첫 줄에서 호출되며, 호출자가 mixed-case 를 흘려도 DB 에는 lowercase 만 들어간다.
func normRepo(s string) string { return strings.ToLower(s) }

// List 는 repo 의 모든 secret row 를 key ASC 로 반환한다 (F-6).
func (s *RepoSecretStore) List(ctx context.Context, repoFullName string) ([]store.RepoSecret, error) {
	repo := normRepo(repoFullName)
	rows, err := s.q.ListRepoSecrets(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("sqlite.RepoSecret.List: %w", err)
	}
	out := make([]store.RepoSecret, 0, len(rows))
	for _, r := range rows {
		updatedAt, perr := time.Parse(iso8601, r.UpdatedAt)
		if perr != nil {
			return nil, fmt.Errorf("sqlite.RepoSecret.List: parse updated_at: %w", perr)
		}
		out = append(out, store.RepoSecret{
			RepoFullName: r.RepoFullName,
			Key:          r.Key,
			Value:        r.Value,
			UpdatedAt:    updatedAt,
		})
	}
	return out, nil
}

// Upsert 는 (repo, key) 기준으로 INSERT 또는 value/updated_at 갱신을 수행한다 (F-3, F-4).
// 결정 12 의 표준 SQL UPSERT (ON CONFLICT ... DO UPDATE).
func (s *RepoSecretStore) Upsert(ctx context.Context, sec store.RepoSecret) error {
	repo := normRepo(sec.RepoFullName)
	updatedAt := sec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	if err := s.q.UpsertRepoSecret(ctx, UpsertRepoSecretParams{
		RepoFullName: repo,
		Key:          sec.Key,
		Value:        sec.Value,
		UpdatedAt:    updatedAt.UTC().Format(iso8601),
	}); err != nil {
		return fmt.Errorf("sqlite.RepoSecret.Upsert: %w", err)
	}
	return nil
}

// Delete 는 (repo, key) 단일 row 를 삭제한다 (F-5).
// 미존재 row 는 NOOP (RowsAffected 검사 안 함 — set-replace 시맨틱과 호환).
func (s *RepoSecretStore) Delete(ctx context.Context, repoFullName, key string) error {
	repo := normRepo(repoFullName)
	if err := s.q.DeleteRepoSecret(ctx, DeleteRepoSecretParams{
		RepoFullName: repo,
		Key:          key,
	}); err != nil {
		return fmt.Errorf("sqlite.RepoSecret.Delete: %w", err)
	}
	return nil
}

// DeleteAllFor 는 repo 의 모든 secret row 를 일괄 삭제한다 (F-8).
// 다른 repo 의 row 는 영향받지 않는다.
func (s *RepoSecretStore) DeleteAllFor(ctx context.Context, repoFullName string) error {
	repo := normRepo(repoFullName)
	if err := s.q.DeleteAllRepoSecretsFor(ctx, repo); err != nil {
		return fmt.Errorf("sqlite.RepoSecret.DeleteAllFor: %w", err)
	}
	return nil
}

// ListRepos 는 secret 이 등록된 distinct repo_full_name 을 정렬 반환한다 (F-7).
// /admin/repos 인덱스 페이지가 PreviewStore.ListRepos 와 union 하기 위한 진입점 (결정 14).
func (s *RepoSecretStore) ListRepos(ctx context.Context) ([]string, error) {
	rows, err := s.q.ListRepoSecretRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlite.RepoSecret.ListRepos: %w", err)
	}
	return rows, nil
}
