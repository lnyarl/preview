// 이 파일의 책임:
//   - .preview.yml / .preview.yaml 설정 파일 파싱 및 검증.
//   - LoadPreviewConfig: worktree 경로에서 파일 탐색 → YAML 파싱 → 검증 → PreviewConfig 반환.
//   - FirstService: services 맵 알파벳 정렬 후 첫 진입점 반환.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// ErrPreviewConfigMissing 은 worktree 에 .preview.yml / .preview.yaml 둘 다
// 존재하지 않을 때 반환된다. 호출자는 errors.Is 로 식별한다.
var ErrPreviewConfigMissing = errors.New("preview config missing")

// 검증 실패 시 사용되는 reason 토큰. 호출자는 fmt.Errorf 메시지에 포함된
// 문자열을 substring 으로 검사한다 (errors.Is 보다는 reason 문자열 매칭).
const (
	reasonServicesEmpty     = "services_empty"
	reasonServiceNameInvalid = "service_name_invalid"
	reasonPortInvalid        = "port_invalid"
	reasonPathInvalid        = "path_invalid"
	reasonDuplicatePath      = "duplicate_path"
	reasonBuildFileNotFound  = "build_file_not_found"
	reasonYAMLParsePrefix    = "yaml_parse"
)

// service name 은 소문자 영숫자/하이픈/언더스코어만 허용.
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// PreviewService 는 단일 프리뷰 서비스의 라우팅·노출 정보.
type PreviewService struct {
	Port  int
	Path  string
	Strip bool // 파싱 시 default true 적용
}

// PreviewConfig 는 .preview.yml 의 파싱 결과.
//   - ComposeFile / Dockerfile 은 명시 시 auto-detect 를 건너뛴다.
//   - Services 의 key 는 service name.
type PreviewConfig struct {
	ComposeFile string                    // optional, 명시 시 auto-detect 건너뜀
	Dockerfile  string                    // optional, 명시 시 auto-detect 건너뜀
	Services    map[string]PreviewService // key = service name
}

// FirstService 는 services 맵을 알파벳 오름차순 정렬해 첫 번째 (name, svc) 반환.
// services 가 비어있으면 ok=false.
func (c PreviewConfig) FirstService() (name string, svc PreviewService, ok bool) {
	if len(c.Services) == 0 {
		return "", PreviewService{}, false
	}
	keys := make([]string, 0, len(c.Services))
	for k := range c.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := keys[0]
	return first, c.Services[first], true
}

// rawPreviewService 는 YAML 파싱 전용 구조체. strip 의 default=true 를
// 표현하기 위해 *bool 포인터를 사용한다 (yaml.v3 는 bool 의 zero value 가 false 라
// "생략" 과 "false 명시" 구분이 불가).
type rawPreviewService struct {
	Port  int    `yaml:"port"`
	Path  string `yaml:"path"`
	Strip *bool  `yaml:"strip"`
}

type rawPreviewConfig struct {
	ComposeFile string                       `yaml:"compose_file"`
	Dockerfile  string                       `yaml:"dockerfile"`
	Services    map[string]rawPreviewService `yaml:"services"`
}

// LoadPreviewConfig 는 worktree 안의 .preview.yml 또는 .preview.yaml 을
// 읽어 PreviewConfig 로 반환한다.
//
// 탐색 순서: .preview.yml → .preview.yaml. 둘 다 없으면 ErrPreviewConfigMissing.
// YAML 파싱 실패 시 ErrPreviewConfigInvalid("yaml_parse: ...").
// 검증 실패 시 ErrPreviewConfigInvalid(reason).
func LoadPreviewConfig(worktree string) (PreviewConfig, error) {
	path, err := findConfigFile(worktree)
	if err != nil {
		return PreviewConfig{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// 탐색 직후 read 실패 — race 가 아니면 일반적이지 않음. missing 으로 취급.
		if errors.Is(err, os.ErrNotExist) {
			return PreviewConfig{}, ErrPreviewConfigMissing
		}
		return PreviewConfig{}, fmt.Errorf("preview config read: %w", err)
	}

	var raw rawPreviewConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return PreviewConfig{}, invalid(fmt.Sprintf("%s: %s", reasonYAMLParsePrefix, err.Error()))
	}

	cfg := PreviewConfig{
		ComposeFile: raw.ComposeFile,
		Dockerfile:  raw.Dockerfile,
	}

	// 명시된 build 파일은 worktree 기준 상대경로 해석 후 존재 검사.
	if raw.ComposeFile != "" {
		if !buildFileExists(worktree, raw.ComposeFile) {
			return PreviewConfig{}, invalid(reasonBuildFileNotFound)
		}
	}
	if raw.Dockerfile != "" {
		if !buildFileExists(worktree, raw.Dockerfile) {
			return PreviewConfig{}, invalid(reasonBuildFileNotFound)
		}
	}

	// services 검증.
	if len(raw.Services) == 0 {
		return PreviewConfig{}, invalid(reasonServicesEmpty)
	}

	services := make(map[string]PreviewService, len(raw.Services))
	seenPaths := make(map[string]string, len(raw.Services)) // path -> serviceName

	// service 검증을 결정적 순서로 (테스트 안정성).
	names := make([]string, 0, len(raw.Services))
	for name := range raw.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		raws := raw.Services[name]
		if !serviceNamePattern.MatchString(name) {
			return PreviewConfig{}, invalid(reasonServiceNameInvalid)
		}
		if raws.Port <= 0 || raws.Port > 65535 {
			return PreviewConfig{}, invalid(reasonPortInvalid)
		}
		if len(raws.Path) == 0 || raws.Path[0] != '/' {
			return PreviewConfig{}, invalid(reasonPathInvalid)
		}
		if _, dup := seenPaths[raws.Path]; dup {
			return PreviewConfig{}, invalid(reasonDuplicatePath)
		}
		seenPaths[raws.Path] = name

		// strip default = true.
		strip := true
		if raws.Strip != nil {
			strip = *raws.Strip
		}
		services[name] = PreviewService{
			Port:  raws.Port,
			Path:  raws.Path,
			Strip: strip,
		}
	}
	cfg.Services = services
	return cfg, nil
}

// findConfigFile 은 worktree 에서 .preview.yml 우선, 그 다음 .preview.yaml 를
// 찾는다. 둘 다 없으면 ErrPreviewConfigMissing.
func findConfigFile(worktree string) (string, error) {
	for _, name := range []string{".preview.yml", ".preview.yaml"} {
		p := filepath.Join(worktree, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", ErrPreviewConfigMissing
}

// buildFileExists 는 worktree 기준 상대경로면 join, 절대경로면 그대로 검사.
func buildFileExists(worktree, ref string) bool {
	target := ref
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktree, ref)
	}
	st, err := os.Stat(target)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

// invalid 는 reason 을 표준 prefix 로 감싸 에러 객체로 만든다.
func invalid(reason string) error {
	return fmt.Errorf("preview config invalid: %s", reason)
}
