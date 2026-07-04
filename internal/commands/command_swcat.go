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
// By default it runs as a dry run, printing what it would upload. Pass -post to
// actually send the observations.
func Swcat(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("swcat", flag.ContinueOnError)
	url := fs.String("url", "http://localhost:9191", "base URL of the swcat server")
	post := fs.Bool("post", false, "upload the observed dependencies to swcat (default: dry run)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: swcat [-url <swcat-url>] [-post] <file> [<file>...]")
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

	if !*post {
		fmt.Fprintf(out, "\ndry run: pass -post to upload to %s\n", *url)
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
