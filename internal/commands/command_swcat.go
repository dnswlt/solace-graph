package commands

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

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
	obs, dropped := swcat.ObservedDependencies(res)

	var matched int
	var catalogOnly []string
	for _, m := range res.Matches {
		if m.App != nil {
			matched++
			continue
		}
		catalogOnly = append(catalogOnly, fmt.Sprintf("%s (%s:%s)", m.Component.GetMetadata().GetName(), m.GroupID, m.ArtifactID))
	}
	fmt.Fprintf(out, "matched %d/%d catalog components and %d/%d collected applications\n",
		matched, len(res.Matches)+len(res.Unresolved), matched, len(apps))

	// Everything below reports what did *not* work, most actionable first.
	var unresolved []string
	for _, e := range res.Unresolved {
		unresolved = append(unresolved, e.GetMetadata().GetName())
	}
	printProblems(out, "catalog components have no Maven groupId, so they can never match "+
		"(annotate the component, its system or its domain with maven.apache.org/groupId, "+
		"or the component with maven.apache.org/coords)", unresolved)

	var appOnly []string
	for _, a := range res.UnmatchedApps {
		appOnly = append(appOnly, a.GAV.GroupId+":"+a.GAV.ArtifactId)
	}
	printProblems(out, "collected applications have no catalog component, so nothing is reported for them", appOnly)

	var drops []string
	for _, d := range dropped {
		drops = append(drops, fmt.Sprintf("%s -> %s:%s [%s]",
			d.Source.GetMetadata().GetName(), d.Target.GroupId, d.Target.ArtifactId, strings.Join(d.Topics, ", ")))
	}
	printProblems(out, "detected dependencies are not reported, because the producing application "+
		"is one of those missing from the catalog", drops)

	printProblems(out, "catalog components have no collected application "+
		"(not built from the scanned sources; expected unless the sources should cover them)", catalogOnly)

	var withDeps, totalDeps int
	for _, od := range obs {
		if n := len(od.GetDependencies()); n > 0 {
			withDeps++
			totalDeps += n
		}
	}
	fmt.Fprintf(out, "\nreporting %d dependencies of %d source components (%d messages, the rest clearing previous observations)\n",
		totalDeps, withDeps, len(obs))

	if *dryRun {
		for _, od := range obs {
			if len(od.GetDependencies()) > 0 {
				fmt.Fprintf(out, "  %s: %s\n", od.GetSource().GetName(), swcat.Summary(od))
			}
		}
		fmt.Fprintf(out, "\ndry run: nothing sent to %s (omit -dry-run to upload)\n", *url)
		return nil
	}

	var failed []string
	for _, od := range obs {
		if err := client.PostObservedDependencies(od); err != nil {
			slog.Debug("failed to post observed dependencies", "source", od.GetSource().GetName(), "err", err)
			failed = append(failed, fmt.Sprintf("%s: %v", od.GetSource().GetName(), err))
		}
	}
	fmt.Fprintf(out, "\nposted %d/%d messages to %s\n", len(obs)-len(failed), len(obs), *url)
	printProblems(out, "messages were rejected by swcat", failed)
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d posts failed", len(failed), len(obs))
	}
	return nil
}

// maxListed caps how many entries printProblems spells out; beyond it only the
// remaining count is shown, so a large catalog can't bury the summary.
const maxListed = 20

// printProblems writes one "N <what went wrong>" section listing the offending
// entries, or nothing at all if there are none.
func printProblems(out io.Writer, headline string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%d %s:\n", len(items), headline)
	for i, item := range items {
		if i == maxListed {
			fmt.Fprintf(out, "  ... and %d more\n", len(items)-maxListed)
			break
		}
		fmt.Fprintf(out, "  %s\n", item)
	}
}
