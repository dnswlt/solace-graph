package swcat

import (
	"sort"
	"strings"

	catalogpb "github.com/dnswlt/solace-graph/internal/catalog/pb"
	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/maven"
)

// DetectedBy is the tool label recorded on every observation we report.
const DetectedBy = "solace-graph"

// DroppedDependency is a dependency that was detected but cannot be reported,
// because one of its two ends has no catalog Component: either there is no
// entity to attribute the observation to, or none to point it at.
//
// For request/reply, the requester is the Consumer (it depends on the replier)
// and the replier the Producer.
type DroppedDependency struct {
	Consumer maven.GAV // the depending application: subscriber, or requester
	Producer maven.GAV // the depended-on application: publisher, or replier
	Topics   []string  // the topics the dependency was detected on
	// Missing is the end without a catalog Component, always exactly one of
	// Consumer or Producer (a dependency between two uncatalogued
	// applications is not detected at all: neither end is a source).
	Missing maven.GAV
}

// ObservedDependencies runs the dependency analysis over the collected
// applications and converts, for each matched source Component, its
// dependencies on other matched Components into an ObservedDependencies
// message.
//
// One message is returned per matched source Component, including sources with
// no dependencies: reporting an empty list clears any dependencies previously
// observed by this tool, so the result is a full, idempotent sync.
//
// The graph is built over the unmatched applications as well, even though they
// can never be a source. That way a dependency on an application that is
// missing from the catalog is returned as a DroppedDependency instead of
// silently disappearing from the report.
func ObservedDependencies(res MatchResult) ([]*catalogpb.ObservedDependencies, []DroppedDependency) {
	apps := make([]graph.Application, 0, len(res.Matches)+len(res.UnmatchedApps))
	compByGAV := make(map[maven.GAV]*catalogpb.Entity, len(res.Matches))
	seen := make(map[maven.GAV]bool, cap(apps))
	addApp := func(app *graph.Application) {
		if seen[app.GAV] {
			return // graph.Build requires apps to be unique by GAV
		}
		seen[app.GAV] = true
		apps = append(apps, *app)
	}
	for _, m := range res.Matches {
		if m.App == nil || m.Shadowed {
			continue
		}
		addApp(m.App)
		compByGAV[m.App.GAV] = m.Component
	}
	for _, app := range res.UnmatchedApps {
		addApp(app)
	}

	nodes := graph.Build(apps)

	msgs := make([]*catalogpb.ObservedDependencies, 0, len(nodes))
	var dropped []DroppedDependency
	for _, node := range nodes {
		src, ok := compByGAV[node.App.GAV]
		if !ok {
			continue // an unmatched application: no Component to report it for
		}
		deps, drops := dependencies(node.App.GAV, node.Edges, compByGAV)
		msgs = append(msgs, &catalogpb.ObservedDependencies{
			Source:       componentRef(src),
			DetectedBy:   DetectedBy,
			Dependencies: deps,
		})
		dropped = append(dropped, drops...)
	}
	return msgs, dropped
}

// dependencies converts a source node's edges into ObservedDependency entries.
// Edges whose neighbor is not a matched Component are returned as
// DroppedDependency instead, so the caller can report what could not be synced.
//
// Only the depending direction ("from" matches) is reported. graph.Build produces
// symmetric edges: whenever A depends on B, that same relationship surfaces as a
// "from" match on A's node and a "to" match on B's node. Reporting the "from" side
// only records each dependency exactly once, attributed to the component that
// depends on the other: a consumer needs the producer's messages (CONSUMES), a
// request/reply requester needs the replier's answers (CALLS).
//
// The "to" matches are still examined, for their own dependency is lost just as
// easily: if the neighbor depending on local has no Component, no node will ever
// report that dependency, since only matched Components are sources. Each dropped
// dependency is found exactly once, from whichever of its two ends is a matched
// Component.
func dependencies(local maven.GAV, edges []graph.Edge, compByGAV map[maven.GAV]*catalogpb.Entity) ([]*catalogpb.ObservedDependency, []DroppedDependency) {
	var deps []*catalogpb.ObservedDependency
	var dropped []DroppedDependency
	for _, e := range edges {
		// Evidence is the destination of the depending side's own binding: the
		// topic a consumer subscribes to, or a requester sends its requests to.
		// The other side names the same channel and would be redundant. Local is
		// the depending side of a "from" match, the neighbor of a "to" match.
		consumes := make(map[string]bool)   // local consumes from the neighbor
		calls := make(map[string]bool)      // local sends requests to the neighbor
		dependents := make(map[string]bool) // the neighbor depends on local
		for _, m := range e.Matches {
			switch {
			case m.Direction != graph.DirFrom:
				dependents[m.Remote.Destination] = true
			case m.Kind == graph.KindRequest:
				calls[m.Local.Destination] = true
			default:
				consumes[m.Local.Destination] = true
			}
		}

		neighbor, matched := compByGAV[e.To]
		if !matched {
			if ev := sortedKeys(consumes, calls); len(ev) > 0 {
				dropped = append(dropped, DroppedDependency{
					Consumer: local, Producer: e.To, Topics: ev, Missing: e.To,
				})
			}
			// A matched neighbor would report this itself, from its own node.
			if ev := sortedKeys(dependents); len(ev) > 0 {
				dropped = append(dropped, DroppedDependency{
					Consumer: e.To, Producer: local, Topics: ev, Missing: e.To,
				})
			}
			continue
		}
		for _, r := range []struct {
			relation catalogpb.DependencyRelation
			topics   map[string]bool
		}{
			{catalogpb.DependencyRelation_DEPENDENCY_RELATION_CONSUMES, consumes},
			{catalogpb.DependencyRelation_DEPENDENCY_RELATION_CALLS, calls},
		} {
			if ev := sortedKeys(r.topics); len(ev) > 0 {
				deps = append(deps, &catalogpb.ObservedDependency{
					Target:   componentRef(neighbor),
					Relation: r.relation,
					Evidence: ev,
				})
			}
		}
	}
	return deps, dropped
}

// sortedKeys returns the distinct non-empty keys of the given sets, sorted for
// deterministic output.
func sortedKeys(sets ...map[string]bool) []string {
	union := make(map[string]bool)
	for _, set := range sets {
		for k := range set {
			if k != "" {
				union[k] = true
			}
		}
	}
	if len(union) == 0 {
		return nil
	}
	out := make([]string, 0, len(union))
	for k := range union {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Summary renders the dependencies of one ObservedDependencies message as a
// compact "relation:target" list, e.g. "consumes:a, produces:b".
func Summary(od *catalogpb.ObservedDependencies) string {
	parts := make([]string, 0, len(od.GetDependencies()))
	for _, d := range od.GetDependencies() {
		parts = append(parts, relationLabel(d.GetRelation())+":"+d.GetTarget().GetName())
	}
	return strings.Join(parts, ", ")
}

func relationLabel(r catalogpb.DependencyRelation) string {
	switch r {
	case catalogpb.DependencyRelation_DEPENDENCY_RELATION_CONSUMES:
		return "consumes"
	case catalogpb.DependencyRelation_DEPENDENCY_RELATION_PRODUCES:
		return "produces"
	case catalogpb.DependencyRelation_DEPENDENCY_RELATION_CALLS:
		return "calls"
	default:
		return "depends-on"
	}
}

func componentRef(e *catalogpb.Entity) *catalogpb.Ref {
	md := e.GetMetadata()
	return &catalogpb.Ref{
		Kind:      KindComponent,
		Namespace: md.GetNamespace(),
		Name:      md.GetName(),
	}
}
