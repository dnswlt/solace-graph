package swcat

import (
	"reflect"
	"testing"

	catalogpb "github.com/dnswlt/solace-graph/internal/catalog/pb"
	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/spring"
)

func TestDependenciesRelations(t *testing.T) {
	local := maven.GAV{GroupId: "g", ArtifactId: "local"}
	remote := maven.GAV{GroupId: "g", ArtifactId: "remote"}
	compByGAV := map[maven.GAV]*catalogpb.Entity{
		remote: {Metadata: &catalogpb.Metadata{Name: "remote"}},
	}
	b := func(dest string) spring.StreamBinding { return spring.StreamBinding{Destination: dest} }
	edges := []graph.Edge{{
		To:        remote,
		Direction: graph.DirBoth,
		Matches: []graph.BindingMatch{
			{Direction: graph.DirFrom, Kind: graph.KindEvent, Local: b("remote/events"), Remote: b("remote/events")},
			{Direction: graph.DirFrom, Kind: graph.KindRequest, Local: b("remote/request"), Remote: b("remote/request")},
			// The neighbor's own dependency on local is reported from its node, not here.
			{Direction: graph.DirTo, Kind: graph.KindEvent, Local: b("local/events"), Remote: b("local/events")},
		},
	}}

	deps, dropped := dependencies(local, edges, compByGAV)
	if len(dropped) != 0 {
		t.Errorf("dropped: got %v, want none", dropped)
	}
	type rel struct {
		relation catalogpb.DependencyRelation
		evidence []string
	}
	var got []rel
	for _, d := range deps {
		got = append(got, rel{d.GetRelation(), d.GetEvidence()})
	}
	want := []rel{
		{catalogpb.DependencyRelation_DEPENDENCY_RELATION_CONSUMES, []string{"remote/events"}},
		{catalogpb.DependencyRelation_DEPENDENCY_RELATION_CALLS, []string{"remote/request"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps: got %v, want %v", got, want)
	}
}

func TestDependenciesDroppedRequest(t *testing.T) {
	local := maven.GAV{GroupId: "g", ArtifactId: "local"}
	replier := maven.GAV{GroupId: "g", ArtifactId: "replier"} // not in the catalog
	edges := []graph.Edge{{
		To:        replier,
		Direction: graph.DirFrom,
		Matches: []graph.BindingMatch{{
			Direction: graph.DirFrom, Kind: graph.KindRequest,
			Local:  spring.StreamBinding{Destination: "replier/request"},
			Remote: spring.StreamBinding{Destination: "replier/request"},
		}},
	}}
	deps, dropped := dependencies(local, edges, map[maven.GAV]*catalogpb.Entity{})
	if len(deps) != 0 {
		t.Errorf("deps: got %v, want none", deps)
	}
	want := []DroppedDependency{{Consumer: local, Producer: replier, Topics: []string{"replier/request"}, Missing: replier}}
	if !reflect.DeepEqual(dropped, want) {
		t.Errorf("dropped: got %+v, want %+v", dropped, want)
	}
}
