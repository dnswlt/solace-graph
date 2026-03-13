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

// Build constructs the dependency graph from a list of discovered applications.
// A dependency (edge) exists from application A to B if A has a Solace input binding
// that matches a Solace output binding of B.
// It assumes that the input list contains unique applications by name.
func Build(apps []Application) []Node {
	sort.Slice(apps, func(i, j int) bool {
		return apps[i].Name < apps[j].Name
	})

	var nodes []Node
	for _, app := range apps {
		edgeMap := make(map[string]*Edge)

		for _, localB := range app.Bindings {
			if !isSolace(localB) {
				continue
			}

			for _, otherApp := range apps {
				if app.Name == otherApp.Name {
					continue
				}

				for _, otherB := range otherApp.Bindings {
					if !isSolace(otherB) {
						continue
					}

					// We define two directions:
					// 1. Current app consumes FROM other app (Local: IN, Other: OUT)
					// 2. Current app produces TO other app (Local: OUT, Other: IN)
					
					var direction string
					if localB.Direction == spring.BindingIn && otherB.Direction == spring.BindingOut {
						if spring.MatchTopics(localB.Destination, otherB.Destination) {
							direction = "from"
						}
					} else if localB.Direction == spring.BindingOut && otherB.Direction == spring.BindingIn {
						if spring.MatchTopics(otherB.Destination, localB.Destination) {
							direction = "to"
						}
					}

					if direction != "" {
						e, ok := edgeMap[otherApp.Name]
						if !ok {
							e = &Edge{To: otherApp.Name, Direction: direction}
							edgeMap[otherApp.Name] = e
						}
						e.Matches = append(e.Matches, BindingMatch{Local: localB, Remote: otherB})
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
			App:   app,
			Edges: edges,
		})
	}

	return nodes
}

func isSolace(b spring.StreamBinding) bool {
	if b.Binder == "solace" {
		return true
	}
	if b.Binder != "" {
		return false
	}
	// Heuristic: Solace topics typically use '/', while Kafka topics use '.'.
	return strings.Contains(b.Destination, "/") && !strings.Contains(b.Destination, ".")
}
