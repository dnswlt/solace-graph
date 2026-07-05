package commands

import (
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/swcat"
)

// Swcat matches swcat catalog Component entities against the Applications
// collected from the Maven/Spring sources (as produced by the `collect`
// command), derives the dependencies between matched Components from the
// Spring Cloud Stream bindings, and reports them to swcat as observed
// dependencies (one POST per source Component).
//
// By default it uploads the observations. Pass -dry-run to only print what it
// would send without contacting swcat, or -delete to remove all observations
// this tool previously reported (see swcat.DetectedBy) and exit.
func Swcat(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("swcat", flag.ContinueOnError)
	url := fs.String("url", "http://localhost:9191", "base URL of the swcat server")
	dryRun := fs.Bool("dry-run", false, "print what would be uploaded without sending it to swcat")
	del := fs.Bool("delete", false, "delete all observations previously reported by this tool, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *del {
		client := swcat.NewClient(*url)
		if err := client.DeleteObservedDependencies(swcat.DetectedBy); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted observations detected by %q from %s\n", swcat.DetectedBy, *url)
		return nil
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: swcat [-url <swcat-url>] [-dry-run | -delete] <file> [<file>...]")
	}

	var apps []graph.Application
	for _, path := range fs.Args() {
		a, err := readApplications(path)
		if err != nil {
			return err
		}
		apps = append(apps, a...)
	}

	client := swcat.NewClient(*url)
	entities, err := client.Entities()
	if err != nil {
		return err
	}
	slog.Info("retrieved catalog entities", "count", len(entities), "url", *url)

	res := swcat.MatchComponents(entities, apps)
	var matched int
	for _, m := range res.Matches {
		if m.App != nil {
			matched++
		}
	}
	fmt.Fprintf(out, "%d matched, %d catalog-only, %d app-only (%d components ignored: no groupId)\n",
		matched, len(res.Matches)-matched, len(res.UnmatchedApps), res.Ignored)

	obs := swcat.ObservedDependencies(res)
	var withDeps, totalDeps int
	for _, od := range obs {
		if n := len(od.GetDependencies()); n > 0 {
			withDeps++
			totalDeps += n
			fmt.Fprintf(out, "  %s: %s\n", od.GetSource().GetName(), swcat.Summary(od))
		}
	}
	fmt.Fprintf(out, "%d source components with dependencies, %d observed dependencies total (%d source messages)\n",
		withDeps, totalDeps, len(obs))

	if *dryRun {
		fmt.Fprintf(out, "\ndry run: would upload to %s (omit -dry-run to send)\n", *url)
		return nil
	}

	var failed int
	for _, od := range obs {
		if err := client.PostObservedDependencies(od); err != nil {
			slog.Error("failed to post observed dependencies", "source", od.GetSource().GetName(), "err", err)
			failed++
		}
	}
	fmt.Fprintf(out, "\nposted %d/%d source messages to %s\n", len(obs)-failed, len(obs), *url)
	if failed > 0 {
		return fmt.Errorf("%d of %d posts failed", failed, len(obs))
	}
	return nil
}
