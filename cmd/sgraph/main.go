// sgraph walks a directory tree and extracts Spring Cloud Stream bindings
// from files whose full paths match any of the given regular expressions,
// then outputs the result as JSON.
//
// Usage:
//
//	sgraph <command> [arguments]
//
// Commands:
//
//	collect: output the bindings found in each file, mapped to an application name
//	graph:   output the dependency graph between applications from collected bindings
//
// Examples:
//
//	sgraph collect /repos '.*/src/main/resources/application.*\.yml' > bindings.json
//	sgraph graph -input bindings.json
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
	fmt.Fprintf(os.Stderr, "  collect <root> <pattern> [<pattern>...]   Extract bindings and map to applications\n")
	fmt.Fprintf(os.Stderr, "  graph -input <file>                       Build dependency graph from collected bindings\n")
}
