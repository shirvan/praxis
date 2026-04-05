// template.go — no more `praxis template` command group.
//
// Template operations are now accessed through top-level verbs:
//   - `praxis create template <file.cue>`  (create.go)
//   - `praxis list templates`              (list.go)
//   - `praxis get template/<name>`         (get.go)
//   - `praxis delete template/<name>`      (delete.go)
//   - `praxis deploy <name> --var ...`     (deploy.go)
//
// This file is intentionally empty after the verb-first migration.
package cli
