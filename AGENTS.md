# AGENTS.md

Guidance for agents (and humans) working in this repo. The [README](README.md) covers what
the tool does, the CLI, and the matching rules; this file covers the *why* behind the
design and the code layout. Read both before changing behavior.

## Design decisions (the "why")

Deliberate, non-obvious intent — easy to get wrong without context. Each notes where it
lives in the code.

- **Maven modules are the unit of collection.** `collect` scans the roots for `pom.xml`
  (`maven.Scan`), *not* for `application*.yml`. Each module maps to at most one
  application, keyed by its full **GAV**. Aggregator/library modules (no
  `src/main/resources`) contribute nothing.

- **The classpath is simulated from declared Maven dependencies.** A `classpath:`
  `spring.config.import` is resolved only against the importing module's own resources plus
  its (transitive) dependency modules' resources (`Modules.Classpath` / `ResolveResource`)
  — a heuristic reconstruction: no Maven invocation, no external JARs. Modules outside the
  scanned set are simply absent from the classpath.

- **Context instantiation is heuristic, not a real Spring runtime**
  (`spring.ReadApplicationProperties`): flatten a module's `application*.yml`, follow
  `spring.config.import`, resolve `${...}` with relaxed binding
  (case/dash/underscore-insensitive). YAML only.

- **Profiles are filtered, not simulated.** `-exclude-profile` drops files by name; every
  other profile file is merged (the union of all profiles). Profile activation
  (`spring.profiles.active`, `include`, groups, `on-profile` documents) is deliberately
  not modelled. This was evaluated against a large real-world multi-repo code base: an
  activation model lost a substantial share of real bindings and gained nothing over
  "union + exclude `local|dev|test`". The losses came from:
  - profiles activated outside the code (deployment env vars, per-process modes such as
    `on-profile: export`), so the repo alone declares no or only partial activation;
  - library modules whose profile files are loaded by the deploying application through
    custom mechanisms (`EnvironmentPostProcessor` with `classpath*:` patterns, or
    `spring.config.import` from another module) rather than through Spring profiles;
  - profile names containing placeholders (`kafka-${KAFKA_PROFILE}`), which are
    ambiguous either way.

  Making the merge order Spring-accurate (imports override their importer, last document
  wins) was evaluated too and changed no bindings, so it hasn't been done. Don't add
  either without a concrete case where it changes the graph. The real risk of the union is
  developer profiles shadowing real values (first file by name wins), which
  `-exclude-profile` addresses.

- **Unresolved `${VAR}` is kept; defaults are not substituted** — a compile-time
  `${VAR:default}` rarely matches the deployed value. Kept placeholders become single-level
  wildcards during matching, which is what makes matching environment-agnostic.

- **Reply-topics are dropped at collection** (`spring.StreamBindings` skips
  `${replyTopicWithWildcards|...}`): per-request runtime queues, never real topics.

- **Binder technology drives matching** (`spring.TopicSyntaxFor`): read from
  `spring.cloud.stream.binders.<name>.type`, falling back to a structural guess from the
  destination when unknown. Bindings of different technologies never match.

- **Duplicate GAVs: first wins, with a warning.** Overlapping input roots are de-duped by
  absolute `pom.xml` path in `maven.Scan`. If two genuinely different modules declare the
  same GAV, `collect` keeps the first and logs a `slog.Warn` rather than merging (there is
  no `Merge` — fusing distinct services into one node would mislead).

## Code layout

```text
cmd/sgraph/            CLI entrypoint (main.go: collect|report|swcat) + log handler
internal/maven/        pom.xml parsing (GAV, dependencies) and module/classpath scanning
internal/spring/       app-context reading, placeholder resolution, binding extraction,
                       topic syntax + matching (the core heuristics live here)
internal/graph/        Application model + Build(): producer/consumer edge construction
internal/report/       self-contained interactive HTML report (report -html)
internal/swcat/        swcat catalog HTTP client, Component<->Application matching,
                       and ObservedDependencies reporting
proto/swcat/...        vendored .proto (see below); generated via `make proto`
internal/catalog/pb/   generated protobuf types for the swcat catalog API
```

The on-disk `collect`→consume contract is JSON `[]graph.Application`.

Dependency direction: `graph` and `swcat` depend on `spring` and `maven`; `spring` and
`maven` don't depend on each other (they're wired together in `commands`). Keep it that way
— `spring` stays a pure app-context parser, taking its import resolution as an injected
`spring.ImportResolver` func so it never imports `maven`.

### swcat

`swcat` fetches catalog `Component` entities, resolves each to Maven coordinates
(`match.go`: an explicit `maven.apache.org/coords` annotation, or a `groupId` annotation
inherited Component → System → Domain, with the entity name as artifactId), and matches
them to collected applications by GAV. It runs the same `graph.Build` over *all* collected
apps and, per matched source Component, emits an `ObservedDependencies` message covering only
the `"from"` (this-component-consumes) edges. Reporting is a **full idempotent sync**: sources
with no dependencies still emit an empty message, clearing previously observed deps. Uploads
by default; `-dry-run` only prints what would be sent.

Unmatched apps are in the graph purely for diagnostics: they can never be a source, but an
edge touching one is returned as a `swcat.DroppedDependency` rather than vanishing. Both
orientations count — a missing *consumer* loses the dependency just as surely as a missing
producer, since only matched Components are sources — so `dependencies` inspects the `"to"`
matches too, and each dropped dependency is found once, from whichever end is matched.
Likewise `MatchResult.Unresolved` keeps the Components whose groupId could not be resolved.

Several Components may legitimately resolve to the same coordinates (e.g. two entities for
two live versions of the same binary). An application can only be reported as one Component,
so the last match wins and the earlier ones are flagged `Match.Shadowed` — reported to the
user, not silently dropped. Keep the pick where it is: `MatchComponents` decides, and
`ObservedDependencies` skips shadowed matches explicitly rather than relying on map-overwrite
order.

The command's stdout is a one-line match summary followed by sections for **what went wrong**
only (unresolvable components, shadowed duplicates, modules missing from the catalog, dropped
dependencies, rejected posts) — the successful mappings are printed only under
`-dry-run`. Keep it that way: on a real catalog the successes are the bulk and the problems
are what the user acts on.

**`proto/swcat/catalog/v1/catalog.proto` is a verbatim copy from the
[swcat](https://github.com/dnswlt/swcat) repo, which owns it.** Do **not** edit it (or the
generated `internal/catalog/pb/catalog.pb.go`) here — changes must be made upstream in
swcat and the file re-copied, otherwise the two definitions drift and protojson
(de)serialization against the swcat API breaks. `make proto` regenerates the Go types from
the local copy; run it only after syncing a new copy from swcat.

## Conventions

- Go 1.25. Standard library + `gopkg.in/yaml.v3` + `google.golang.org/protobuf`.
- Logging via `log/slog` to stderr; JSON results to stdout. `-v` enables debug logging.
- Run `gofmt`, `go vet`, and `go test ./...` before finishing. Tests live in `maven`
  (`Scan`/classpath) and `spring` (placeholder resolution, imports, topic matching); add to
  those when changing the heuristics.
