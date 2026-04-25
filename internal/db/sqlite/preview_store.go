// 이 파일의 책임:
//   - sqlc 가 생성한 Queries 를 감싸 store.PreviewStore 인터페이스를 구현.
//   - Upsert 트랜잭션: SELECT 사전 조회 → UPSERT → preview_events 분기 INSERT (룰 R1/R2).
//   - UpdateStatus 트랜잭션: status/error_message 갱신 + preview_events INSERT (룰 R1).
//   - Step 2/3 에 들어갈 메서드는 stub (store.ErrNotImplementedStep1) 으로 둔다.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, §5-5, §5-1-2, 결정 11.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/store"
)

// PreviewStore 는 store.PreviewStore 를 만족하는 SQLite 구현체.
// 구체 타입이 public 인 이유는 Step 2 이후 cmd/hub 가 ResetAllAssigned 같은
// 인터페이스 밖 메서드를 호출할 수 있도록 하기 위함이다(결정 11 패턴 재사용).
type PreviewStore struct {
	db *sql.DB
	q  *Queries
}

// NewPreviewStore 는 sql.DB 를 받아 PreviewStore 를 만든다.
// 호출자는 sql.DB 의 생명주기를 직접 관리한다(Close 는 호출자 책임).
func NewPreviewStore(db *sql.DB) *PreviewStore {
	return &PreviewStore{db: db, q: New(db)}
}

// 컴파일 타임 인터페이스 만족 확인 (F-S1-6).
var _ store.PreviewStore = (*PreviewStore)(nil)

// Upsert 는 (repo_full_name, pr_number) 기준으로 INSERT or UPDATE.
// 트랜잭션 안에서:
//  1. 기존 row SELECT (없으면 NULL prev).
//  2. UpsertPreview SQL 실행 (status 변경 없음).
//  3. 신규 INSERT 면 preview_events(NULL→queued) 1행 INSERT.
//
// 결정 11(R1/R2): 신규=event 1건, 기존+status변경없음=event 0건. status 변경 분기는
// 호출자(webhook handler) 가 별도로 UpdateStatus 호출.
func (s *PreviewStore) Upsert(ctx context.Context, p store.Preview) (created bool, prev *store.Preview, err error) {
	labelsJSON, err := encodeLabels(p.Labels)
	if err != nil {
		return false, nil, fmt.Errorf("sqlite.Upsert: marshal labels: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("sqlite.Upsert: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	qtx := s.q.WithTx(tx)

	// (1) 사전 SELECT — UPDATE/INSERT 분기 판단용.
	existingRow, sErr := qtx.GetPreviewByRepoAndPR(ctx, GetPreviewByRepoAndPRParams{
		RepoFullName: p.RepoFullName,
		PrNumber:     int64(p.PrNumber),
	})
	var existing *store.Preview
	switch {
	case errors.Is(sErr, sql.ErrNoRows):
		existing = nil
	case sErr != nil:
		return false, nil, fmt.Errorf("sqlite.Upsert: select existing: %w", sErr)
	default:
		converted, cErr := previewRowToDomain(existingRow)
		if cErr != nil {
			return false, nil, cErr
		}
		existing = converted
	}

	// (2) UPSERT (sqlc).
	now := p.UpdatedAt.UTC().Format(iso8601)
	createdAt := p.CreatedAt.UTC().Format(iso8601)
	if existing != nil {
		// UPDATE: created_at 보존을 위해 기존 값을 사용. (UpsertPreview 의 EXCLUDED 에는
		// created_at 이 없어 SQL 자체가 INSERT 시점 created_at 만 적용 — 안전.)
		createdAt = existing.CreatedAt.UTC().Format(iso8601)
	}
	if _, err := qtx.UpsertPreview(ctx, UpsertPreviewParams{
		ID:           p.ID,
		RepoFullName: p.RepoFullName,
		PrNumber:     int64(p.PrNumber),
		CommitSha:    p.CommitSha,
		Branch:       p.Branch,
		Labels:       labelsJSON,
		CreatedAt:    createdAt,
		UpdatedAt:    now,
	}); err != nil {
		return false, nil, fmt.Errorf("sqlite.Upsert: upsert: %w", err)
	}

	// (3) preview_events 분기 INSERT.
	if existing == nil {
		// R1: 신규 INSERT → (NULL → queued).
		evt := InsertPreviewEventParams{
			ID:         uuid.NewString(),
			PreviewID:  p.ID,
			FromStatus: sql.NullString{},
			ToStatus:   "queued",
			Message:    "",
			CreatedAt:  now,
		}
		if err := qtx.InsertPreviewEvent(ctx, evt); err != nil {
			return false, nil, fmt.Errorf("sqlite.Upsert: insert event: %w", err)
		}
	}
	// R2: 기존 row + status 변경 없음 → event 0건.

	if err := tx.Commit(); err != nil {
		return false, nil, fmt.Errorf("sqlite.Upsert: commit: %w", err)
	}
	committed = true

	return existing == nil, existing, nil
}

// GetByID 는 id 로 Preview 를 찾는다. 없으면 store.ErrNotFound.
func (s *PreviewStore) GetByID(ctx context.Context, id string) (*store.Preview, error) {
	row, err := s.q.GetPreviewByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlite.GetByID: %w", err)
	}
	return previewRowToDomain(row)
}

// UpdateStatus 는 단일 트랜잭션에서 status/error_message/updated_at 갱신 +
// preview_events INSERT 를 수행한다.
//
// Step 1 단순화: fromStatus CAS 는 적용하지 않고 항상 갱신한다(결정 11 의 CAS 는
// Step 2 의 dispatcher 동시성에서 의미를 가짐 — webhook handler 만 호출하는 본 Step
// 에서는 race 가 발생하지 않는다). fields 의 ErrorMessage 만 활용; 다른 필드는
// Step 2 의 UpdatePreviewStatusFields 에서 도입한다.
func (s *PreviewStore) UpdateStatus(ctx context.Context, id string, fromStatus, toStatus, message string, now time.Time, fields store.PreviewFields) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite.UpdateStatus: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	qtx := s.q.WithTx(tx)

	// 사전 SELECT — preview 존재 + (옵션) fromStatus 일치 검증.
	row, err := qtx.GetPreviewByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return fmt.Errorf("sqlite.UpdateStatus: select: %w", err)
	}
	if fromStatus != "" && row.Status != fromStatus {
		return store.ErrStaleState
	}

	errMsg := sql.NullString{}
	if fields.ErrorMessage != nil {
		errMsg = sql.NullString{String: *fields.ErrorMessage, Valid: true}
	} else if row.ErrorMessage.Valid {
		// 기존 error_message 보존.
		errMsg = row.ErrorMessage
	}
	nowStr := now.UTC().Format(iso8601)
	if err := qtx.UpdatePreviewStatus(ctx, UpdatePreviewStatusParams{
		Status:       toStatus,
		ErrorMessage: errMsg,
		UpdatedAt:    nowStr,
		ID:           id,
	}); err != nil {
		return fmt.Errorf("sqlite.UpdateStatus: update: %w", err)
	}

	// R1: 항상 event 1건.
	from := sql.NullString{String: row.Status, Valid: true}
	if err := qtx.InsertPreviewEvent(ctx, InsertPreviewEventParams{
		ID:         uuid.NewString(),
		PreviewID:  id,
		FromStatus: from,
		ToStatus:   toStatus,
		Message:    message,
		CreatedAt:  nowStr,
	}); err != nil {
		return fmt.Errorf("sqlite.UpdateStatus: insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite.UpdateStatus: commit: %w", err)
	}
	committed = true
	return nil
}

// ListAll 은 모든 preview 를 created_at DESC 로 반환한다.
func (s *PreviewStore) ListAll(ctx context.Context) ([]store.Preview, error) {
	rows, err := s.q.ListAllPreviews(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlite.ListAll: %w", err)
	}
	out := make([]store.Preview, 0, len(rows))
	for _, r := range rows {
		p, err := previewRowToDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// FindByHost 는 Step 2/3 에서 구현. 본 Step 에서는 stub.
func (s *PreviewStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}

// ListQueuedForCandidates 는 Step 2 에서 구현. 본 Step 에서는 stub.
func (s *PreviewStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}

// Claim 은 Step 2 에서 구현. 본 Step 에서는 stub.
func (s *PreviewStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}

// ListRunningByAgent 는 Step 3 에서 구현. 본 Step 에서는 stub.
func (s *PreviewStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}

// ListStaleAssigned 는 Step 3 에서 구현. 본 Step 에서는 stub.
func (s *PreviewStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}

// ListByAgent 는 Step 3 에서 구현. 본 Step 에서는 stub.
func (s *PreviewStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}

// CountPreviewEvents 는 단위 테스트용 helper. preview_id 의 이벤트 수 반환.
func (s *PreviewStore) CountPreviewEvents(ctx context.Context, previewID string) (int, error) {
	var n int
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM preview_events WHERE preview_id = ?`, previewID)
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite.CountPreviewEvents: %w", err)
	}
	return n, nil
}

// ListPreviewEvents 는 단위 테스트용 helper. preview_id 의 모든 이벤트를
// created_at ASC 로 반환한다.
type PreviewEventRow struct {
	FromStatus *string
	ToStatus   string
	Message    string
}

// ListPreviewEvents 는 preview_id 에 속한 preview_events 를 created_at ASC 로 반환한다.
func (s *PreviewStore) ListPreviewEvents(ctx context.Context, previewID string) ([]PreviewEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT from_status, to_status, message FROM preview_events WHERE preview_id = ? ORDER BY created_at ASC`, previewID)
	if err != nil {
		return nil, fmt.Errorf("sqlite.ListPreviewEvents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []PreviewEventRow{}
	for rows.Next() {
		var from sql.NullString
		var r PreviewEventRow
		if err := rows.Scan(&from, &r.ToStatus, &r.Message); err != nil {
			return nil, err
		}
		if from.Valid {
			s := from.String
			r.FromStatus = &s
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// previewRowToDomain 은 sqlc 생성 Preview row 를 store.Preview 도메인 객체로 변환한다.
func previewRowToDomain(r Preview) (*store.Preview, error) {
	labels, err := decodeLabels(r.Labels)
	if err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	createdAt, err := time.Parse(iso8601, r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, err := time.Parse(iso8601, r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	p := &store.Preview{
		ID:           r.ID,
		RepoFullName: r.RepoFullName,
		PrNumber:     int(r.PrNumber),
		CommitSha:    r.CommitSha,
		Branch:       r.Branch,
		Status:       r.Status,
		Labels:       labels,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}
	if r.AssignedAgentID.Valid {
		v := r.AssignedAgentID.String
		p.AssignedAgentID = &v
	}
	if r.ContainerID.Valid {
		v := r.ContainerID.String
		p.ContainerID = &v
	}
	if r.AgentHost.Valid {
		v := r.AgentHost.String
		p.AgentHost = &v
	}
	if r.AgentPort.Valid {
		v := int(r.AgentPort.Int64)
		p.AgentPort = &v
	}
	if r.PublicUrl.Valid {
		v := r.PublicUrl.String
		p.PublicURL = &v
	}
	if r.ErrorMessage.Valid {
		v := r.ErrorMessage.String
		p.ErrorMessage = &v
	}
	return p, nil
}
