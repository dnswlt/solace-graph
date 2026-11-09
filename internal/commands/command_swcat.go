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
// command), and reports each matched Component's endpoints (its Spring Cloud
// Stream bindings) to swcat as observed endpoints, with the applications each
// endpoint connects to as its counterparts (one POST per source Component).
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
		if err := client.DeleteObservedEndpoints(swcat.DetectedBy); err != nil {
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
	obs, dropped := swcat.ObservedEndpoints(res)

	var matched int
	reportedAs := make(map[string]string) // coords -> name of the component that won
	for _, m := range res.Matches {
		if m.App == nil {
			continue
		}
		matched++
		if !m.Shadowed {
			reportedAs[m.GroupID+":"+m.ArtifactID] = m.Component.GetMetadata().GetName()
		}
	}
	// Counted per side: duplicate coordinates make several components match the
	// same module, so the two numbers need not agree.
	fmt.Fprintf(out, "matched %d/%d catalog components and %d/%d collected applications\n",
		matched, len(res.Matches)+len(res.Unresolved), len(apps)-len(res.UnmatchedApps), len(apps))

	// Everything below reports what did *not* work, most actionable first.
	var unresolved []string
	for _, e := range res.Unresolved {
		unresolved = append(unresolved, e.GetMetadata().GetName())
	}
	printProblems(out, "catalog components with no Maven groupId (annotate the component, its system "+
		"or its domain with maven.apache.org/groupId, or the component with maven.apache.org/coords)", unresolved)

	var shadowed []string
	for _, m := range res.Matches {
		if !m.Shadowed {
			continue
		}
		coords := m.GroupID + ":" + m.ArtifactID
		shadowed = append(shadowed, fmt.Sprintf("%s (%s), reported as %s",
			m.Component.GetMetadata().GetName(), coords, reportedAs[coords]))
	}
	printProblems(out, "catalog components skipped, another component has the same Maven coordinates", shadowed)

	var appOnly []string
	for _, a := range res.UnmatchedApps {
		appOnly = append(appOnly, a.GAV.GroupId+":"+a.GAV.ArtifactId)
	}
	printProblems(out, "scanned Maven modules that no catalog component claims", appOnly)

	// A dependency on an uncatalogued module is reported as an external
	// counterpart, but can never become a relationship in the catalog. It is
	// missing either its producer or its consumer; the two cases are listed
	// apart so each line needs no marker to be read.
	var noProducer, noConsumer []string
	for _, d := range dropped {
		line := fmt.Sprintf("%s:%s -> %s:%s [%s]",
			d.Consumer.GroupId, d.Consumer.ArtifactId,
			d.Producer.GroupId, d.Producer.ArtifactId, strings.Join(d.Topics, ", "))
		if d.Missing == d.Producer {
			noProducer = append(noProducer, line)
		} else {
			noConsumer = append(noConsumer, line)
		}
	}
	printProblems(out, "detected dependencies on modules without a catalog component (the producing side)", noProducer)
	printProblems(out, "detected dependencies of modules without a catalog component (the consuming side)", noConsumer)

	var withEndpoints, totalEndpoints int
	for _, oe := range obs {
		if n := len(oe.GetEndpoints()); n > 0 {
			withEndpoints++
			totalEndpoints += n
		}
	}
	fmt.Fprintf(out, "\nreporting %d endpoints of %d source components", totalEndpoints, withEndpoints)
	if empty := len(obs) - withEndpoints; empty > 0 {
		fmt.Fprintf(out, ", and clearing the observations of %d without endpoints", empty)
	}
	fmt.Fprintln(out)

	if *dryRun {
		for _, oe := range obs {
			if len(oe.GetEndpoints()) > 0 {
				fmt.Fprintf(out, "  %s: %s\n", oe.GetSource().GetName(), swcat.Summary(oe))
			}
		}
		fmt.Fprintf(out, "\ndry run: nothing sent to %s (omit -dry-run to upload)\n", *url)
		return nil
	}

	var failed []string
	for _, oe := range obs {
		if err := client.PostObservedEndpoints(oe); err != nil {
			slog.Debug("failed to post observed endpoints", "source", oe.GetSource().GetName(), "err", err)
			failed = append(failed, fmt.Sprintf("%s: %v", oe.GetSource().GetName(), err))
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
