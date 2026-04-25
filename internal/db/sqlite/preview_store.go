// 이 파일의 책임:
//   - sqlc 가 생성한 Queries 를 감싸 store.PreviewStore 인터페이스를 구현.
//   - Upsert 트랜잭션: SELECT 사전 조회 → UPSERT → preview_events 분기 INSERT (룰 R1/R2).
//   - UpdateStatus 트랜잭션: status + 부수 필드(PreviewFields) 갱신 + preview_events INSERT (룰 R1).
//   - ListQueuedForCandidates / Claim: Step 2 dispatcher 가 사용. Claim 은 race-free CAS.
//   - ResetAllAssigned: Hub 기동 직후 잔존 'assigned' → 'queued' 일괄 복귀 (인터페이스 외).
//   - Step 3 에 들어갈 메서드(FindByHost, ListRunningByAgent, ListStaleAssigned, ListByAgent)는 stub.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, §5-5, §5-1-2, 결정 5/11.
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

// UpdateStatus 는 단일 트랜잭션에서 status + nullable 부수 필드 갱신 +
// preview_events INSERT 를 수행한다(결정 11, R1).
//
// fields 가 모든 nil 인 경우와 일부라도 non-nil 인 경우 모두 동일 SQL
// (UpdatePreviewStatusFields, COALESCE 보호)을 사용한다 — fields 가 nil 이어도
// 기존 column 값이 유지되므로 안전. ErrorMessage 만은 호출자가 message 인자를
// 통해 자주 갱신하므로 fields.ErrorMessage 가 nil 이고 message 가 비어있지 않은
// 경우 message 를 error_message 로 동시에 채운다(webhook handleClose 패턴과 호환).
//
// fromStatus="" 이면 CAS 비활성. fromStatus 가 비어있지 않은데 현재 row 의
// status 와 다르면 ErrStaleState.
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

	nowStr := now.UTC().Format(iso8601)
	params := UpdatePreviewStatusFieldsParams{
		Status:    toStatus,
		UpdatedAt: nowStr,
		ID:        id,
	}
	if fields.ContainerID != nil {
		params.ContainerID = sql.NullString{String: *fields.ContainerID, Valid: true}
	}
	if fields.AgentHost != nil {
		params.AgentHost = sql.NullString{String: *fields.AgentHost, Valid: true}
	}
	if fields.AgentPort != nil {
		params.AgentPort = sql.NullInt64{Int64: int64(*fields.AgentPort), Valid: true}
	}
	if fields.PublicURL != nil {
		params.PublicUrl = sql.NullString{String: *fields.PublicURL, Valid: true}
	}
	switch {
	case fields.ErrorMessage != nil:
		params.ErrorMessage = sql.NullString{String: *fields.ErrorMessage, Valid: true}
	case message != "":
		// message 인자를 error_message 로도 사용 (webhook 호환).
		params.ErrorMessage = sql.NullString{String: message, Valid: true}
	}
	if fields.AssignedAgentID != nil {
		params.AssignedAgentID = sql.NullString{String: *fields.AssignedAgentID, Valid: true}
	}
	if _, err := qtx.UpdatePreviewStatusFields(ctx, params); err != nil {
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

// FindByHost 는 pr_number 로 preview 를 찾는다. 없으면 ErrNotFound.
// repoFullName 은 미래 multi-repo 확장용 예약 파라미터 — 현재는 무시.
func (s *PreviewStore) FindByHost(ctx context.Context, _ string, prNumber int) (*store.Preview, error) {
	row, err := s.q.FindPreviewByHost(ctx, int64(prNumber))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlite.FindByHost: %w", err)
	}
	return previewRowToDomain(row)
}

// ListQueuedForCandidates 는 status='queued' 인 모든 preview 를 created_at ASC 로 반환한다.
// dispatcher 가 이 결과에 라벨 매칭(LabelsMatch)을 적용해 후보를 좁힌다.
func (s *PreviewStore) ListQueuedForCandidates(ctx context.Context) ([]store.Preview, error) {
	rows, err := s.q.ListQueuedPreviewsForLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlite.ListQueuedForCandidates: %w", err)
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

// Claim 은 candidate ID 슬라이스 중 status='queued' 인 가장 오래된 row 를
// race-free 로 'assigned' 로 점유한다. 단일 트랜잭션에서:
//  1. UPDATE ... SELECT LIMIT 1 (sql.ErrNoRows 면 다른 worker 가 선점 → ErrNotFound).
//  2. preview_events(queued → assigned) INSERT.
//
// candidateIDs 가 비어있으면 ErrNotFound 즉시 반환 (sqlc.slice 빈 슬라이스 보호).
func (s *PreviewStore) Claim(ctx context.Context, candidateIDs []string, agentID string, now time.Time) (*store.Preview, error) {
	if len(candidateIDs) == 0 {
		return nil, store.ErrNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite.Claim: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	qtx := s.q.WithTx(tx)

	nowStr := now.UTC().Format(iso8601)
	row, err := qtx.ClaimPreview(ctx, ClaimPreviewParams{
		AssignedAgentID: sql.NullString{String: agentID, Valid: true},
		UpdatedAt:       nowStr,
		CandidateIds:    candidateIDs,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlite.Claim: update: %w", err)
	}

	// R1: queued → assigned event.
	if err := qtx.InsertPreviewEvent(ctx, InsertPreviewEventParams{
		ID:         uuid.NewString(),
		PreviewID:  row.ID,
		FromStatus: sql.NullString{String: "queued", Valid: true},
		ToStatus:   "assigned",
		Message:    "",
		CreatedAt:  nowStr,
	}); err != nil {
		return nil, fmt.Errorf("sqlite.Claim: insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlite.Claim: commit: %w", err)
	}
	committed = true

	return previewRowToDomain(row)
}

// ResetAllAssigned 는 status='assigned' 인 모든 row 를 'queued' 로 일괄 복귀시키고,
// 각 row 마다 preview_events(assigned → queued, message='hub_restart') INSERT.
// Hub 기동 직후 1회 호출 (cmd/hub daemon). 인터페이스 외 구체 타입 메서드.
// 반환: 영향 받은 row 수.
func (s *PreviewStore) ResetAllAssigned(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllAssigned: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 사전 SELECT — event INSERT 가 row 단위로 필요.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM previews WHERE status = 'assigned'`)
	if err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllAssigned: select: %w", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("sqlite.ResetAllAssigned: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllAssigned: rows close: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllAssigned: rows: %w", err)
	}

	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("sqlite.ResetAllAssigned: commit: %w", err)
		}
		committed = true
		return 0, nil
	}

	nowStr := time.Now().UTC().Format(iso8601)
	qtx := s.q.WithTx(tx)
	n, err := qtx.ResetAllAssignedPreviews(ctx, nowStr)
	if err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllAssigned: update: %w", err)
	}

	for _, id := range ids {
		if err := qtx.InsertPreviewEvent(ctx, InsertPreviewEventParams{
			ID:         uuid.NewString(),
			PreviewID:  id,
			FromStatus: sql.NullString{String: "assigned", Valid: true},
			ToStatus:   "queued",
			Message:    "hub_restart",
			CreatedAt:  nowStr,
		}); err != nil {
			return 0, fmt.Errorf("sqlite.ResetAllAssigned: insert event: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllAssigned: commit: %w", err)
	}
	committed = true
	return n, nil
}

// ListRunningByAgent 는 status='running' 이고 assigned_agent_id=agentID 인 row.
// reconciler 가 offline agent 의 보존 카운트용으로 사용.
func (s *PreviewStore) ListRunningByAgent(ctx context.Context, agentID string) ([]store.Preview, error) {
	rows, err := s.q.ListRunningPreviewsByAgent(ctx, sql.NullString{String: agentID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("sqlite.ListRunningByAgent: %w", err)
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

// ListStaleAssigned 는 status='assigned' 이고 updated_at < staleAfter 인 row.
// reconciler 가 stale assigned → queued 회수용으로 사용.
func (s *PreviewStore) ListStaleAssigned(ctx context.Context, staleAfter time.Time) ([]store.Preview, error) {
	rows, err := s.q.ListStaleAssignedPreviews(ctx, staleAfter.UTC().Format(iso8601))
	if err != nil {
		return nil, fmt.Errorf("sqlite.ListStaleAssigned: %w", err)
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

// ListByAgent 는 assigned_agent_id=agentID 이고 status IN statuses 인 row.
// statuses 가 비어있으면 nil 반환 (sqlc.slice 빈 슬라이스 보호).
func (s *PreviewStore) ListByAgent(ctx context.Context, agentID string, statuses []string) ([]store.Preview, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListByAgent(ctx, ListByAgentParams{
		AssignedAgentID: sql.NullString{String: agentID, Valid: true},
		Statuses:        statuses,
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite.ListByAgent: %w", err)
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
