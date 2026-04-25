// preview_config.go 는 저장소 루트의 preview.yml 을 파싱한다.
// 파일이 없으면 기본값(docker build + port 80)으로 fallback.
package agent

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// PreviewConfig 는 저장소 루트의 preview.yml 구조체.
type PreviewConfig struct {
	// Build: 순서대로 실행할 셸 명령어 목록.
	// 사용 가능한 환경변수: PREVIEW_ID, PREVIEW_IMAGE, PREVIEW_SHA, PREVIEW_BRANCH
	Build []string `yaml:"build"`

	// Port: 컨테이너가 노출하는 포트 (기본: 80).
	Port int `yaml:"port"`
}

// defaultPreviewConfig 는 preview.yml 이 없을 때 사용하는 기본값.
func defaultPreviewConfig(image string) PreviewConfig {
	return PreviewConfig{
		Build: []string{"docker build -t $PREVIEW_IMAGE ."},
		Port:  80,
	}
}

// loadPreviewConfig 는 worktreePath/preview.yml 을 읽어 PreviewConfig 를 반환한다.
// 파일이 없거나 파싱에 실패하면 기본값을 반환한다.
func loadPreviewConfig(worktreePath, image string) PreviewConfig {
	path := filepath.Join(worktreePath, "preview.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		// 파일 없음 — 기본값
		return defaultPreviewConfig(image)
	}
	var cfg PreviewConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// 파싱 실패 — 기본값
		return defaultPreviewConfig(image)
	}
	if len(cfg.Build) == 0 {
		cfg.Build = []string{"docker build -t $PREVIEW_IMAGE ."}
	}
	if cfg.Port == 0 {
		cfg.Port = 80
	}
	return cfg
}
