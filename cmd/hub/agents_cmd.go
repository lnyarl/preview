// 이 파일의 책임:
//   - "hub agents list" 서브커맨드: DB 에 read-only 접근해 AgentView JSON 배열을 stdout 에 출력.
//
// evaluator 가 jq/sqlite3 없이도 상태를 확인할 수 있도록 제공. HTTP 를 거치지 않으며
// Hub 데몬이 기동 중이 아니어도 동작한다.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	sqlitestore "github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
)

// errAgentsUsage 는 exit code 2 로 변환된다 (인자 오류).
var errAgentsUsage = errors.New("usage: hub agents list")

func runAgents(args []string) error {
	if len(args) == 0 {
		return errAgentsUsage
	}
	if args[0] != "list" {
		return errAgentsUsage
	}
	_ = godotenv.Load()
	cfg := hub.DefaultConfig()
	ctx := context.Background()
	db, err := sqlitestore.OpenURL(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	s := sqlitestore.NewAgentStore(db)
	list, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	// admin_handler.AgentView 와 동일한 JSON shape 을 유지해야 함.
	type agentView struct {
		ID         string            `json:"id"`
		Name       string            `json:"name"`
		Labels     map[string]string `json:"labels"`
		Status     string            `json:"status"`
		LastSeenAt *string           `json:"last_seen_at"`
		CreatedAt  string            `json:"created_at"`
	}
	views := make([]agentView, 0, len(list))
	for _, a := range list {
		labels := a.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		v := agentView{
			ID:        a.ID,
			Name:      a.Name,
			Labels:    labels,
			Status:    a.Status,
			CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if a.LastSeenAt != nil {
			s := a.LastSeenAt.UTC().Format(time.RFC3339Nano)
			v.LastSeenAt = &s
		}
		views = append(views, v)
	}
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(views)
}
