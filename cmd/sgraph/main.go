// sgraph extracts Spring Cloud Stream bindings from application files
// and builds a dependency graph between applications.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/dnswlt/solace-graph/internal/commands"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "collect":
		err = commands.Collect(os.Stdout, args)
	case "graph":
		err = commands.Graph(os.Stdout, args)
	default:
		log.Fatalf("unknown command %q", cmd)
	}

	if err != nil {
		log.Fatal(err)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <command> [arguments]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nCommands:\n")
	fmt.Fprintf(os.Stderr, "  collect [-exclude-profile <regex>]... <root> [<root>...]   Extract bindings and map to applications\n")
	fmt.Fprintf(os.Stderr, "  graph [-html <report.html>] <file> [<file>...]              Build dependency graph from collected bindings\n")
}
