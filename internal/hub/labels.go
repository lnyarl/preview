// 이 파일의 책임:
//   - LabelsMatch: preview 가 요구하는 라벨이 agent 라벨 집합에 모두 포함되는지 여부.
//   - 라벨 매칭은 SQL 이 아닌 Go 메모리에서 수행 (이식성 원칙, 결정 4 / NF-Portability-3).
//
// 본 함수는 Phase 2 Step 1 에서 인터페이스만 도입(Step 2 dispatcher 가 호출).
// 단위 테스트로 결정 4 의 6 case + nil edge 를 커버한다(F-S2-3).
package hub

// LabelsMatch 는 preview 가 요구하는 모든 라벨 값이 agent 라벨 집합에 존재할 때만 true.
//   - 빈 preview labels 는 vacuously true (모든 agent 매치).
//   - 빈 agent labels 는 preview 가 빈 경우에만 true.
//   - nil 슬라이스는 빈 슬라이스와 동일하게 취급.
func LabelsMatch(preview, agent []string) bool {
	if len(preview) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(agent))
	for _, v := range agent {
		set[v] = struct{}{}
	}
	for _, v := range preview {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}
