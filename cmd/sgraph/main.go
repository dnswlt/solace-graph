// sgraph extracts Spring Cloud Stream bindings from application files
// and builds a dependency graph between applications.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/dnswlt/solace-graph/internal/commands"
)

func main() {
	verbose := flag.Bool("v", false, "enable debug logging")
	flag.Usage = printUsage
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(&cliHandler{w: os.Stderr, level: level}))

	if flag.NArg() < 1 {
		printUsage()
		os.Exit(1)
	}

	cmd := flag.Arg(0)
	args := flag.Args()[1:]

	var err error
	switch cmd {
	case "collect":
		err = commands.Collect(os.Stdout, args)
	case "report":
		err = commands.Report(os.Stdout, args)
	case "swcat":
		err = commands.Swcat(os.Stdout, args)
	default:
		slog.Error("unknown command", "cmd", cmd)
		os.Exit(1)
	}

	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [-v] <command> [arguments]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	fmt.Fprintf(os.Stderr, "  -v   enable debug logging\n")
	fmt.Fprintf(os.Stderr, "\nCommands:\n")
	fmt.Fprintf(os.Stderr, "  collect [-exclude-profile <regex>]... <root> [<root>...]   Extract bindings and map to applications\n")
	fmt.Fprintf(os.Stderr, "  report [-html <report.html>] <file> [<file>...]             Render an HTML dependency report from collected bindings\n")
	fmt.Fprintf(os.Stderr, "  swcat [-url <swcat-url>] [-post] <file> [<file>...]          Report observed dependencies between matched components to swcat\n")
}
