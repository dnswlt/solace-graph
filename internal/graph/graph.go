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
		if a.Bindings[i].BindingName != a.Bindings[j].BindingName {
			return a.Bindings[i].BindingName < a.Bindings[j].BindingName
		}
		return a.Bindings[i].Destination < a.Bindings[j].Destination
	})
}

// Match directions, from the point of view of the node's (local) application.
const (
	// DirFrom: the local application depends on the remote one. It consumes the
	// remote's messages, or (for KindRequest) sends requests the remote replies to.
	DirFrom = "from"
	// DirTo: the remote application depends on the local one.
	DirTo = "to"
	// DirBoth: an edge with matches in both directions.
	DirBoth = "both"
)

// Match kinds.
const (
	// KindEvent is a plain publish/subscribe match: the consumer depends on the
	// producer.
	KindEvent = "event"
	// KindRequest is a request/reply match: the producing side sends requests
	// (spring.StreamBinding.Request) and depends on the consuming side, the replier.
	KindRequest = "request"
)

// BindingMatch describes a specific link between the current application and another one.
type BindingMatch struct {
	Direction string               `json:"direction"` // DirFrom if local depends on remote, DirTo otherwise
	Kind      string               `json:"kind"`      // KindEvent or KindRequest
	Local     spring.StreamBinding `json:"local"`     // binding of the node's application
	Remote    spring.StreamBinding `json:"remote"`    // binding of the other application
}

// Edge represents a dependency on another application, including the reason (matching bindings).
type Edge struct {
	To        maven.GAV      `json:"to"`        // GAV of the other application
	Direction string         `json:"direction"` // DirFrom, DirTo, or DirBoth
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
// Request/reply reverses this: if B's output binding is a request binding, B
// depends on A (the replier) rather than A on B.
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
		edgeMap := make(map[maven.GAV]*Edge)

		for _, other := range prepared {
			if pa.app.GAV == other.app.GAV {
				continue
			}

			var fromEdge, toEdge *Edge
			// add records a match on the edge for its dependency direction.
			add := func(dir, kind string, local, remote spring.StreamBinding) {
				e := &fromEdge
				if dir == DirTo {
					e = &toEdge
				}
				if *e == nil {
					*e = &Edge{To: other.app.GAV, Direction: dir}
				}
				(*e).Matches = append((*e).Matches, BindingMatch{Direction: dir, Kind: kind, Local: local, Remote: remote})
			}

			// Current app consumes what the other app produces (pa.in matches other.out):
			// pa depends on other, unless other is sending requests for pa to answer.
			for _, inB := range pa.in {
				for _, outB := range other.out {
					if inB.syntax == outB.syntax && spring.MatchLevels(inB.levels, outB.levels) {
						if outB.binding.Request {
							add(DirTo, KindRequest, inB.binding, outB.binding)
						} else {
							add(DirFrom, KindEvent, inB.binding, outB.binding)
						}
					}
				}
			}

			// Other app consumes what the current app produces (pa.out matches other.in):
			// other depends on pa, unless pa is sending requests for other to answer.
			for _, outB := range pa.out {
				for _, inB := range other.in {
					if inB.syntax == outB.syntax && spring.MatchLevels(inB.levels, outB.levels) {
						if outB.binding.Request {
							add(DirFrom, KindRequest, outB.binding, inB.binding)
						} else {
							add(DirTo, KindEvent, outB.binding, inB.binding)
						}
					}
				}
			}

			switch {
			case fromEdge != nil && toEdge != nil:
				fromEdge.Direction = DirBoth
				fromEdge.Matches = append(fromEdge.Matches, toEdge.Matches...)
				edgeMap[other.app.GAV] = fromEdge
			case fromEdge != nil:
				edgeMap[other.app.GAV] = fromEdge
			case toEdge != nil:
				edgeMap[other.app.GAV] = toEdge
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
			if edges[i].To.GroupId != edges[j].To.GroupId {
				return edges[i].To.GroupId < edges[j].To.GroupId
			}
			return edges[i].To.ArtifactId < edges[j].To.ArtifactId
		})

		nodes = append(nodes, Node{
			App:   pa.app,
			Edges: edges,
		})
	}

	return nodes
}
