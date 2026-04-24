// 이 파일의 책임:
//   - env 및 flag 로부터 Hub 설정 로딩.
//   - 기본값은 HUB_ADDR=:3000, DATABASE_URL=sqlite://./hub.db, BCRYPT_COST=10, LOG_LEVEL=info.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-7.
package hub

import (
	"os"
	"strconv"
)

// Config 는 Hub 기동에 필요한 런타임 설정.
type Config struct {
	Addr        string // 바인드 주소. :3000 기본
	DatabaseURL string // DB DSN
	BcryptCost  int    // bcrypt cost, 기본 10
	LogLevel    string // slog 레벨 (debug/info/warn/error)
}

// DefaultConfig 는 env 를 읽어 기본값이 채워진 Config 를 만든다.
// 플래그 오버라이드는 cmd/hub 가 수행한다.
func DefaultConfig() Config {
	return Config{
		Addr:        envOr("HUB_ADDR", ":3000"),
		DatabaseURL: envOr("DATABASE_URL", "sqlite://./hub.db"),
		BcryptCost:  envInt("BCRYPT_COST", 10),
		LogLevel:    envOr("LOG_LEVEL", "info"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
