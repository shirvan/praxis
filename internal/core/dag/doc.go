// Package dag implements Praxis' pure dependency graph engine.
//
// The DAG engine is a foundational piece of the Praxis orchestrator. It sits
// between the template renderer (which produces concrete resource specs) and the
// Restate-based orchestrator (which dispatches driver calls). Its purpose is to:
//
//  1. Parse rendered resource specs to discover inter-resource dependencies via
//     ${resources.<name>.outputs.*} expressions embedded in JSON values.
//  2. Construct a validated directed acyclic graph (DAG) from those dependencies.
//  3. Provide deterministic topological orderings and scheduling queries so the
//     orchestrator can dispatch resources in the correct order, executing
//     independent resources in parallel (eager dispatch).
//
// The package is intentionally small, deterministic, and free of runtime
// dependencies on Restate, AWS, or driver implementations. Its job is to take
// rendered resource documents, discover resource-to-resource references in
// dispatch-time output expressions, validate the resulting dependency graph, and
// answer the scheduling questions the orchestrator will ask later.
//
// # Architecture
//
// The package is split into three concerns:
//
//   - parser.go — Expression reference extraction. Walks JSON-decoded resource
//     specs, finds ${...} placeholders, extracts resource output references via
//     regex, and records the JSON path of each dispatch-time expression. This is
//     how dependency edges are discovered from declarative resource definitions.
//
//   - graph.go — DAG construction and validation. Builds the adjacency list
//     representation from parsed dependencies, enforces uniqueness and
//     referential integrity, deduplicates/sorts all edge lists for determinism,
//     and runs DFS-based cycle detection to guarantee the graph is a valid DAG.
//     Also provides Kahn's algorithm-based topological sort and subgraph
//     extraction for targeted deployments.
//
//   - scheduler.go — Runtime scheduling queries. Given a validated graph and the
//     current execution state (completed/dispatched sets), answers "which
//     resources are ready now?" for eager parallel dispatch, and "which resources
//     are transitively affected?" for failure propagation.
//
// # Determinism Guarantee
//
// The implementation favors explicit comments and deterministic output so both
// humans and AI agents can reason about behavior directly from the code and the
// tests without reverse-engineering implicit assumptions. All adjacency lists
// are sorted alphabetically, topological orderings break ties alphabetically,
// and map iterations always proceed over sorted key slices.
//
// # Data Flow
//
// The typical flow through this package during a deployment:
//
//	Template Renderer
//	    → ParseDependencies (per resource, extracts deps + expression paths)
//	    → NewGraph (validates all resources + deps, builds adjacency lists)
//	    → NewSchedule (wraps graph for runtime queries)
//	    → Ready / AffectedByFailure (called repeatedly by orchestrator)
package dag
