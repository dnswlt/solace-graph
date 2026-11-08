package swcat

import (
	"reflect"
	"strings"
	"testing"

	catalogpb "github.com/dnswlt/solace-graph/internal/catalog/pb"
	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/spring"
)

func testApp(id string, bindings ...spring.StreamBinding) *graph.Application {
	return &graph.Application{GAV: maven.GAV{GroupId: "g", ArtifactId: id}, Bindings: bindings}
}

func testBinding(name string, dir spring.BindingDirection, dest string, request bool) spring.StreamBinding {
	return spring.StreamBinding{BindingName: name, Direction: dir, Destination: dest, BinderType: "solace", Request: request}
}

func testMatch(app *graph.Application) Match {
	return Match{
		Component: &catalogpb.Entity{Metadata: &catalogpb.Metadata{Name: app.GAV.ArtifactId}},
		App:       app,
	}
}

// describe renders an endpoint compactly: "<dir> <kind> <address> [names] -> counterparts".
func describe(ep *catalogpb.Endpoint) string {
	dir := "in"
	if ep.GetDirection() == catalogpb.EndpointDirection_ENDPOINT_DIRECTION_OUTBOUND {
		dir = "out"
	}
	kind := "event"
	if ep.GetKind() == catalogpb.EndpointKind_ENDPOINT_KIND_REQUEST {
		kind = "request"
	}
	var cps []string
	for _, cp := range ep.GetCounterparts() {
		s := cp.GetEntity().GetName()
		if s == "" {
			s = "ext:" + cp.GetExternal()
		}
		if cp.GetAddress() != "" {
			s += "@" + cp.GetAddress()
		}
		cps = append(cps, s)
	}
	return dir + " " + kind + " " + ep.GetAddress() + " [" + strings.Join(ep.GetNames(), ",") + "] -> " + strings.Join(cps, ",")
}

func TestObservedEndpoints(t *testing.T) {
	requester := testApp("requester",
		testBinding("ask-out-0", spring.BindingOut, "svc/${STAGE}/orders/request", true),
		testBinding("events-in-0", spring.BindingIn, "svc/*/prices", false),
		testBinding("notify-out-0", spring.BindingOut, "svc/notify", false),
		testBinding("notifyAgain-out-0", spring.BindingOut, "svc/notify", false), // merged into one endpoint
		testBinding("lonely-out-0", spring.BindingOut, "svc/nobody/listens", false),
	)
	replier := testApp("replier",
		testBinding("serve-in-0", spring.BindingIn, "svc/prod/orders/request", false),
	)
	producer := testApp("producer",
		testBinding("prices-out-0", spring.BindingOut, "svc/prod/prices", false),
	)
	outsider := testApp("outsider", // not in the catalog
		testBinding("notify-in-0", spring.BindingIn, "svc/notify", false),
	)
	res := MatchResult{
		Matches:       []Match{testMatch(requester), testMatch(replier), testMatch(producer)},
		UnmatchedApps: []*graph.Application{outsider},
	}

	msgs, dropped := ObservedEndpoints(res)
	bySource := make(map[string][]string)
	for _, oe := range msgs {
		if oe.GetDetectedBy() != DetectedBy {
			t.Errorf("DetectedBy = %q", oe.GetDetectedBy())
		}
		var eps []string
		for _, ep := range oe.GetEndpoints() {
			eps = append(eps, describe(ep))
		}
		bySource[oe.GetSource().GetName()] = eps
	}

	want := map[string][]string{
		"requester": { // sorted by address (byte order: '$' < '*' < 'n')
			"out request svc/${STAGE}/orders/request [ask-out-0] -> replier@svc/prod/orders/request",
			"in event svc/*/prices [events-in-0] -> producer@svc/prod/prices",
			"out event svc/nobody/listens [lonely-out-0] -> ",
			"out event svc/notify [notify-out-0,notifyAgain-out-0] -> ext:g:outsider",
		},
		// The replier serves requests: its inbound endpoint is a request endpoint.
		"replier": {
			"in request svc/prod/orders/request [serve-in-0] -> requester@svc/${STAGE}/orders/request",
		},
		"producer": {
			"out event svc/prod/prices [prices-out-0] -> requester@svc/*/prices",
		},
	}
	if !reflect.DeepEqual(bySource, want) {
		t.Errorf("endpoints:\n got %q\nwant %q", bySource, want)
	}

	// The outsider's dependency on the requester is found once, from the matched end.
	wantDropped := []DroppedDependency{{
		Consumer: outsider.GAV, Producer: requester.GAV, Topics: []string{"svc/notify"}, Missing: outsider.GAV,
	}}
	if !reflect.DeepEqual(dropped, wantDropped) {
		t.Errorf("dropped:\n got %+v\nwant %+v", dropped, wantDropped)
	}
}

func TestObservedEndpoints_ClearsSourcesWithoutBindings(t *testing.T) {
	res := MatchResult{Matches: []Match{testMatch(testApp("quiet"))}}
	msgs, _ := ObservedEndpoints(res)
	if len(msgs) != 1 || len(msgs[0].GetEndpoints()) != 0 {
		t.Fatalf("got %v, want one message with no endpoints", msgs)
	}
}

func TestSummary(t *testing.T) {
	ref := func(name string) *catalogpb.Counterpart {
		return &catalogpb.Counterpart{Entity: &catalogpb.Ref{Kind: KindComponent, Name: name}}
	}
	oe := &catalogpb.ObservedEndpoints{Endpoints: []*catalogpb.Endpoint{
		{Direction: catalogpb.EndpointDirection_ENDPOINT_DIRECTION_INBOUND, Counterparts: []*catalogpb.Counterpart{ref("producer")}},
		{Direction: catalogpb.EndpointDirection_ENDPOINT_DIRECTION_OUTBOUND, Kind: catalogpb.EndpointKind_ENDPOINT_KIND_REQUEST,
			Counterparts: []*catalogpb.Counterpart{ref("replier")}},
		{Direction: catalogpb.EndpointDirection_ENDPOINT_DIRECTION_OUTBOUND,
			Counterparts: []*catalogpb.Counterpart{ref("consumer"), {External: "g:x"}}},
	}}
	want := "3 endpoints; depends on producer, replier; used by consumer; 1 external counterpart"
	if got := Summary(oe); got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}
