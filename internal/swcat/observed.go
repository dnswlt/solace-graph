package swcat

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"

	catalogpb "github.com/dnswlt/solace-graph/internal/catalog/pb"
	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/spring"
)

// DetectedBy is the tool label recorded on every observation we report.
const DetectedBy = "solace-graph"

// DroppedDependency is a dependency that was detected but involves an
// application without a catalog Component. It is still reported, as an
// external counterpart of the matched end's endpoint, but it can never appear
// in the catalog as a relationship between two Components.
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

// ObservedEndpoints runs the dependency analysis over the collected
// applications and builds, for each matched source Component, an
// ObservedEndpoints message: one Endpoint per distinct (direction, kind,
// address) of its bindings, with the applications each endpoint was matched
// with as its counterparts. Counterparts with a catalog Component are reported
// as entities, the others as external (by Maven coordinates).
//
// One message is returned per matched source Component, including sources with
// no bindings: reporting an empty list clears any endpoints previously
// observed by this tool, so the result is a full, idempotent sync.
//
// The graph is built over the unmatched applications as well, even though they
// can never be a source, so they show up as external counterparts. The
// dependencies involving them are also returned as DroppedDependency, for the
// caller to list what the catalog is missing.
func ObservedEndpoints(res MatchResult) ([]*catalogpb.ObservedEndpoints, []DroppedDependency) {
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

	msgs := make([]*catalogpb.ObservedEndpoints, 0, len(nodes))
	var dropped []DroppedDependency
	for _, node := range nodes {
		src, ok := compByGAV[node.App.GAV]
		if !ok {
			continue // an unmatched application: no Component to report it for
		}
		msgs = append(msgs, &catalogpb.ObservedEndpoints{
			Source:     componentRef(src),
			DetectedBy: DetectedBy,
			Endpoints:  endpoints(node, compByGAV),
		})
		dropped = append(dropped, droppedDependencies(node, compByGAV)...)
	}
	return msgs, dropped
}

// endpointKey identifies an Endpoint: bindings sharing it are merged into one.
type endpointKey struct {
	direction catalogpb.EndpointDirection
	kind      catalogpb.EndpointKind
	address   string
}

// bindingKey identifies a binding of the node's application.
type bindingKey struct {
	name, destination string
}

// endpoints converts the node's bindings into Endpoints, with the node's edge
// matches as their counterparts.
func endpoints(node graph.Node, compByGAV map[maven.GAV]*catalogpb.Entity) []*catalogpb.Endpoint {
	// An inbound binding serves requests if a request binding matched it: the
	// replier's own configuration doesn't say so.
	serves := make(map[bindingKey]bool)
	for _, e := range node.Edges {
		for _, m := range e.Matches {
			if m.Kind == graph.KindRequest && m.Local.Direction == spring.BindingIn {
				serves[bindingKey{m.Local.BindingName, m.Local.Destination}] = true
			}
		}
	}
	keyOf := func(b spring.StreamBinding) (endpointKey, bool) {
		k := endpointKey{address: b.Destination, kind: catalogpb.EndpointKind_ENDPOINT_KIND_EVENT}
		switch b.Direction {
		case spring.BindingIn:
			k.direction = catalogpb.EndpointDirection_ENDPOINT_DIRECTION_INBOUND
			if serves[bindingKey{b.BindingName, b.Destination}] {
				k.kind = catalogpb.EndpointKind_ENDPOINT_KIND_REQUEST
			}
		case spring.BindingOut:
			k.direction = catalogpb.EndpointDirection_ENDPOINT_DIRECTION_OUTBOUND
			if b.Request {
				k.kind = catalogpb.EndpointKind_ENDPOINT_KIND_REQUEST
			}
		default:
			return k, false // no direction known: not a reportable endpoint
		}
		return k, true
	}

	byKey := make(map[endpointKey]*catalogpb.Endpoint)
	for _, b := range node.App.Bindings {
		k, ok := keyOf(b)
		if !ok {
			continue
		}
		ep := byKey[k]
		if ep == nil {
			ep = &catalogpb.Endpoint{Address: k.address, Direction: k.direction, Kind: k.kind, Technology: b.BinderType}
			byKey[k] = ep
		}
		if !slices.Contains(ep.Names, b.BindingName) {
			ep.Names = append(ep.Names, b.BindingName)
		}
	}

	type cpKey struct{ entity, external, address string }
	seenCP := make(map[*catalogpb.Endpoint]map[cpKey]bool)
	for _, e := range node.Edges {
		comp, inCatalog := compByGAV[e.To]
		for _, m := range e.Matches {
			k, ok := keyOf(m.Local)
			if !ok {
				continue
			}
			ep := byKey[k]
			cp := &catalogpb.Counterpart{}
			if inCatalog {
				cp.Entity = componentRef(comp)
			} else {
				cp.External = e.To.GroupId + ":" + e.To.ArtifactId
			}
			if m.Remote.Destination != m.Local.Destination {
				cp.Address = m.Remote.Destination
			}
			ck := cpKey{cp.GetEntity().GetName(), cp.External, cp.Address}
			if seenCP[ep] == nil {
				seenCP[ep] = make(map[cpKey]bool)
			}
			if seenCP[ep][ck] {
				continue // matched through several bindings of the same endpoint
			}
			seenCP[ep][ck] = true
			ep.Counterparts = append(ep.Counterparts, cp)
		}
	}

	out := make([]*catalogpb.Endpoint, 0, len(byKey))
	for _, ep := range byKey {
		sort.Strings(ep.Names)
		slices.SortFunc(ep.Counterparts, func(a, b *catalogpb.Counterpart) int {
			return cmp.Or(
				cmp.Compare(a.GetEntity().GetName(), b.GetEntity().GetName()),
				cmp.Compare(a.External, b.External),
				cmp.Compare(a.Address, b.Address),
			)
		})
		out = append(out, ep)
	}
	slices.SortFunc(out, func(a, b *catalogpb.Endpoint) int {
		return cmp.Or(
			cmp.Compare(a.Address, b.Address),
			cmp.Compare(a.Direction, b.Direction),
			cmp.Compare(a.Kind, b.Kind),
		)
	})
	return out
}

// droppedDependencies returns the dependencies between the node's application
// and applications without a catalog Component, in either direction. A
// dependency between a matched and an unmatched application is only ever seen
// from the matched end, so each is found exactly once.
func droppedDependencies(node graph.Node, compByGAV map[maven.GAV]*catalogpb.Entity) []DroppedDependency {
	local := node.App.GAV
	var dropped []DroppedDependency
	for _, e := range node.Edges {
		if _, matched := compByGAV[e.To]; matched {
			continue
		}
		// Evidence is the destination of the depending side's own binding: the
		// topic a consumer subscribes to, or a requester sends its requests to.
		dependsOn := make(map[string]bool)  // local depends on the neighbor
		dependents := make(map[string]bool) // the neighbor depends on local
		for _, m := range e.Matches {
			if m.Direction == graph.DirFrom {
				dependsOn[m.Local.Destination] = true
			} else {
				dependents[m.Remote.Destination] = true
			}
		}
		if ev := sortedKeys(dependsOn); len(ev) > 0 {
			dropped = append(dropped, DroppedDependency{Consumer: local, Producer: e.To, Topics: ev, Missing: e.To})
		}
		if ev := sortedKeys(dependents); len(ev) > 0 {
			dropped = append(dropped, DroppedDependency{Consumer: e.To, Producer: local, Topics: ev, Missing: e.To})
		}
	}
	return dropped
}

// sortedKeys returns the non-empty keys of set, sorted for deterministic output.
func sortedKeys(set map[string]bool) []string {
	var out []string
	for k := range set {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Summary renders one ObservedEndpoints message as a compact line: the number
// of endpoints, and the catalog Components the source depends on and that
// depend on it, e.g. "12 endpoints; depends on a, b; used by c".
func Summary(oe *catalogpb.ObservedEndpoints) string {
	dependsOn := make(map[string]bool)
	usedBy := make(map[string]bool)
	external := 0
	for _, ep := range oe.GetEndpoints() {
		in := ep.GetDirection() == catalogpb.EndpointDirection_ENDPOINT_DIRECTION_INBOUND
		request := ep.GetKind() == catalogpb.EndpointKind_ENDPOINT_KIND_REQUEST
		for _, cp := range ep.GetCounterparts() {
			name := cp.GetEntity().GetName()
			if name == "" {
				external++
				continue
			}
			if in != request { // inbound event or outbound request
				dependsOn[name] = true
			} else {
				usedBy[name] = true
			}
		}
	}
	parts := []string{plural(len(oe.GetEndpoints()), "endpoint")}
	if ns := sortedKeys(dependsOn); len(ns) > 0 {
		parts = append(parts, "depends on "+strings.Join(ns, ", "))
	}
	if ns := sortedKeys(usedBy); len(ns) > 0 {
		parts = append(parts, "used by "+strings.Join(ns, ", "))
	}
	if external > 0 {
		parts = append(parts, plural(external, "external counterpart"))
	}
	return strings.Join(parts, "; ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func componentRef(e *catalogpb.Entity) *catalogpb.Ref {
	md := e.GetMetadata()
	return &catalogpb.Ref{
		Kind:      KindComponent,
		Namespace: md.GetNamespace(),
		Name:      md.GetName(),
	}
}
