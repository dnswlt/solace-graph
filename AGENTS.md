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

- **Request/reply is tracked via the request topic, with reversed dependency.** A
  requester's output binding is marked `StreamBinding.Request`
  (`spring.requestReplyConfig`: named in a `requestreply.bindingMapping`, or its input
  binding subscribes to a reply topic). In `graph.Build`, a match whose producing side is a
  request binding means the *producer* depends on the consumer (the replier):
  `BindingMatch.Kind == KindRequest`, and `DirFrom` always means "local depends on remote",
  never just "remote produces". Literal `topicPatterns` become extra request bindings
  (requests to runtime-chosen destinations have no static `destination`). Regex
  `topicPatterns` are deliberately skipped: matching regexes against topics would
  complicate matching for a handful of cases that can be written as plain topics instead.
  Forwarding proxies (consume one request topic, republish to another) can't be recognised
  from config and stay plain pub/sub — accepted.

- **Lists of maps from several files are appended** (`flattenValue` /
  `listIndexOccupied`): a map item only creates `prefix[i].field` keys, so the "next free
  index" check must look for nested keys too, or later files' entries (e.g.
  `bindingMapping` spread over profile files) collide and are dropped by first-wins.

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
                       and ObservedEndpoints reporting
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
apps and, per matched source Component, emits an `ObservedEndpoints` message: one `Endpoint`
per distinct (direction, kind, address) of its bindings, with the apps it was matched with as
counterparts — in **both** directions, so each link appears in both components' reports and
swcat needs no inverse lookup. swcat derives who depends on whom from direction and kind
(inbound event or outbound request: the source depends on the counterpart). An inbound
binding is a request (reply-serving) endpoint if a request binding matched it, since the
replier's own config doesn't say so. Endpoints without counterparts are reported too.
Reporting is a **full idempotent sync**: sources with no bindings still emit an empty
message, clearing previously observed endpoints. Uploads by default; `-dry-run` only prints
what would be sent.

Unmatched apps can never be a source, but they are in the graph so that they show up as
`external` counterparts (by Maven coordinates) instead of vanishing. The dependencies
involving them are additionally returned as `swcat.DroppedDependency`, for the stdout list of
what the catalog is missing. Both orientations count — a missing *consumer* loses the
dependency just as surely as a missing producer — and each is found once, from whichever end
is matched.
Likewise `MatchResult.Unresolved` keeps the Components whose groupId could not be resolved.

Several Components may legitimately resolve to the same coordinates (e.g. two entities for
two live versions of the same binary). An application can only be reported as one Component,
so the last match wins and the earlier ones are flagged `Match.Shadowed` — reported to the
user, not silently dropped. Keep the pick where it is: `MatchComponents` decides, and
`ObservedEndpoints` skips shadowed matches explicitly rather than relying on map-overwrite
order.

The command's stdout is a one-line match summary followed by sections for **what went wrong**
only (unresolvable components, shadowed duplicates, modules missing from the catalog,
dependencies involving those modules, rejected posts) — the successful mappings are printed only under
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
  (`Scan`/classpath), `spring` (placeholder resolution, imports, topic matching,
  request/reply detection), `graph` (edge directions) and `swcat` (reported relations); add
  to those when changing the heuristics.
