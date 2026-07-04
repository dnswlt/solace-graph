package commands

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/swcat"
)

// Swcat matches swcat catalog Component entities against the Applications
// collected from the Maven/Spring sources (as produced by the `collect`
// command) and reports the matches. Later it will upload the identified
// dependencies to swcat as observations.
func Swcat(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("swcat", flag.ContinueOnError)
	url := fs.String("url", "http://localhost:9191", "base URL of the swcat server")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: swcat [-url <swcat-url>] <file> [<file>...]")
	}

	var apps []graph.Application
	for _, path := range fs.Args() {
		a, err := readApplications(path)
		if err != nil {
			return err
		}
		apps = append(apps, a...)
	}

	entities, err := swcat.NewClient(*url).Entities()
	if err != nil {
		return err
	}
	slog.Info("retrieved catalog entities", "count", len(entities), "url", *url)

	res := swcat.MatchComponents(entities, apps)
	sort.Slice(res.Matches, func(i, j int) bool {
		return res.Matches[i].GroupID+":"+res.Matches[i].ArtifactID <
			res.Matches[j].GroupID+":"+res.Matches[j].ArtifactID
	})
	sort.Slice(res.UnmatchedApps, func(i, j int) bool {
		a, b := res.UnmatchedApps[i].GAV, res.UnmatchedApps[j].GAV
		return a.GroupId+":"+a.ArtifactId < b.GroupId+":"+b.ArtifactId
	})

	// Matched: a catalog Component and a collected Application share coordinates.
	var matched int
	for _, m := range res.Matches {
		if m.App != nil {
			matched++
			fmt.Fprintf(out, "MATCH        %s:%s\n", m.GroupID, m.ArtifactID)
		}
	}
	// Catalog-only: Component exists in the catalog, but no Application was collected.
	for _, m := range res.Matches {
		if m.App == nil {
			fmt.Fprintf(out, "catalog-only %s:%s\n", m.GroupID, m.ArtifactID)
		}
	}
	// App-only: Application was collected, but no matching Component in the catalog.
	for _, app := range res.UnmatchedApps {
		fmt.Fprintf(out, "app-only     %s:%s\n", app.GAV.GroupId, app.GAV.ArtifactId)
	}

	fmt.Fprintf(out, "\n%d matched, %d catalog-only, %d app-only (%d components ignored: no groupId)\n",
		matched, len(res.Matches)-matched, len(res.UnmatchedApps), res.Ignored)
	return nil
}
