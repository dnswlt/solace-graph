package graph

import (
	"sort"

	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/spring"
)

// Application represents a set of bindings discovered in one or more files.
// Its GAV (Maven groupId/artifactId/version) identifies the application when
// matching against other data.
type Application struct {
	GAV      maven.GAV              `json:"gav"`
	Files    []string               `json:"files"`    // all source files for this application
	Bindings []spring.StreamBinding `json:"bindings"` // all bindings
}

// Sort sorts both the source files and the bindings of the application for deterministic output.
func (a *Application) Sort() {
	sort.Strings(a.Files)
	sort.Slice(a.Bindings, func(i, j int) bool {
		return a.Bindings[i].BindingName < a.Bindings[j].BindingName
	})
}

// BindingMatch describes a specific link between the current application and another one.
type BindingMatch struct {
	Direction string               `json:"direction"` // "from" if remote produces, "to" if local produces
	Local     spring.StreamBinding `json:"local"`     // binding of the node's application
	Remote    spring.StreamBinding `json:"remote"`    // binding of the other application
}

// Edge represents a dependency on another application, including the reason (matching bindings).
type Edge struct {
	To        string         `json:"to"`
	Direction string         `json:"direction"` // "from", "to", or "both"
	Matches   []BindingMatch `json:"matches"`
}

// Node represents an application in the dependency graph and its outgoing edges.
type Node struct {
	App   Application `json:"app"`
	Edges []Edge      `json:"edges"`
}

type preparedBinding struct {
	binding spring.StreamBinding
	syntax  spring.TopicSyntax
	levels  []string
}

type preparedApp struct {
	app Application
	in  []preparedBinding
	out []preparedBinding
}

// Build constructs the dependency graph from a list of discovered applications.
// A dependency (edge) exists from application A to B if A has an input binding whose
// topic matches an output binding of B on the same binder technology (Solace, Kafka,
// TIBCO RV, ...); bindings of different technologies never match.
// It assumes that the input list contains unique applications by GAV.
func Build(apps []Application) []Node {
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].GAV.ArtifactId < apps[j].GAV.ArtifactId
	})

	prepared := make([]preparedApp, len(apps))
	for i, app := range apps {
		pa := preparedApp{app: app}
		for _, b := range app.Bindings {
			syntax := spring.TopicSyntaxFor(b)
			if syntax == spring.SyntaxUnknown {
				continue
			}
			levels := spring.TopicLevels(b.Destination, syntax)
			if levels == nil {
				continue // reply-topic variable or fully-unresolved destination
			}
			pb := preparedBinding{
				binding: b,
				syntax:  syntax,
				levels:  levels,
			}
			switch b.Direction {
			case spring.BindingIn:
				pa.in = append(pa.in, pb)
			case spring.BindingOut:
				pa.out = append(pa.out, pb)
			}
		}
		prepared[i] = pa
	}

	var nodes []Node
	for _, pa := range prepared {
		edgeMap := make(map[string]*Edge)

		for _, other := range prepared {
			if pa.app.GAV == other.app.GAV {
				continue
			}

			var fromEdge, toEdge *Edge

			// Current app consumes FROM other app (pa.in matches other.out)
			for _, inB := range pa.in {
				for _, outB := range other.out {
					if inB.syntax == outB.syntax && spring.MatchLevels(inB.levels, outB.levels) {
						if fromEdge == nil {
							fromEdge = &Edge{To: other.app.GAV.ArtifactId, Direction: "from"}
						}
						fromEdge.Matches = append(fromEdge.Matches, BindingMatch{Direction: "from", Local: inB.binding, Remote: outB.binding})
					}
				}
			}

			// Current app produces TO other app (pa.out matches other.in)
			for _, outB := range pa.out {
				for _, inB := range other.in {
					if inB.syntax == outB.syntax && spring.MatchLevels(inB.levels, outB.levels) {
						if toEdge == nil {
							toEdge = &Edge{To: other.app.GAV.ArtifactId, Direction: "to"}
						}
						toEdge.Matches = append(toEdge.Matches, BindingMatch{Direction: "to", Local: outB.binding, Remote: inB.binding})
					}
				}
			}

			switch {
			case fromEdge != nil && toEdge != nil:
				fromEdge.Direction = "both"
				fromEdge.Matches = append(fromEdge.Matches, toEdge.Matches...)
				edgeMap[other.app.GAV.ArtifactId] = fromEdge
			case fromEdge != nil:
				edgeMap[other.app.GAV.ArtifactId] = fromEdge
			case toEdge != nil:
				edgeMap[other.app.GAV.ArtifactId] = toEdge
			}
		}

		var edges []Edge
		for _, e := range edgeMap {
			// Sort matches for deterministic output
			sort.Slice(e.Matches, func(i, j int) bool {
				if e.Matches[i].Local.BindingName != e.Matches[j].Local.BindingName {
					return e.Matches[i].Local.BindingName < e.Matches[j].Local.BindingName
				}
				return e.Matches[i].Remote.BindingName < e.Matches[j].Remote.BindingName
			})
			edges = append(edges, *e)
		}
		sort.Slice(edges, func(i, j int) bool {
			return edges[i].To < edges[j].To
		})

		nodes = append(nodes, Node{
			App:   pa.app,
			Edges: edges,
		})
	}

	return nodes
}
