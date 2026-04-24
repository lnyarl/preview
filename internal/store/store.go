// Package store is the portability boundary between business logic and the
// underlying database driver.
//
// Phase 1 에서 AgentStore 인터페이스 타입이 메서드와 함께 처음 도입된다.
// 비즈니스 로직(internal/hub, internal/agent)은 반드시 이 인터페이스에만 의존하며,
// internal/db/sqlite 같은 구체 구현을 직접 import 해서는 안 된다(.golangci.yml depguard 로 강제).
//
// PreviewStore / JobStore 는 Phase 2 에서 Job·Preview 도메인 등장과 함께 추가된다.
package store

import (
	"context"
	"time"
)

// Agent 는 도메인 엔티티. sqlc 생성 구조체를 그대로 노출하지 않는 이유는
// 이식성 경계면을 sqlc 런타임 구현 세부에 결합시키지 않기 위함이다.
type Agent struct {
	ID         string
	Name       string
	TokenHash  string
	Labels     map[string]string
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
