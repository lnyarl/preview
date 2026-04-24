// 이 파일의 책임:
//   - log/slog 기반 구조화 로거 셋업.
//   - LOG_LEVEL env 를 slog.Level 로 파싱.
//
// 포맷: 기본 text(TTY 에서 가독성), LOG_FORMAT=json 으로 JSON 선택 가능.
// Phase 1 에서는 text 기본값이 기획서의 주요 이벤트 grep 검증에 충분.
package hub

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger 는 주어진 레벨 문자열로 *slog.Logger 를 만든다.
func NewLogger(level string) *slog.Logger {
	return newLoggerTo(os.Stdout, level)
}

func newLoggerTo(w io.Writer, level string) *slog.Logger {
	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
