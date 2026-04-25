// 이 파일의 책임:
//   - LabelsMatch: preview 가 요구하는 라벨이 agent 라벨에 모두 (k,v) 일치 여부.
//   - 라벨 매칭은 SQL 이 아닌 Go 메모리에서 수행 (이식성 원칙, 결정 4 / NF-Portability-3).
//
// 본 함수는 Phase 2 Step 1 에서 인터페이스만 도입(Step 2 dispatcher 가 호출).
// 단위 테스트로 결정 4 의 6 case + nil edge 를 커버한다(F-S2-3).
package hub

// LabelsMatch 는 preview 가 요구하는 모든 키-값 쌍이 agent 에 동일하게 존재할 때만 true.
//   - 빈 preview labels 는 vacuously true (모든 agent 매치).
//   - 빈 agent labels 는 preview 가 빈 경우에만 true.
//   - nil map 은 빈 map 과 동일하게 취급.
func LabelsMatch(preview, agent map[string]string) bool {
	for k, v := range preview {
		if av, ok := agent[k]; !ok || av != v {
			return false
		}
	}
	return true
}
