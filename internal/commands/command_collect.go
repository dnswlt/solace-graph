package commands

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"

	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/spring"
)

// Collect extracts bindings from all Spring application contexts found under the given
// roots and writes them as JSON to out. Each Maven module (identified by its GAV)
// contributes one application; if two distinct modules declare the same GAV, the first
// wins and the collision is logged.
func Collect(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("collect", flag.ContinueOnError)
	var excludeProfileFlags multiFlag
	var excludeAppFlags multiFlag
	fs.Var(&excludeProfileFlags, "exclude-profile", "regex matched against profile suffixes to exclude (repeatable), e.g. -exclude-profile 'dev|test'")
	fs.Var(&excludeAppFlags, "exclude-app", "regex matched against application names to exclude (repeatable), e.g. -exclude-app 'test-.*'")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: collect [-exclude-profile <regex>]... [-exclude-app <regex>]... <root> [<root>...]")
	}

	excludeProfiles := make([]*regexp.Regexp, len(excludeProfileFlags))
	for i, s := range excludeProfileFlags {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("invalid -exclude-profile %q: %v", s, err)
		}
		excludeProfiles[i] = re
	}

	excludeApps := make([]*regexp.Regexp, len(excludeAppFlags))
	for i, s := range excludeAppFlags {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("invalid -exclude-app %q: %v", s, err)
		}
		excludeApps[i] = re
	}

	mods, err := maven.Scan(fs.Args())
	if err != nil {
		return fmt.Errorf("maven.Scan: %v", err)
	}

	appMap := make(map[string]*graph.Application)
	var keys []string

	for _, m := range mods.All {
		if m.ResourcesDir == "" {
			continue // aggregator/library module with no application context
		}
		if matchesAny(excludeApps, m.ArtifactId) {
			continue
		}

		resolve := func(location string) (string, bool) { return mods.ResolveResource(m, location) }
		props, err := spring.ReadApplicationProperties(m.ResourcesDir, resolve, excludeProfiles)
		if err != nil {
			return fmt.Errorf("reading application properties for %s: %v", m.Key(), err)
		}
		bindings := spring.StreamBindings(props)
		if len(bindings) == 0 {
			continue
		}
		spring.LogUnresolvedPlaceholders(m.ResourcesDir, bindings)

		if existing, ok := appMap[m.Key()]; ok {
			slog.Warn("collect: duplicate application GAV; keeping the first, skipping",
				"gav", m.Key(), "kept", existing.Files, "skipped", m.ResourcesDir)
			continue
		}
		appMap[m.Key()] = &graph.Application{
			GAV:      m.GAV,
			Files:    []string{m.ResourcesDir},
			Bindings: bindings,
		}
		keys = append(keys, m.Key())
	}

	sort.Strings(keys)
	var apps []graph.Application
	for _, key := range keys {
		app := appMap[key]
		app.Sort()
		apps = append(apps, *app)
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(apps)
}

func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
