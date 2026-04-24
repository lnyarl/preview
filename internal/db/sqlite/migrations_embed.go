// 이 파일의 책임:
//   - db/migrations/*.sql 을 바이너리에 임베드한다.
//   - golang-migrate/migrate/v4 가 사용할 수 있는 source.Driver 를 반환한다.
//
// 참고: 결정 6 (migrate 라이브러리 임베드, CLI 별도 설치 불요).
package sqlitestore

import (
	"embed"
	"io/fs"

	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrationsSource 는 임베드된 migrations 디렉토리에 대한 migrate source.Driver 를 만든다.
func MigrationsSource() (source.Driver, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	return iofs.New(sub, ".")
}
