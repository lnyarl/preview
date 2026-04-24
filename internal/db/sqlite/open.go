// 이 파일의 책임:
//   - DATABASE_URL 에서 파일 경로를 추출하고 modernc.org/sqlite 드라이버로 sql.DB 를 연다.
//   - 연결 직후 PRAGMA journal_mode=WAL, synchronous=NORMAL, busy_timeout=5000, foreign_keys=ON 실행.
//   - 단일 writer 보장을 위해 SetMaxOpenConns(1) 설정.
//
// 참고: 결정 7 (DATABASE_URL 분기), 결정 8 (WAL + 단일 writer).
package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	// blank import: register "sqlite" driver with database/sql.
	_ "modernc.org/sqlite"
)

// OpenURL 은 DATABASE_URL 을 받아 sql.DB 를 연다.
// sqlite:// 로 시작하면 SQLite 파일을 연다. postgres:// 는 Phase 1 에서 미구현 에러.
// 그 외 스키마는 드라이버 인식 실패 에러.
func OpenURL(ctx context.Context, dsn string) (*sql.DB, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		path := strings.TrimPrefix(dsn, "sqlite://")
		if path == "" {
			return nil, fmt.Errorf("sqlite: empty path in DATABASE_URL")
		}
		return openSQLite(ctx, path)
	case strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://"):
		return nil, fmt.Errorf("postgres driver not wired in phase 1")
	default:
		return nil, fmt.Errorf("unsupported DATABASE_URL scheme: %q", dsn)
	}
}

func openSQLite(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite.Open: %w", err)
	}
	// 단일 writer 보장: 결정 8.
	db.SetMaxOpenConns(1)
	if err := applyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func applyPragmas(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("sqlite.pragma(%q): %w", p, err)
		}
	}
	return nil
}

// FilePathFromURL 은 sqlite:// DSN 에서 파일 경로를 추출한다.
// 마이그레이션 라이브러리(golang-migrate)가 별도로 파일 경로를 요구할 때 사용한다.
// postgres/기타 스키마에서는 빈 문자열과 false.
func FilePathFromURL(dsn string) (string, bool) {
	if !strings.HasPrefix(dsn, "sqlite://") {
		return "", false
	}
	return strings.TrimPrefix(dsn, "sqlite://"), true
}
