// 이 파일의 책임:
//   - "hub previews list" 서브커맨드: previews 테이블 read-only 조회.
//   - "hub previews show <id>" 서브커맨드: 단건 조회.
//
// evaluator 가 sqlite3 없이도 row 와 status 를 확인할 수 있도록 제공한다(F-S1-0,
// F-S1-9 등). HTTP 를 거치지 않으며 Hub 데몬 기동 여부와 무관하게 동작한다.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
	"github.com/lnyarl/preview/internal/store"
)

// errPreviewsUsage 는 exit code 2 로 변환된다 (인자 오류).
var errPreviewsUsage = errors.New("usage: hub previews list | show <id> | seed-stale [--pr=N]")

func runPreviews(args []string) error {
	if len(args) == 0 {
		return errPreviewsUsage
	}
	cfg := hub.DefaultConfig()
	ctx := context.Background()
	db, err := sqlitestore.OpenURL(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()
	s := sqlitestore.NewPreviewStore(db)

	switch args[0] {
	case "list":
		list, err := s.ListAll(ctx)
		if err != nil {
			return fmt.Errorf("list previews: %w", err)
		}
		views := make([]hub.PreviewView, 0, len(list))
		for _, p := range list {
			views = append(views, hub.PreviewToView(p))
		}
		return json.NewEncoder(os.Stdout).Encode(views)
	case "show":
		if len(args) < 2 {
			return errPreviewsUsage
		}
		p, err := s.GetByID(ctx, args[1])
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("preview not found: %s", args[1])
			}
			return fmt.Errorf("get preview: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(hub.PreviewToView(*p))
	case "seed-stale":
		// evaluator/F-S3-10 검증용: status='assigned', updated_at=10 분 전인 row INSERT.
		seedFS := flag.NewFlagSet("seed-stale", flag.ContinueOnError)
		prNum := seedFS.Int("pr", 1, "PR number to seed")
		if err := seedFS.Parse(args[1:]); err != nil {
			return err
		}
		id := uuid.NewString()
		now := time.Now().UTC()
		_, err = db.ExecContext(ctx,
			`INSERT INTO previews (id, repo_full_name, pr_number, commit_sha, branch, status, labels, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "fixture/repo", int64(*prNum), "aabbcc", "pr-fixture",
			"assigned", "{}",
			now.Add(-15*time.Minute).Format(time.RFC3339Nano),
			now.Add(-10*time.Minute).Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("seed-stale: %w", err)
		}
		fmt.Fprintf(os.Stdout, `{"preview_id": %q, "status": "assigned"}`+"\n", id)
		return nil
	default:
		return errPreviewsUsage
	}
}
