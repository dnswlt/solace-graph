package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/dnswlt/solace-graph/internal/graph"
)

// multiFlag is a flag.Value that accumulates repeated string flags.
type multiFlag []string

func (f *multiFlag) String() string     { return strings.Join(*f, ", ") }
func (f *multiFlag) Set(s string) error { *f = append(*f, s); return nil }

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
