# Application dependency graph (sgraph)

`sgraph` statically analyses Spring Cloud Stream applications to discover the messaging
dependencies between them. It reads each application's configuration, matches producer
bindings to consumer bindings by topic, and reports the resulting dependency graph.

Despite the repo name, `sgraph` is **not Solace-specific**. It understands several binder
technologies and only matches bindings **within the same technology**:

- **Solace** — `/`-separated topics with `*` (single level) and `>` (multi level) wildcards.
- **Kafka** and **TIBCO RV (tibrv)** — `.`-separated topics, no wildcards.

## Usage

The tool works in two steps: **collect** once (slow, reads the filesystem) into JSON, then
run **report** or **swcat** as often as needed on that JSON. In `report` and `swcat`,
`<file>` is always output from `collect`.

### 1. Collect bindings

`collect` recursively scans the given roots for Maven modules (`pom.xml`) and extracts
their bindings into JSON.

```bash
sgraph collect [-exclude-profile <regex>]... [-exclude-app <regex>]... <root> [<root>...] > bindings.json
```

- `-exclude-profile <regex>`: skip `application-<profile>.yml` files whose profile suffix
  matches (e.g. `dev|test`). Repeatable. Base `application.yml` is never excluded.
  **Recommended:** exclude developer and test profiles, e.g.
  `-exclude-profile 'local|dev|test'`. All remaining profile files are merged, and when
  several define the same property the file read first (by name) wins — so without the
  exclusion, a hard-coded value in `application-local.yml` can shadow the real
  (placeholder-based) destination in, say, `application-solace.yml`.
- `-exclude-app <regex>`: skip applications whose artifactId matches. Repeatable.
- `<root>`: one or more directories to scan recursively for `pom.xml` modules.

### 2. Build dependency report

`report` processes the collected bindings into a dependency graph and renders a
self-contained, interactive HTML report.

```bash
sgraph report [-html <report.html>] <file> [<file>...]
```

- `-html`: path to write the HTML report (default `sgraph.html`).

### 3. Report to swcat

`swcat` matches the collected applications against the
[swcat](https://github.com/dnswlt/swcat) service catalog by Maven coordinates and reports,
per matched component, its observed endpoints: each topic it publishes to or subscribes
to, with the applications on the other end as counterparts (catalog components, or Maven
coordinates for modules not in the catalog). swcat shows them as a table on the
component's page and lints the dependencies they imply against the catalog.

```bash
sgraph swcat [-url <swcat-url>] [-dry-run | -delete] <file> [<file>...]
```

- `-url`: base URL of the swcat server (default `http://localhost:9191`).
- `-dry-run`: don't upload; just print what would be sent. By default `swcat`
  **uploads** the observations.
- `-delete`: delete all observations this tool previously reported, then exit
  (no input files needed).

It prints a one-line match summary and then reports only what went wrong, naming the
entities involved: catalog components with no resolvable Maven groupId (they can never
match), collected applications missing from the catalog, dependencies on or of those
missing applications (reported to swcat only as external counterparts), and any messages
swcat rejected. The
mappings that *did* work are listed only under `-dry-run`.

## Example workflow

```bash
# Collect bindings from two repositories.
sgraph collect ~/git/service-a ~/git/service-b > all_bindings.json

# Render an interactive HTML dependency report.
sgraph report -html report.html all_bindings.json

# Preview what would be reported to the catalog, then upload it.
sgraph swcat -url http://localhost:9191 -dry-run all_bindings.json
sgraph swcat -url http://localhost:9191 all_bindings.json
```

Pass `-v` before the command for debug logging, e.g. `sgraph -v collect ...`.

## How it works

1. **Module discovery**: `collect` walks the roots looking for `pom.xml` files. Each Maven
   module maps to at most one application, identified by its full Maven **GAV**
   (`groupId:artifactId:version`). Modules without a `src/main/resources` (e.g. aggregator
   or library modules) contribute nothing.
2. **Context reading**: for each module it flattens all `application*.yml` files (YAML
   only; `.properties` is not supported), follows `spring.config.import` directives, and
   resolves `${...}` placeholders with Spring's relaxed binding rules. This is a heuristic
   reconstruction of the Spring context, not a real Spring runtime.
   - `classpath:` imports are resolved against a **simulated classpath**: the module's own
     resources plus the resources of the modules it (transitively) depends on, per the
     `<dependencies>` in its `pom.xml`.
   - All profile-specific files are read, as the union of every profile, minus those
     filtered by name with `-exclude-profile`. Profile *activation* (e.g.
     `spring.profiles.active`, profile groups, `spring.config.activate.on-profile`) is
     **not** simulated: in practice the active profiles are often chosen outside the code
     (deployment environment variables, process modes), so simulating activation from the
     repository alone drops real bindings.
   - Unresolved `${VAR}` placeholders are kept as-is (defaults like `${VAR:x}` are **not**
     substituted), so matching stays environment-agnostic.
3. **Binding matching**:
   - Each binding's topic technology (Solace / Kafka / tibrv) is derived from its binder
     type; bindings of different technologies never match.
   - Topics are split into levels (`/` for Solace, `.` for Kafka/tibrv) and compared.
     Solace wildcards `*` and `>` are honoured.
   - Unresolved property placeholders act as a single-level wildcard, so a producer and
     consumer that differ only by an environment-specific segment still match.
   - Request/reply reply-topics (`${replyTopicWithWildcards|...}`) are dropped.
4. **Request/reply**: the request topic is what links requester and replier (the reply
   topic is generated per request). A requester is recognised by a
   `spring.cloud.stream.requestreply.bindingMapping` entry naming its binding, or by an
   input binding subscribed to a `${replyTopicWithWildcards|...}` reply topic; its output
   binding is marked as a request binding. Requests sent to runtime-chosen destinations
   have no static `destination`; for those, the mapping's `topicPatterns` are used as
   request topics. Patterns using regex syntax (e.g. `.*foo/request`) are skipped; write
   them as plain topics (placeholders allowed) to have them matched.
5. **Graph construction**: one node per application, with directed edges representing
   dependencies. A consumer depends on the producer of a topic, but a requester depends on
   the replier, i.e. the consumer of its request topic. `swcat` reports each endpoint with
   its direction and kind (event or request), from which swcat derives who depends on
   whom.
