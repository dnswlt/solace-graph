package commands

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dnswlt/solace-graph/internal/graph"
	"github.com/dnswlt/solace-graph/internal/report"
)

// defaultReportPath is the HTML report file written when -html is not given.
const defaultReportPath = "sgraph.html"

// Report builds a dependency graph from collected bindings in the input files
// and renders it as a self-contained HTML report.
func Report(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	htmlPath := fs.String("html", defaultReportPath, "path to write the HTML report")
	if err := fs.Parse(args); err != nil {
		return err
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		return fmt.Errorf("usage: report [-html <report.html>] <file_or_pattern> [<file_or_pattern>...]")
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

	var buf bytes.Buffer
	if err := report.Generate(&buf, nodes); err != nil {
		return fmt.Errorf("could not generate HTML report: %v", err)
	}
	if err := os.WriteFile(*htmlPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("could not write HTML report file: %v", err)
	}
	return nil
}
