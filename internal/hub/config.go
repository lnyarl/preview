// 이 파일의 책임:
//   - env 및 flag 로부터 Hub 설정 로딩.
//   - 기본값은 HUB_ADDR=:3000, DATABASE_URL=sqlite://./hub.db, BCRYPT_COST=10, LOG_LEVEL=info.
//   - Phase 2 추가: GITHUB_WEBHOOK_SECRET (필수, 시작 시 fail-fast),
//     PREVIEW_BASE_DOMAIN (기본 localhost — 프록시 매칭 base, Step 3 에서 활성).
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-7 +
//
//	docs/specs/phase-2-webhook-dispatch-proxy.md §5-8.
package hub

import (
	"errors"
	"os"
	"strconv"
	"time"
)

// ErrWebhookSecretMissing 은 GITHUB_WEBHOOK_SECRET 이 비어있을 때 반환된다(NF-Security-3).
var ErrWebhookSecretMissing = errors.New("config: GITHUB_WEBHOOK_SECRET required for webhook")

// Config 는 Hub 기동에 필요한 런타임 설정.
type Config struct {
	Addr              string        // 바인드 주소. :3000 기본
	DatabaseURL       string        // DB DSN
	BcryptCost        int           // bcrypt cost, 기본 10
	LogLevel          string        // slog 레벨 (debug/info/warn/error)
	WebhookSecret     string        // GitHub webhook HMAC secret. 비어있으면 fail-fast.
	PreviewBaseDomain string        // reverse proxy 호스트 매칭 base. Step 3 에서 사용.
	PreviewRepoURL    string        // 단일 레포 가정(결정 9). webhook 의 repo_full_name 을 git URL 로 변환.
	ReconcileInterval   time.Duration // reconciler 주기 (기본 60s).
	StaleAssignedAfter time.Duration // assigned 임계 (기본 5m).
	AdminPassword     string        // Phase 3: /admin/* Basic Auth 비밀번호. 빈 값 = 인증 disable + WARN.
}

// DefaultConfig 는 env 를 읽어 기본값이 채워진 Config 를 만든다.
// 플래그 오버라이드는 cmd/hub 가 수행한다. Validate 는 별도 호출.
func DefaultConfig() Config {
	return Config{
		Addr:              envOr("HUB_ADDR", ":3000"),
		DatabaseURL:       envOr("DATABASE_URL", "sqlite://./hub.db"),
		BcryptCost:        envInt("BCRYPT_COST", 10),
		LogLevel:          envOr("LOG_LEVEL", "info"),
		WebhookSecret:     os.Getenv("GITHUB_WEBHOOK_SECRET"),
		PreviewBaseDomain:  envOr("PREVIEW_BASE_DOMAIN", "localhost"),
		PreviewRepoURL:     os.Getenv("PREVIEW_REPO_URL"),
		ReconcileInterval:  envDuration("RECONCILE_INTERVAL", 60*time.Second),
		StaleAssignedAfter: envDuration("STALE_ASSIGNED_AFTER", 5*time.Minute),
		AdminPassword:      os.Getenv("ADMIN_PASSWORD"),
	}
}

// Validate 는 Hub 데몬 기동 직전 필수 설정의 존재를 검증한다.
// 마이그레이션/CLI 서브커맨드는 이 검증을 우회할 수 있다(데몬만 secret 필요).
func (c Config) Validate() error {
	if c.WebhookSecret == "" {
		return ErrWebhookSecretMissing
	}
	return nil
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

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
