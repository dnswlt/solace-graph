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
[swcat](https://github.com/dnswlt/swcat) service catalog by Maven coordinates and reports
the observed dependencies between matched components.

```bash
sgraph swcat [-url <swcat-url>] [-post] <file> [<file>...]
```

- `-url`: base URL of the swcat server (default `http://localhost:9191`).
- `-post`: actually upload the observations. Without it, `swcat` is a **dry run** that
  prints what it would send.

## Example workflow

```bash
# Collect bindings from two repositories.
sgraph collect ~/git/service-a ~/git/service-b > all_bindings.json

# Render an interactive HTML dependency report.
sgraph report -html report.html all_bindings.json

# Preview what would be reported to the catalog, then upload it.
sgraph swcat -url http://localhost:9191 all_bindings.json
sgraph swcat -url http://localhost:9191 -post all_bindings.json
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
   - `-exclude-profile` filters profile-specific files by name. Profile *activation* (e.g.
     `spring.profiles.active`, profile groups) is **not** simulated.
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
4. **Graph construction**: one node per application, with directed edges representing
   dependencies (producing to / consuming from another application).
