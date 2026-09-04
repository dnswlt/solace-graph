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

// dirFrom is the graph.Build edge-match direction meaning the source consumes
// from the neighbor. We report dependencies from this side only (see dependencies).
const dirFrom = "from"

// DroppedDependency is a dependency that was detected but cannot be reported:
// the source Component consumes from an application that has no catalog
// Component, so there is no target to point an observation at.
type DroppedDependency struct {
	Source *catalogpb.Entity // the consuming Component
	Target maven.GAV         // the producing application, absent from the catalog
	Topics []string          // the topics Source consumes from it
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
		if m.App == nil {
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
		deps, drops := dependencies(src, node.Edges, compByGAV)
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
// Edges whose neighbor is not itself a matched Component are returned as
// DroppedDependency instead, so the caller can report what could not be synced.
//
// Only the "consumes" direction is reported. graph.Build produces symmetric
// edges: whenever A consumes from B, that same relationship surfaces as a "from"
// match on A's node and a "to" match on B's node. Reporting the consuming side
// only records each message-flow dependency exactly once, attributed to the
// component that depends on the other (the consumer needs the producer's
// messages). The producing side is captured as the consumer's dependency.
func dependencies(src *catalogpb.Entity, edges []graph.Edge, compByGAV map[maven.GAV]*catalogpb.Entity) ([]*catalogpb.ObservedDependency, []DroppedDependency) {
	var deps []*catalogpb.ObservedDependency
	var dropped []DroppedDependency
	for _, e := range edges {
		// Evidence is the source's own binding destination (Local): the topic it
		// subscribes to. The remote destination is the same channel and would be
		// redundant.
		topics := make(map[string]bool)
		for _, m := range e.Matches {
			if m.Direction == dirFrom {
				topics[m.Local.Destination] = true
			}
		}
		ev := sortedKeys(topics)
		if len(ev) == 0 {
			continue // a "to"-only edge: reported as the consumer's dependency
		}

		target, ok := compByGAV[e.To]
		if !ok {
			dropped = append(dropped, DroppedDependency{Source: src, Target: e.To, Topics: ev})
			continue
		}
		deps = append(deps, &catalogpb.ObservedDependency{
			Target:   componentRef(target),
			Relation: catalogpb.DependencyRelation_DEPENDENCY_RELATION_CONSUMES,
			Evidence: ev,
		})
	}
	return deps, dropped
}

// sortedKeys returns the non-empty keys of set, sorted for deterministic output.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		if k != "" {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil
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
