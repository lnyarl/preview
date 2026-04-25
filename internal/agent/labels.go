// 이 파일의 책임:
//   - PreviewConfig + previewID 로부터 Traefik 컨테이너 라벨 맵 생성 (ServiceLabels).
//   - compose 모드에서 사용할 override YAML 직렬화 (ComposeOverrideYAML).
//   - STATUS_UPDATE 에 실리는 preview_urls 맵 생성 (PreviewURLs).
//
// §4-5, 결정 5, 10, 11, 17.
package agent

import (
	"fmt"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

const traefikNetwork = "preview-net"

// ServiceLabels 는 단일 서비스의 Traefik 컨테이너 라벨 맵을 반환한다.
//   - Dockerfile 모드: docker ContainerCreate Labels 에 직접 사용.
//   - compose 모드: ComposeOverrideYAML 내 각 서비스의 labels 섹션에 사용.
//
// routerName = "{previewID}-{svcName}", fullPath = "/{previewID}{svc.Path}".
func ServiceLabels(previewID, svcName string, svc PreviewService) map[string]string {
	routerName := previewID + "-" + svcName
	fullPath := "/" + previewID + svc.Path

	labels := map[string]string{
		"traefik.enable":        "true",
		"traefik.docker.network": traefikNetwork,
		"traefik.http.routers." + routerName + ".rule":                              "PathPrefix(`" + fullPath + "`)",
		"traefik.http.routers." + routerName + ".entrypoints":                       "web",
		"traefik.http.services." + routerName + ".loadbalancer.server.port":         strconv.Itoa(svc.Port),
	}
	if svc.Strip {
		stripName := routerName + "-strip"
		labels["traefik.http.routers."+routerName+".middlewares"] = stripName
		labels["traefik.http.middlewares."+stripName+".stripprefix.prefixes"] = fullPath
	}
	return labels
}

// overrideServiceDef 는 compose override 의 각 서비스 항목.
type overrideServiceDef struct {
	Networks []string `yaml:"networks"`
	Labels   []string `yaml:"labels"`
}

// overrideNetworkDef 는 compose override 의 네트워크 항목.
type overrideNetworkDef struct {
	External bool `yaml:"external"`
}

type composeOverride struct {
	Services map[string]overrideServiceDef `yaml:"services"`
	Networks map[string]overrideNetworkDef `yaml:"networks"`
}

// ComposeOverrideYAML 은 previewID + cfg 로부터 compose override 파일 내용을
// 직렬화해 반환한다 (결정 5).
//
// 생성 형태:
//
//	services:
//	  {name}:
//	    networks: [preview-net]
//	    labels: ["traefik.enable=true", ...]
//	networks:
//	  preview-net:
//	    external: true
func ComposeOverrideYAML(previewID string, cfg PreviewConfig) ([]byte, error) {
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make(map[string]overrideServiceDef, len(cfg.Services))
	for _, name := range names {
		svc := cfg.Services[name]
		lblMap := ServiceLabels(previewID, name, svc)

		lblKeys := make([]string, 0, len(lblMap))
		for k := range lblMap {
			lblKeys = append(lblKeys, k)
		}
		sort.Strings(lblKeys)

		lblList := make([]string, len(lblKeys))
		for i, k := range lblKeys {
			lblList[i] = k + "=" + lblMap[k]
		}

		services[name] = overrideServiceDef{
			Networks: []string{traefikNetwork},
			Labels:   lblList,
		}
	}

	override := composeOverride{
		Services: services,
		Networks: map[string]overrideNetworkDef{
			traefikNetwork: {External: true},
		},
	}
	return yaml.Marshal(override)
}

// PreviewURLs 는 STATUS_UPDATE 에 실리는 service 이름 → 전체 URL 맵을 반환한다 (결정 11).
//
// URL 형식: http://{advHost}:{traefikPort}/{previewID}{svc.Path}.
// advHost 가 비어있으면 "127.0.0.1" 로 대체.
func PreviewURLs(previewID string, cfg PreviewConfig, advHost string, traefikPort int) map[string]string {
	if advHost == "" {
		advHost = "127.0.0.1"
	}
	urls := make(map[string]string, len(cfg.Services))
	for name, svc := range cfg.Services {
		urls[name] = fmt.Sprintf("http://%s:%d/%s%s", advHost, traefikPort, previewID, svc.Path)
	}
	return urls
}
