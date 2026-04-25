// 이 파일의 책임:
//   - sqlc 가 생성한 Queries 를 감싸 store.AgentStore 인터페이스를 구현.
//   - UNIQUE 제약(name) 위반을 store.ErrDuplicate 로 번역.
//   - RowsAffected=0 을 store.ErrNotFound 로 승격 (Update/Delete).
//   - Phase 1 운영 특수 경로: ResetAllOnline (인터페이스 밖, 결정 11).
//   - Phase 4: GetBuildConfig / SaveBuildConfig — NULL/empty/0 sentinel 처리.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-1, §5-1-1, 결정 10/11,
//       docs/specs/phase-4-agent-build-config.md §4-1.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

const iso8601 = time.RFC3339Nano

// AgentStore 는 store.AgentStore 를 만족하는 SQLite 구현체.
// 구체 타입이 public 인 이유: cmd/hub 에서 ResetAllOnline 같은
// 인터페이스 밖 구체 타입 메서드를 호출할 수 있도록 하기 위함.
type AgentStore struct {
	db *sql.DB
	q  *Queries
}

// NewAgentStore 는 sql.DB 를 받아 AgentStore 를 만든다.
// 호출자는 sql.DB 의 생명주기를 직접 관리한다(Close 는 호출자 책임).
func NewAgentStore(db *sql.DB) *AgentStore {
	return &AgentStore{db: db, q: New(db)}
}

// 컴파일 타임 인터페이스 만족 확인. 이 줄은 F-7 검증 대상.
var _ store.AgentStore = (*AgentStore)(nil)

// Create 는 새 Agent 를 INSERT 한다. name UNIQUE 충돌은 store.ErrDuplicate 로 번역.
func (s *AgentStore) Create(ctx context.Context, a store.Agent) error {
	labelsJSON, err := encodeLabels(a.Labels)
	if err != nil {
		return fmt.Errorf("sqlite.Create: marshal labels: %w", err)
	}
	arg := CreateAgentParams{
		ID:         a.ID,
		Name:       a.Name,
		TokenHash:  a.TokenHash,
		Labels:     labelsJSON,
		Status:     a.Status,
		LastSeenAt: nullTime(a.LastSeenAt),
		CreatedAt:  a.CreatedAt.UTC().Format(iso8601),
	}
	if err := s.q.CreateAgent(ctx, arg); err != nil {
		if isUniqueViolation(err) {
			return store.ErrDuplicate
		}
		return fmt.Errorf("sqlite.Create: %w", err)
	}
	return nil
}

// GetByName 은 name 으로 Agent 를 찾는다. 없으면 store.ErrNotFound.
func (s *AgentStore) GetByName(ctx context.Context, name string) (*store.Agent, error) {
	row, err := s.q.GetAgentByName(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlite.GetByName: %w", err)
	}
	return rowToAgent(row)
}

// GetByID 는 id 로 Agent 를 찾는다. 없으면 store.ErrNotFound.
func (s *AgentStore) GetByID(ctx context.Context, id string) (*store.Agent, error) {
	row, err := s.q.GetAgentByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlite.GetByID: %w", err)
	}
	return rowToAgent(row)
}

// List 는 created_at DESC 순으로 모든 Agent 를 반환한다.
func (s *AgentStore) List(ctx context.Context) ([]store.Agent, error) {
	rows, err := s.q.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlite.List: %w", err)
	}
	out := make([]store.Agent, 0, len(rows))
	for _, r := range rows {
		a, err := rowToAgent(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

// UpdateStatus 는 status 와 last_seen_at 을 동시 갱신한다. 미존재 id 는 store.ErrNotFound.
func (s *AgentStore) UpdateStatus(ctx context.Context, id string, status string, lastSeenAt time.Time) error {
	res, err := s.db.ExecContext(ctx, updateAgentStatus, status, lastSeenAt.UTC().Format(iso8601), id)
	if err != nil {
		return fmt.Errorf("sqlite.UpdateStatus: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite.UpdateStatus: rowsAffected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Delete 는 id 로 Agent 레코드를 삭제한다. 미존재 id 는 store.ErrNotFound.
func (s *AgentStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, deleteAgent, id)
	if err != nil {
		return fmt.Errorf("sqlite.Delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite.Delete: rowsAffected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetBuildConfig 는 agentID 의 빌드 설정을 반환한다 (Phase 4 §4-1).
// DB NULL → ([]string{}, 0). port=0 / commands 빈 슬라이스 = "기본값 적용".
// build_commands 의 raw 텍스트는 \n 으로 split + trim + 빈 줄 제거를 거쳐 []string 으로 변환된다.
func (s *AgentStore) GetBuildConfig(ctx context.Context, agentID string) ([]string, int, error) {
	row, err := s.q.GetAgentBuildConfig(ctx, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, store.ErrNotFound
		}
		return nil, 0, fmt.Errorf("sqlite.GetBuildConfig: %w", err)
	}
	cmds := []string{}
	if row.RunCommands.Valid {
		cmds = splitBuildCommands(row.RunCommands.String)
	}
	port := 0
	if row.ContainerPort.Valid {
		port = int(row.ContainerPort.Int64)
	}
	return cmds, port, nil
}

// SaveBuildConfig 는 raw textarea 텍스트와 port 를 저장한다 (Phase 4 §4-1).
// rawCommands "" → build_commands = NULL (NULLIF 가 처리). port == 0 → container_port = NULL.
func (s *AgentStore) SaveBuildConfig(ctx context.Context, agentID string, rawCommands string, port int) error {
	if err := s.q.SaveAgentBuildConfig(ctx, SaveAgentBuildConfigParams{
		RawCommands: rawCommands,
		Port:        int64(port),
		ID:          agentID,
	}); err != nil {
		return fmt.Errorf("sqlite.SaveBuildConfig: %w", err)
	}
	return nil
}

// splitBuildCommands 는 textarea raw 텍스트를 []string 으로 분해한다.
// 각 라인 trim + 빈 줄 제거. Phase 4 결정 13.
func splitBuildCommands(raw string) []string {
	if raw == "" {
		return []string{}
	}
	// \r\n 정규화 → \n.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.Split(normalized, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ResetAllOnline 은 status='online' 레코드를 일괄 offline 으로 리셋한다.
// Hub 기동 시점에 cmd/hub 가 직접 호출하는 "운영 특수" 경로(결정 11, 리스크 4).
// 인터페이스 AgentStore 에는 포함하지 않는다.
// 반환: 영향 받은 row 수.
func (s *AgentStore) ResetAllOnline(ctx context.Context) (int64, error) {
	const q = `UPDATE agents SET status = 'offline', last_seen_at = ? WHERE status = 'online'`
	res, err := s.db.ExecContext(ctx, q, time.Now().UTC().Format(iso8601))
	if err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllOnline: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite.ResetAllOnline: rowsAffected: %w", err)
	}
	return n, nil
}

func rowToAgent(r Agent) (*store.Agent, error) {
	labels, err := decodeLabels(r.Labels)
	if err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	createdAt, err := time.Parse(iso8601, r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	a := &store.Agent{
		ID:        r.ID,
		Name:      r.Name,
		TokenHash: r.TokenHash,
		Labels:    labels,
		Status:    r.Status,
		CreatedAt: createdAt,
	}
	if r.LastSeenAt.Valid {
		ls, err := time.Parse(iso8601, r.LastSeenAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse last_seen_at: %w", err)
		}
		a.LastSeenAt = &ls
	}
	return a, nil
}

func encodeLabels(m map[string]string) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeLabels(s string) (map[string]string, error) {
	if s == "" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func nullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(iso8601), Valid: true}
}

// isUniqueViolation 은 modernc.org/sqlite 의 UNIQUE 제약 위반 에러를 감지한다.
// driver 메시지는 "constraint failed: UNIQUE constraint failed: agents.name (2067)" 형태.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// SQLite extended result code 2067 = SQLITE_CONSTRAINT_UNIQUE.
	if strings.Contains(msg, "2067") {
		return true
	}
	// primary rc 19 = SQLITE_CONSTRAINT.
	if strings.Contains(msg, "UNIQUE constraint failed") {
		return true
	}
	return false
}
