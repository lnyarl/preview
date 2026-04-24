// Package store is the portability boundary between business logic and the
// underlying database driver.
//
// Phase 0 intentionally declares no types here. The AgentStore and
// PreviewStore interfaces are introduced in Phase 1 together with real
// method signatures; an empty `interface{}` would alias `any` and
// defeat the boundary, so it is deliberately omitted.
//
// Rules for future phases:
//   - internal/hub and internal/agent MUST depend only on interfaces
//     declared in this package.
//   - internal/db/sqlite (sqlc output + store adapter) MUST NOT be
//     imported directly from internal/hub, internal/agent, or cmd/*.
//   - When PostgreSQL support is added, a sibling package
//     internal/db/postgres will implement the same interfaces and the
//     business logic will remain untouched.
package store
