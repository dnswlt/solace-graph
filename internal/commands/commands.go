package commands

import (
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

// Collect extracts bindings from files matching patterns under root and writes them as JSON to out.
// It merges multiple files belonging to the same application (e.g. same pom.xml or folder).
func Collect(out io.Writer, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: collect <root> <pattern> [<pattern>...]")
	}

	root := args[0]
	patterns := make([]*regexp.Regexp, len(args)-1)
	for i, s := range args[1:] {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("invalid pattern %q: %v", s, err)
		}
		patterns[i] = re
	}

	result, err := spring.FindStreamBindings(root, patterns)
	if err != nil {
		return fmt.Errorf("FindStreamBindings: %v", err)
	}

	appMap := make(map[string]*graph.Application)
	var names []string

	for path, bindings := range result {
		name, version, discovery := findApplicationName(path)
		newApp := &graph.Application{
			Name:      name,
			Version:   version,
			Discovery: discovery,
			Files:     []string{path},
			Bindings:  bindings,
		}

		if app, ok := appMap[name]; !ok {
			appMap[name] = newApp
			names = append(names, name)
		} else {
			app.Merge(newApp)
		}
	}

	sort.Strings(names)
	var apps []graph.Application
	for _, name := range names {
		app := appMap[name]
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
		f, err := os.Create(*htmlPath)
		if err != nil {
			return fmt.Errorf("could not create HTML report file: %v", err)
		}
		defer f.Close()
		return report.Generate(f, nodes)
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

func findApplicationName(path string) (name string, version string, discovery string) {
	// If the file lives under src/main/resources, try to find a pom.xml in the module root.
	relPath := filepath.Join("src", "main", "resources")
	if idx := strings.LastIndex(path, relPath); idx != -1 {
		// Ensure it's either at the start or preceded by a path separator to avoid partial matches
		if idx == 0 || os.IsPathSeparator(path[idx-1]) {
			moduleRoot := path[:idx]
			pomPath := filepath.Join(moduleRoot, "pom.xml")
			if pom, err := maven.Load(pomPath); err == nil {
				if pom.ArtifactId != "" {
					return pom.ArtifactId, pom.GetVersion(), "pom.xml"
				}
			}
		}
	}

	// Fallback: use the parent folder name as the application name
	return filepath.Base(filepath.Dir(path)), "", "folder-name"
}
