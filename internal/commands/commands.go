package commands

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/maven"
	"github.com/dnswlt/solace-graph/internal/report"
	"github.com/dnswlt/solace-graph/internal/spring"
)

// multiFlag is a flag.Value that accumulates repeated string flags.
type multiFlag []string

func (f *multiFlag) String() string     { return strings.Join(*f, ", ") }
func (f *multiFlag) Set(s string) error { *f = append(*f, s); return nil }

// Collect extracts bindings from all Spring application contexts found under the given
// roots and writes them as JSON to out. Each Maven module (identified by its GAV)
// contributes one application; modules sharing a GAV are merged.
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

		newApp := &graph.Application{
			GAV:      m.GAV,
			Files:    []string{m.ResourcesDir},
			Bindings: bindings,
		}

		if app, ok := appMap[m.Key()]; !ok {
			appMap[m.Key()] = newApp
			keys = append(keys, m.Key())
		} else {
			app.Merge(newApp)
		}
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

// Graph builds a dependency graph from collected bindings in the input files and writes it as JSON to out.
func Graph(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	htmlPath := fs.String("html", "", "path to output the HTML report")
	if err := fs.Parse(args); err != nil {
		return err
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		return fmt.Errorf("usage: graph [-html <report.html>] <file_or_pattern> [<file_or_pattern>...]")
	}

	var allPaths []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			// If it's not a valid glob pattern, treat it as a literal path.
			allPaths = append(allPaths, p)
			continue
		}
		if len(matches) == 0 {
			// If no files match the pattern, treat it as a literal path (might be a missing file).
			allPaths = append(allPaths, p)
		} else {
			allPaths = append(allPaths, matches...)
		}
	}

	var allApps []graph.Application
	for _, path := range allPaths {
		apps, err := readApplications(path)
		if err != nil {
			return err
		}
		allApps = append(allApps, apps...)
	}

	nodes := graph.Build(allApps)

	if *htmlPath != "" {
		var buf bytes.Buffer
		if err := report.Generate(&buf, nodes); err != nil {
			return fmt.Errorf("could not generate HTML report: %v", err)
		}
		if err := os.WriteFile(*htmlPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("could not write HTML report file: %v", err)
		}
		return nil
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(nodes)
}

func readApplications(path string) ([]graph.Application, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open input file %q: %v", path, err)
	}
	defer f.Close()

	var apps []graph.Application
	if err := json.NewDecoder(f).Decode(&apps); err != nil {
		return nil, fmt.Errorf("could not decode input file %q: %v", path, err)
	}
	return apps, nil
}

func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
