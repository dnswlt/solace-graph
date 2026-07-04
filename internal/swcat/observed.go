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

// ObservedDependencies runs the dependency analysis over the matched
// applications and converts, for each matched source Component, its
// dependencies on other matched Components into an ObservedDependencies
// message.
//
// One message is returned per matched source Component, including sources with
// no dependencies: reporting an empty list clears any dependencies previously
// observed by this tool, so the result is a full, idempotent sync.
func ObservedDependencies(res MatchResult) []*catalogpb.ObservedDependencies {
	apps := make([]graph.Application, 0, len(res.Matches))
	compByGAV := make(map[maven.GAV]*catalogpb.Entity, len(res.Matches))
	for _, m := range res.Matches {
		if m.App == nil {
			continue
		}
		apps = append(apps, *m.App)
		compByGAV[m.App.GAV] = m.Component
	}

	nodes := graph.Build(apps)

	result := make([]*catalogpb.ObservedDependencies, 0, len(nodes))
	for _, node := range nodes {
		src, ok := compByGAV[node.App.GAV]
		if !ok {
			continue // not a matched component (shouldn't happen: Build ran over matched apps)
		}
		result = append(result, &catalogpb.ObservedDependencies{
			Source:       componentRef(src),
			DetectedBy:   DetectedBy,
			Dependencies: dependencies(node.Edges, compByGAV),
		})
	}
	return result
}

// dependencies converts a source node's edges into ObservedDependency entries,
// keeping only neighbors that are themselves matched Components.
//
// Only the "consumes" direction is reported. graph.Build produces symmetric
// edges: whenever A consumes from B, that same relationship surfaces as a "from"
// match on A's node and a "to" match on B's node. Reporting the consuming side
// only records each message-flow dependency exactly once, attributed to the
// component that depends on the other (the consumer needs the producer's
// messages). The producing side is captured as the consumer's dependency.
func dependencies(edges []graph.Edge, compByGAV map[maven.GAV]*catalogpb.Entity) []*catalogpb.ObservedDependency {
	var deps []*catalogpb.ObservedDependency
	for _, e := range edges {
		target, ok := compByGAV[e.To]
		if !ok {
			continue
		}

		// Evidence is the source's own binding destination (Local): the topic it
		// subscribes to. The remote destination is the same channel and would be
		// redundant.
		topics := make(map[string]bool)
		for _, m := range e.Matches {
			if m.Direction == dirFrom {
				topics[m.Local.Destination] = true
			}
		}
		if ev := sortedKeys(topics); len(ev) > 0 {
			deps = append(deps, &catalogpb.ObservedDependency{
				Target:   componentRef(target),
				Relation: catalogpb.DependencyRelation_DEPENDENCY_RELATION_CONSUMES,
				Evidence: ev,
			})
		}
	}
	return deps
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
