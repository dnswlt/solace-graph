package graph

import (
	"testing"

	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/spring"
)

func app(id string, bindings ...spring.StreamBinding) Application {
	return Application{GAV: maven.GAV{GroupId: "g", ArtifactId: id}, Bindings: bindings}
}

func binding(name string, dir spring.BindingDirection, dest string, request bool) spring.StreamBinding {
	return spring.StreamBinding{BindingName: name, Direction: dir, Destination: dest, BinderType: "solace", Request: request}
}

// edgesByNode returns, per artifactId, its edges keyed by neighbor artifactId.
func edgesByNode(nodes []Node) map[string]map[string]Edge {
	out := make(map[string]map[string]Edge)
	for _, n := range nodes {
		m := make(map[string]Edge)
		for _, e := range n.Edges {
			m[e.To.ArtifactId] = e
		}
		out[n.App.GAV.ArtifactId] = m
	}
	return out
}

func TestBuildEventDependency(t *testing.T) {
	edges := edgesByNode(Build([]Application{
		app("producer", binding("pub-out-0", spring.BindingOut, "svc/events", false)),
		app("consumer", binding("sub-in-0", spring.BindingIn, "svc/*", false)),
	}))
	if e := edges["consumer"]["producer"]; e.Direction != DirFrom || e.Matches[0].Kind != KindEvent {
		t.Errorf("consumer->producer: got direction %q kind %q, want %q %q", e.Direction, e.Matches[0].Kind, DirFrom, KindEvent)
	}
	if e := edges["producer"]["consumer"]; e.Direction != DirTo {
		t.Errorf("producer->consumer: got direction %q, want %q", e.Direction, DirTo)
	}
}

func TestBuildRequestReplyDependency(t *testing.T) {
	edges := edgesByNode(Build([]Application{
		app("requester", binding("req-out-0", spring.BindingOut, "svc/${STAGE}/foo/request", true)),
		app("replier", binding("answer-in-0", spring.BindingIn, "svc/prod/foo/request", false)),
	}))
	// The requester depends on the replier, not the other way round.
	e := edges["requester"]["replier"]
	if e.Direction != DirFrom {
		t.Fatalf("requester->replier: got direction %q, want %q", e.Direction, DirFrom)
	}
	m := e.Matches[0]
	if m.Kind != KindRequest || m.Local.BindingName != "req-out-0" || m.Remote.BindingName != "answer-in-0" {
		t.Errorf("requester match: got %+v", m)
	}
	e = edges["replier"]["requester"]
	if e.Direction != DirTo || e.Matches[0].Kind != KindRequest || e.Matches[0].Local.BindingName != "answer-in-0" {
		t.Errorf("replier->requester: got %+v", e)
	}
}

func TestBuildRequestAndEventIsBoth(t *testing.T) {
	// a sends requests to b and consumes b's events: two dependencies of a on b.
	// b also consumes a's events, a dependency the other way, so the edge is "both".
	edges := edgesByNode(Build([]Application{
		app("a",
			binding("req-out-0", spring.BindingOut, "b/request", true),
			binding("evt-in-0", spring.BindingIn, "b/events", false),
			binding("pub-out-0", spring.BindingOut, "a/events", false)),
		app("b",
			binding("answer-in-0", spring.BindingIn, "b/request", false),
			binding("pub-out-0", spring.BindingOut, "b/events", false),
			binding("sub-in-0", spring.BindingIn, "a/events", false)),
	}))
	e := edges["a"]["b"]
	if e.Direction != DirBoth || len(e.Matches) != 3 {
		t.Fatalf("a->b: got direction %q with %d matches, want %q with 3", e.Direction, len(e.Matches), DirBoth)
	}
	kinds := map[string]string{}
	for _, m := range e.Matches {
		kinds[m.Local.BindingName] = m.Direction + "/" + m.Kind
	}
	want := map[string]string{
		"req-out-0": DirFrom + "/" + KindRequest,
		"evt-in-0":  DirFrom + "/" + KindEvent,
		"pub-out-0": DirTo + "/" + KindEvent,
	}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("match %s: got %q, want %q", k, kinds[k], v)
		}
	}
}
