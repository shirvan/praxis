// Package dag implements Praxis' pure dependency graph engine.
//
// The package is intentionally small, deterministic, and free of runtime
// dependencies on Restate, AWS, or driver implementations. Its job is to take
// rendered resource documents, discover resource-to-resource references in
// dispatch-time CEL expressions, validate the resulting dependency graph, and
// answer the scheduling questions the orchestrator will ask later.
//
// The package is split into three concerns:
//
//   - parser.go discovers dependency edges and records where each dispatch-time
//     CEL expression lives in a JSON document.
//   - graph.go validates the dependency graph, rejects malformed inputs, and
//     computes stable topological orderings.
//   - scheduler.go answers runtime questions such as which resources are ready
//     to start and which resources must be skipped after a failure.
//
// The implementation favors explicit comments and deterministic output so both
// humans and AI agents can reason about behavior directly from the code and the
// tests without reverse-engineering implicit assumptions.
package dag
