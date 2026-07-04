package swcat

import (
	"strings"

	catalogpb "github.com/dnswlt/solace-graph/internal/catalog/pb"
	"github.com/dnswlt/solace-graph/internal/graph"
)

const (
	// KindComponent is the only entity kind considered for Maven matching.
	KindComponent = "Component"
	kindSystem    = "System"
	kindDomain    = "Domain"

	// annCoords, if set on a Component, overrides the Maven coordinates derived
	// from the entity name. Its value is "groupId:artifactId[:version]".
	annCoords = "maven.apache.org/coords"
	// annGroupID, if set, provides the Maven groupId. It may be set on the
	// Component itself or inherited from its parent System or Domain.
	annGroupID = "maven.apache.org/groupId"
)

// Match is a resolved correspondence between a catalog Component and the Maven
// coordinates derived from it. App is nil when no Application matched (the
// component exists in the catalog but no application was collected for it).
type Match struct {
	Component  *catalogpb.Entity
	GroupID    string // Maven groupId resolved for the component
	ArtifactID string // Maven artifactId resolved for the component
	App        *graph.Application
}

// MatchResult holds the outcome of matching catalog Components against collected
// Applications, from both directions.
type MatchResult struct {
	// Matches has one entry per Component with a resolvable groupId. App is set
	// when a collected Application shares its coordinates, nil otherwise
	// (catalog-only: in the catalog but not built here).
	Matches []Match
	// UnmatchedApps are Applications no Component matched (app-only: collected
	// from the sources but absent from the catalog).
	UnmatchedApps []*graph.Application
	// Ignored counts Components skipped because no groupId could be resolved.
	Ignored int
}

// MatchComponents resolves every Component entity to Maven coordinates
// (groupId:artifactId) and matches it against apps by those coordinates.
// Components whose groupId cannot be resolved are ignored (counted, not matched).
func MatchComponents(entities []*catalogpb.Entity, apps []graph.Application) MatchResult {
	ix := newEntityIndex(entities)

	appByCoords := make(map[string]*graph.Application, len(apps))
	for i := range apps {
		app := &apps[i]
		appByCoords[app.GAV.GroupId+":"+app.GAV.ArtifactId] = app
	}

	var res MatchResult
	matchedCoords := make(map[string]bool)
	for _, e := range entities {
		if e.GetKind() != KindComponent {
			continue
		}
		groupID, artifactID := ix.resolveCoords(e)
		if groupID == "" {
			res.Ignored++
			continue
		}
		coords := groupID + ":" + artifactID
		app := appByCoords[coords]
		if app != nil {
			matchedCoords[coords] = true
		}
		res.Matches = append(res.Matches, Match{
			Component:  e,
			GroupID:    groupID,
			ArtifactID: artifactID,
			App:        app,
		})
	}

	for i := range apps {
		coords := apps[i].GAV.GroupId + ":" + apps[i].GAV.ArtifactId
		if !matchedCoords[coords] {
			res.UnmatchedApps = append(res.UnmatchedApps, &apps[i])
		}
	}
	return res
}

// entityIndex allows looking up entities by kind and name so that Refs (e.g. a
// component's parent system) can be resolved.
type entityIndex struct {
	byKindName map[string]*catalogpb.Entity
}

func newEntityIndex(entities []*catalogpb.Entity) *entityIndex {
	ix := &entityIndex{byKindName: make(map[string]*catalogpb.Entity, len(entities))}
	for _, e := range entities {
		ix.byKindName[e.GetKind()+"\x00"+e.GetMetadata().GetName()] = e
	}
	return ix
}

// ref looks up the entity a Ref points to. If the Ref omits the kind,
// expectedKind is used instead.
func (ix *entityIndex) ref(ref *catalogpb.Ref, expectedKind string) *catalogpb.Entity {
	if ref == nil || ref.GetName() == "" {
		return nil
	}
	kind := ref.GetKind()
	if kind == "" {
		kind = expectedKind
	}
	return ix.byKindName[kind+"\x00"+ref.GetName()]
}

// resolveCoords derives the Maven groupId and artifactId for a Component. The
// artifactId defaults to the entity name; the groupId is resolved from
// annotations, walking up to the parent System and Domain if necessary. An
// explicit "maven.apache.org/coords" annotation overrides both.
func (ix *entityIndex) resolveCoords(e *catalogpb.Entity) (groupID, artifactID string) {
	md := e.GetMetadata()
	artifactID = md.GetName()

	if coords := md.GetAnnotations()[annCoords]; coords != "" {
		g, a := parseCoords(coords)
		if a != "" {
			artifactID = a
		}
		return g, artifactID
	}

	return ix.resolveGroupID(e), artifactID
}

// resolveGroupID looks for the groupId annotation on the component, then on its
// parent System, then on the System's Domain (or the component's own Domain
// ref). Returns "" if none is found.
func (ix *entityIndex) resolveGroupID(e *catalogpb.Entity) string {
	if g := e.GetMetadata().GetAnnotations()[annGroupID]; g != "" {
		return g
	}

	spec := e.GetComponentSpec()
	if sys := ix.ref(spec.GetSystem(), kindSystem); sys != nil {
		if g := sys.GetMetadata().GetAnnotations()[annGroupID]; g != "" {
			return g
		}
		if dom := ix.ref(sys.GetSystemSpec().GetDomain(), kindDomain); dom != nil {
			if g := dom.GetMetadata().GetAnnotations()[annGroupID]; g != "" {
				return g
			}
		}
	}
	if dom := ix.ref(spec.GetDomain(), kindDomain); dom != nil {
		if g := dom.GetMetadata().GetAnnotations()[annGroupID]; g != "" {
			return g
		}
	}
	return ""
}

// parseCoords splits a "groupId:artifactId[:version]" string into its groupId
// and artifactId parts.
func parseCoords(coords string) (groupID, artifactID string) {
	parts := strings.Split(coords, ":")
	if len(parts) >= 1 {
		groupID = parts[0]
	}
	if len(parts) >= 2 {
		artifactID = parts[1]
	}
	return groupID, artifactID
}
