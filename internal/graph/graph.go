package graph

import (
	"sort"
	"strings"

	"github.com/dnswlt/solace-graph/internal/spring"
)

// Application represents a set of bindings discovered in one or more files.
type Application struct {
	Name      string                 `json:"name"`
	Version   string                 `json:"version,omitempty"`
	Discovery string                 `json:"discovery"` // how the name was determined (e.g., "pom.xml" or "folder-name")
	Files     []string               `json:"files"`     // all source files for this application
	Bindings  []spring.StreamBinding `json:"bindings"`  // all bindings
}

// Merge consolidates the data from another application instance into this one.
// It prefers more specific discovery methods (like "pom.xml") and appends unique files and bindings.
func (a *Application) Merge(other *Application) {
	if a.Discovery == "folder-name" && other.Discovery == "pom.xml" {
		a.Discovery = "pom.xml"
		a.Version = other.Version
	}
	a.Files = append(a.Files, other.Files...)
	a.Bindings = append(a.Bindings, other.Bindings...)
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
	Local  spring.StreamBinding `json:"local"`  // binding of the node's application
	Remote spring.StreamBinding `json:"remote"` // binding of the other application
}

// Edge represents a dependency on another application, including the reason (matching bindings).
type Edge struct {
	To        string         `json:"to"`
	Direction string         `json:"direction"` // "from" if we consume from them, "to" if we produce for them
	Matches   []BindingMatch `json:"matches"`
}

// Node represents an application in the dependency graph and its outgoing edges.
type Node struct {
	App   Application `json:"app"`
	Edges []Edge      `json:"edges"`
}

type preparedBinding struct {
	binding spring.StreamBinding
	levels  []string
}

type preparedApp struct {
	app      Application
	in       []preparedBinding
	out      []preparedBinding
	bindings []preparedBinding
}

// Build constructs the dependency graph from a list of discovered applications.
// A dependency (edge) exists from application A to B if A has a Solace input binding
// that matches a Solace output binding of B.
// It assumes that the input list contains unique applications by name.
func Build(apps []Application) []Node {
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Name < apps[j].Name
	})

	prepared := make([]preparedApp, len(apps))
	for i, app := range apps {
		pa := preparedApp{app: app}
		for _, b := range app.Bindings {
			if !isSolace(b) {
				continue
			}
			pb := preparedBinding{
				binding: b,
				levels:  spring.TopicLevels(b.Destination),
			}
			pa.bindings = append(pa.bindings, pb)
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
			if pa.app.Name == other.app.Name {
				continue
			}

			// Current app consumes FROM other app (pa.in matches other.out)
			for _, inB := range pa.in {
				for _, outB := range other.out {
					if spring.MatchLevels(inB.levels, outB.levels) {
						e, ok := edgeMap[other.app.Name]
						if !ok {
							e = &Edge{To: other.app.Name, Direction: "from"}
							edgeMap[other.app.Name] = e
						}
						e.Matches = append(e.Matches, BindingMatch{Local: inB.binding, Remote: outB.binding})
					}
				}
			}

			// Current app produces TO other app (pa.out matches other.in)
			for _, outB := range pa.out {
				for _, inB := range other.in {
					if spring.MatchLevels(inB.levels, outB.levels) {
						e, ok := edgeMap[other.app.Name]
						if !ok {
							e = &Edge{To: other.app.Name, Direction: "to"}
							edgeMap[other.app.Name] = e
						}
						e.Matches = append(e.Matches, BindingMatch{Local: outB.binding, Remote: inB.binding})
					}
				}
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

func isSolace(b spring.StreamBinding) bool {
	if strings.Contains(b.BinderType, "solace") {
		return true
	}
	// Heuristic: Solace topics typically use '/', while Kafka topics use '.'.
	return strings.Contains(b.Destination, "/")
}
