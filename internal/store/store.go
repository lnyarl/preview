// Package store is the portability boundary between business logic and the
// underlying database driver.
//
// Phase 1 에서 AgentStore 인터페이스 타입이 메서드와 함께 처음 도입되었고,
// Phase 2 에서 PreviewStore 인터페이스가 추가된다. 비즈니스 로직(internal/hub,
// internal/agent)은 반드시 이 인터페이스에만 의존하며, internal/db/sqlite 같은
// 구체 구현을 직접 import 해서는 안 된다(.golangci.yml depguard 로 강제).
package store

import (
	"context"
	"time"
)

// Agent 는 도메인 엔티티. sqlc 생성 구조체를 그대로 노출하지 않는 이유는
// 이식성 경계면을 sqlc 런타임 구현 세부에 결합시키지 않기 위함이다.
//
// Labels 는 단순 값 슬라이스. 라우팅 매칭은 set-membership 으로 수행한다.
type Agent struct {
	ID         string
	Name       string
	TokenHash  string
	Labels     []string
	Status     string
	LastSeenAt *time.Time
	CreatedAt  time.Time
}

// AgentStore 는 agents 테이블에 대한 이식성 있는 저장소 인터페이스.
// 모든 메서드는 첫 인자로 context.Context 를 받는다.
type AgentStore interface {
	Create(ctx context.Context, a Agent) error
	GetByName(ctx context.Context, name string) (*Agent, error)
	GetByID(ctx context.Context, id string) (*Agent, error)
	List(ctx context.Context) ([]Agent, error)
	UpdateStatus(ctx context.Context, id string, status string, lastSeenAt time.Time) error
	Delete(ctx context.Context, id string) error
}

// Preview 는 previews 테이블에 대응하는 도메인 엔티티.
// nullable 필드는 포인터 또는 zero value 로 표현한다. JSON 직렬화 시
// internal/hub 의 PreviewView DTO 가 명시 변환을 담당한다.
//
// Phase 6: PublicURL 제거. 대신 RepoCloneURL (webhook 에서 추출한 git clone URL)
// 과 PreviewURLs (Agent 가 .preview.yml 기반으로 산출한 service→URL JSON 직렬화
// 문자열) 를 도입한다 — non-nullable string, 기본값 "".
//
// Phase 9: 자연키가 (repo_full_name, commit_sha) 로 변경. CommitSha 는 빈 문자열이
// SQLite 의 NULL 에 대응하며 (UNIQUE 미발동), 도메인 표현은 그대로 string 으로 유지한다.
// IsAdhoc 은 webhook(false)/Admin Test Build(true) 진입점을 구분한다.
type Preview struct {
	ID              string
	RepoFullName    string
	PrNumber        int
	CommitSha       string // 빈 문자열 = NULL (Phase 9 마이그레이션 후 nullable 컬럼)
	Branch          string
	Status          string
	AssignedAgentID *string
	ContainerID     *string
	AgentHost       *string
	AgentPort       *int
	RepoCloneURL    string
	PreviewURLs     string
	Labels          []string
	ErrorMessage    *string
	IsAdhoc         bool // Phase 9: webhook=false, admin Test Build=true
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PreviewFields 는 UpdateStatus 가 부수적으로 갱신할 수 있는 nullable 필드 묶음.
// 각 필드가 nil 이면 SQL COALESCE 로 기존 값을 보존한다.
// 빈 문자열 포인터(&"") 는 명시 갱신으로 취급한다.
//
// Phase 2 Step 1 에서는 정의만 도입한다. UpdateStatus 가 본 구조체를 받지만
// 본 Step 의 sqlc 구현은 status/error_message/updated_at 만 갱신하므로
// 다른 필드들은 무시한다(시그니처만 §5-1 정합 유지).
//
// Phase 6: PublicURL 제거, PreviewURLs 도입. PreviewURLs 은 Agent 가 산출한
// service→URL 매핑의 직렬화 문자열(현재 단계에서는 raw JSON 그대로 통과).
//
// Phase 9: CommitSha 추가 — Agent 가 worktree 의 git rev-parse HEAD 로 resolve 한
// 실제 sha 를 hub 에 보고할 때 사용. nil=무변경, &"abc"=NULL 일 때만 채움 시도
// (이미 채워진 sha 와 다른 값이면 ErrShaConflict 반환).
type PreviewFields struct {
	ContainerID     *string
	AgentHost       *string
	AgentPort       *int
	PreviewURLs     *string
	ErrorMessage    *string
	AssignedAgentID *string
	CommitSha       *string // Phase 9
}

// PreviewStore 는 previews + preview_events 두 테이블에 대한 이식성 있는 저장소 인터페이스.
//
// 결정 11(단일 진입점): preview.status 변경은 모두 UpdateStatus 또는 Claim 만이
// 수행한다. Upsert 는 status 를 절대 변경하지 않으며, 신규 INSERT 시점에만
// SQL DEFAULT 'queued' 로 들어간다.
//
// 결정 1: InsertPreviewEvent 는 인터페이스에 노출하지 않는다 — Upsert/UpdateStatus
// 내부에서 같은 트랜잭션으로 호출되어 status 변경 ↔ event 기록의 일관성을 보장.
//
// Phase 2 Step 1 에서는 Upsert / GetByID / GetByRepoAndPR / UpdateStatus / ListAll
// 만 실제 구현되며, 그 외 메서드는 stub 으로 ErrNotImplementedStep1 을 반환한다.
// 이 인터페이스의 메서드 9 개 시그니처는 기획서 §5-1 의 단일 진실 소스이다.
type PreviewStore interface {
	// Upsert 는 (repo_full_name, pr_number) 기준 신규 INSERT 또는 UPDATE 한다.
	// 신규일 때 status='queued' + preview_events(NULL→queued) 1행 함께 INSERT(트랜잭션).
	// 기존 row 일 때 commit_sha/branch/labels/updated_at 만 갱신, status 변경 없음.
	// 반환 created=true 는 신규 INSERT, false 는 UPDATE.
	// prev 는 UPDATE 직전의 row(재오픈 분기용). created=true 일 때 prev=nil.
	Upsert(ctx context.Context, p Preview) (created bool, prev *Preview, err error)

	// GetByID 는 id 로 Preview 를 찾는다. 없으면 ErrNotFound.
	GetByID(ctx context.Context, id string) (*Preview, error)

	// FindByHost 는 reverse proxy 라우팅용 lookup. running/building 상태만 반환한다.
	// Phase 2 Step 1 에서는 stub.
	FindByHost(ctx context.Context, repoFullName string, prNumber int) (*Preview, error)

	// ListQueuedForCandidates 는 dispatcher 라벨 매칭 후보 목록(status='queued', created_at ASC).
	// Phase 2 Step 2 에서 구현.
	ListQueuedForCandidates(ctx context.Context) ([]Preview, error)

	// Claim 은 race-free 로 candidate 중 하나를 'assigned' 로 점유한다.
	// 0행 매칭이면 ErrNotFound.
	// Phase 2 Step 2 에서 구현.
	Claim(ctx context.Context, candidateIDs []string, agentID string, now time.Time) (*Preview, error)

	// UpdateStatus 는 단일 트랜잭션에서 status CAS + preview_events INSERT 를 수행한다.
	// fromStatus 와 일치하지 않으면 ErrStaleState. fields 는 nullable, 본 Step 1 에서는
	// error_message 만 활용된다.
	UpdateStatus(ctx context.Context, id string, fromStatus, toStatus, message string, now time.Time, fields PreviewFields) error

	// ListRunningByAgent 는 reconciler 가 offline agent 의 running preview 보존 카운트용.
	// Phase 2 Step 3 에서 구현.
	ListRunningByAgent(ctx context.Context, agentID string) ([]Preview, error)

	// ListStaleAssigned 는 status='assigned' 이면서 updated_at < staleAfter 인 row.
	// Phase 2 Step 3 에서 구현.
	ListStaleAssigned(ctx context.Context, staleAfter time.Time) ([]Preview, error)

	// ListByAgent 는 agentID 의 assigned/building/running/teardown 상태 row 들.
	// Phase 2 Step 3 에서 구현.
	ListByAgent(ctx context.Context, agentID string, statuses []string) ([]Preview, error)

	// ListAll 은 모든 preview 를 created_at DESC 로 반환한다.
	// fixture 검증 편의(Step 1 evaluator 가 sqlite3 의존 없이 row 확인) 를 위한
	// 작은 확장. Step 2 의 ListQueuedForCandidates / Step 3 의 ListByAgent 와는
	// 직교 — admin /admin/previews 와 cmd/hub previews list 가 본 메서드를 사용.
	ListAll(ctx context.Context) ([]Preview, error)

	// ListPreviewEvents 는 preview detail 페이지의 timeline 렌더에 사용된다.
	// (Phase 3 §5-12). created_at ASC, id ASC 로 정렬.
	ListPreviewEvents(ctx context.Context, previewID string, limit, offset int) ([]PreviewEvent, error)

	// GetActiveByRepoAndPR 은 같은 (repo, pr) 의 in-flight row 1건을 반환한다.
	// 상태 ∈ {queued, assigned, building, running} 중 가장 최근 created_at row 만 매칭.
	// 없으면 ErrNotFound. Phase 9 webhook synchronize 분기 (Decision Matrix Case B) 에서
	// 기존 active 가 있는지 확인하기 위해 사용한다.
	GetActiveByRepoAndPR(ctx context.Context, repoFullName string, prNumber int) (*Preview, error)

	// FindAdhocByBranch 는 같은 (repo_full_name, branch) 의 가장 최근 adhoc preview 1건을
	// 반환한다. 상태 무관 — 호출자가 active/terminal 분기를 책임진다. 없으면 ErrNotFound.
	//
	// Phase 11: Admin Test Build 의 (repo, branch) 단위 dedup 진입점.
	// is_adhoc=1 row 만 매칭하므로 webhook(is_adhoc=false) 경로는 영향받지 않는다.
	// 동일 키 row 가 여러 건이면 created_at DESC 의 첫 번째 (= 가장 최근) row 가 반환된다.
	FindAdhocByBranch(ctx context.Context, repoFullName, branch string) (*Preview, error)

	// ListRepos 는 previews 테이블의 distinct repo_full_name 을 정렬 반환한다.
	// Phase 10 (결정 14): /admin/repos 인덱스 페이지가 repo_secrets 와 union 하기 위한
	// 진입점. SQL DISTINCT 를 사용해 메모리 폭주 없이 N→K 축소를 DB 측에서 수행.
	ListRepos(ctx context.Context) ([]string, error)
}

// PreviewEvent 는 preview_events 한 row 의 도메인 표현.
// FromStatus 가 NULL (최초 INSERT) 이면 nil.
type PreviewEvent struct {
	ID         string
	PreviewID  string
	FromStatus *string
	ToStatus   string
	Message    string
	CreatedAt  time.Time
}

// RepoSecret 은 repo_secrets 한 행의 도메인 표현 (Phase 10).
//
// Value 는 plaintext (Phase 10 결정 7 — 후속 Phase 에서 envelope encryption 으로
// in-place 마이그레이션 예정). 본 구조체는 절대 마샬링되어 와이어로 흘러가지 않으며,
// JOB_ASSIGN 의 BuildEnv 는 map[string]string 으로 별도 빌드된다.
//
// RepoFullName 은 store 진입점에서 lowercase 정규화된 형태가 항상 유지된다 (결정 15).
type RepoSecret struct {
	RepoFullName string
	Key          string
	Value        string
	UpdatedAt    time.Time
}

// RepoSecretStore 는 repo_secrets 테이블에 대한 이식성 있는 저장소 인터페이스 (Phase 10).
//
// 단일 진입점:
//   - List: 한 repo 의 모든 secret 행 (key 정렬)
//   - Upsert: 1 행 INSERT 또는 value 갱신
//   - Delete: 1 행 삭제
//   - DeleteAllFor: repo 단위 일괄 삭제 (현재 Admin UI 에선 미사용 — 추후 repo 삭제 동선용)
//   - ListRepos: distinct repo_full_name (인덱스 페이지용)
//
// 결정 15: 모든 메서드는 입력 repoFullName 을 lowercase 로 정규화한 뒤 사용한다.
type RepoSecretStore interface {
	List(ctx context.Context, repoFullName string) ([]RepoSecret, error)
	Upsert(ctx context.Context, s RepoSecret) error
	Delete(ctx context.Context, repoFullName, key string) error
	DeleteAllFor(ctx context.Context, repoFullName string) error
	ListRepos(ctx context.Context) ([]string, error)
}
