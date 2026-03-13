package maven

import (
	"encoding/xml"
	"os"
)

// GAV represents the Group, Artifact, and Version coordinates.
type GAV struct {
	GroupId    string `xml:"groupId"`
	ArtifactId string `xml:"artifactId"`
	Version    string `xml:"version"`
}

// POM represents a Maven Project Object Model.
type POM struct {
	XMLName xml.Name `xml:"project"`
	GAV
	Parent GAV `xml:"parent"`
}

// GetGroupId returns the effective GroupId of the project,
// falling back to the parent GroupId if not explicitly defined.
func (p *POM) GetGroupId() string {
	if p.GroupId != "" {
		return p.GroupId
	}
	return p.Parent.GroupId
}

// GetVersion returns the effective version of the project,
// falling back to the parent version if not explicitly defined.
func (p *POM) GetVersion() string {
	if p.Version != "" {
		return p.Version
	}
	return p.Parent.Version
}

// Load reads and decodes a pom.xml file from the specified path.
func Load(path string) (*POM, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var pom POM
	if err := xml.NewDecoder(f).Decode(&pom); err != nil {
		return nil, err
	}
	return &pom, nil
}
