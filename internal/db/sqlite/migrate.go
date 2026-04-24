// 이 파일의 책임:
//   - golang-migrate/migrate/v4 라이브러리로 임베드된 마이그레이션을 SQLite 에 적용.
//   - migrate.Close 가 database driver 를 닫아버리므로, 호출자 DB 를 공유하지 않고
//     동일 DSN 으로 별도 sql.DB 를 열어 사용한다.
//
// 참고: 결정 6 (migrate 라이브러리 임베드), 결정 7 (DATABASE_URL 분기).
package sqlitestore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
)

// MigrateUp 는 임베드된 마이그레이션을 모두 적용한다.
// dsn 은 DATABASE_URL 형식 (sqlite://path 또는 postgres://...).
// 반환 applied 는 호출로 전진한 마이그레이션 수 (before->after 버전 증가량).
func MigrateUp(dsn string) (applied int, err error) {
	db, close, err := openMigrationDB(dsn)
	if err != nil {
		return 0, err
	}
	defer close()

	m, err := newMigrator(db)
	if err != nil {
		return 0, err
	}
	// migrate.Close 는 database driver 를 닫으므로 여기서는 source 만 정리한다.
	defer func() {
		_, _ = m.Close()
	}()

	before, _, beforeErr := m.Version()
	if beforeErr != nil && !errors.Is(beforeErr, migrate.ErrNilVersion) {
		return 0, fmt.Errorf("migrate.version(before): %w", beforeErr)
	}
	if errors.Is(beforeErr, migrate.ErrNilVersion) {
		before = 0
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("migrate.up: %w", err)
	}

	after, _, afterErr := m.Version()
	if afterErr != nil && !errors.Is(afterErr, migrate.ErrNilVersion) {
		return 0, fmt.Errorf("migrate.version(after): %w", afterErr)
	}
	if errors.Is(afterErr, migrate.ErrNilVersion) {
		after = 0
	}

	if after >= before {
		return int(after - before), nil
	}
	return 0, nil
}

// MigrateDown 은 정확히 1 단계만 되돌린다.
func MigrateDown(dsn string) error {
	db, close, err := openMigrationDB(dsn)
	if err != nil {
		return err
	}
	defer close()
	m, err := newMigrator(db)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate.down: %w", err)
	}
	return nil
}

// MigrateVersion 은 현재 migrate 버전과 dirty 여부를 반환한다.
func MigrateVersion(dsn string) (version uint, dirty bool, err error) {
	db, close, oerr := openMigrationDB(dsn)
	if oerr != nil {
		return 0, false, oerr
	}
	defer close()
	m, merr := newMigrator(db)
	if merr != nil {
		return 0, false, merr
	}
	defer func() { _, _ = m.Close() }()
	v, d, vErr := m.Version()
	if errors.Is(vErr, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if vErr != nil {
		return 0, false, fmt.Errorf("migrate.version: %w", vErr)
	}
	return v, d, nil
}

// openMigrationDB 는 마이그레이션 전용 sql.DB 를 연다. 반환된 close 는 멱등.
func openMigrationDB(dsn string) (*sql.DB, func(), error) {
	if !strings.HasPrefix(dsn, "sqlite://") {
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			return nil, nil, fmt.Errorf("postgres driver not wired in phase 1")
		}
		return nil, nil, fmt.Errorf("unsupported DATABASE_URL scheme: %q", dsn)
	}
	path := strings.TrimPrefix(dsn, "sqlite://")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite.Open: %w", err)
	}
	db.SetMaxOpenConns(1)
	// migrate 자체 실행 시에도 busy_timeout 은 도움이 된다.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("sqlite.busy_timeout: %w", err)
	}
	return db, func() { _ = db.Close() }, nil
}

func newMigrator(db *sql.DB) (*migrate.Migrate, error) {
	src, err := MigrationsSource()
	if err != nil {
		return nil, fmt.Errorf("migrations source: %w", err)
	}
	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return nil, fmt.Errorf("migrate sqlite driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return nil, fmt.Errorf("migrate instance: %w", err)
	}
	return m, nil
}
