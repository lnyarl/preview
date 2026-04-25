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
	"fmt"
	"os"

	"github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
	"github.com/lnyarl/preview/internal/store"
)

// errPreviewsUsage 는 exit code 2 로 변환된다 (인자 오류).
var errPreviewsUsage = errors.New("usage: hub previews list | show <id>")

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
	default:
		return errPreviewsUsage
	}
}
