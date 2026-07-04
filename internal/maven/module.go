package maven

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skipDirs are directory names that never contain source modules we care about
// and that would only slow down (or pollute) the scan.
var skipDirs = map[string]bool{
	".git":         true,
	"target":       true,
	"node_modules": true,
	".idea":        true,
}

// Module is a scanned Maven module: its coordinates and declared dependencies (from
// pom.xml), together with the YAML resource files found under src/main/resources.
type Module struct {
	Dir          string       // directory containing the module's pom.xml
	GAV                       // effective coordinates (group/version fall back to parent)
	Dependencies []Dependency // declared <dependencies>

	// ResourcesDir is the module's src/main/resources directory, or "" if absent.
	ResourcesDir string
	// ResourceFiles maps a resource-relative path (using '/' separators, e.g.
	// "config/shared.yml") to its file path, for every *.yml/*.yaml file under
	// ResourcesDir.
	ResourceFiles map[string]string
}

// Key returns the module's identity as "groupId:artifactId". Version is deliberately
// excluded so that dependency edges match regardless of version.
func (m *Module) Key() string {
	return m.GroupId + ":" + m.ArtifactId
}

// Modules is the result of scanning one or more roots for Maven modules.
type Modules struct {
	All   []*Module
	byKey map[string]*Module // "groupId:artifactId" -> module
}

// Scan walks each root recursively and, for every pom.xml found, loads the module
// (effective GAV with parent fallback, declared dependencies) and collects the YAML
// resource files under its src/main/resources directory.
func Scan(roots []string) (*Modules, error) {
	ms := &Modules{byKey: make(map[string]*Module)}
	seen := make(map[string]bool) // absolute pom paths already scanned (overlapping roots)

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if d.Name() != "pom.xml" {
				return nil
			}
			// Skip a pom.xml already scanned via another (overlapping) root, so the
			// same physical module is not counted twice.
			if abs, err := filepath.Abs(path); err == nil {
				if seen[abs] {
					return nil
				}
				seen[abs] = true
			}
			m, err := loadModule(path)
			if err != nil {
				return err
			}
			ms.add(m)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return ms, nil
}

func (ms *Modules) add(m *Module) {
	ms.All = append(ms.All, m)
	// First module wins if two distinct directories declare the same GAV.
	if _, ok := ms.byKey[m.Key()]; !ok {
		ms.byKey[m.Key()] = m
	}
}

// loadModule parses the pom.xml at pomPath and collects the module's YAML resources.
func loadModule(pomPath string) (*Module, error) {
	pom, err := Load(pomPath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(pomPath)
	m := &Module{
		Dir: dir,
		GAV: GAV{
			GroupId:    pom.GetGroupId(),
			ArtifactId: pom.ArtifactId,
			Version:    pom.GetVersion(),
		},
		Dependencies: pom.Dependencies,
	}

	resDir := filepath.Join(dir, "src", "main", "resources")
	files, err := collectResourceFiles(resDir)
	if err != nil {
		return nil, err
	}
	if files != nil {
		m.ResourcesDir = resDir
		m.ResourceFiles = files
	}
	return m, nil
}

// collectResourceFiles returns a map of resource-relative path -> absolute path for
// every YAML file under resDir. It returns nil (no error) if resDir does not exist,
// which is the normal case for aggregator or library modules.
func collectResourceFiles(resDir string) (map[string]string, error) {
	if info, err := os.Stat(resDir); err != nil || !info.IsDir() {
		return nil, nil
	}

	files := make(map[string]string)
	err := filepath.WalkDir(resDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		rel, err := filepath.Rel(resDir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = path
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	return files, nil
}

// Classpath returns m followed by the transitive closure of its dependency modules
// that are present in the scanned set, in breadth-first order. Self always comes
// first so that a module's own resources take precedence over its dependencies'.
func (ms *Modules) Classpath(m *Module) []*Module {
	var order []*Module
	seen := map[string]bool{m.Key(): true}
	order = append(order, m)

	for i := 0; i < len(order); i++ {
		for _, dep := range order[i].Dependencies {
			key := dep.GroupId + ":" + dep.ArtifactId
			if seen[key] {
				continue
			}
			seen[key] = true
			if dm, ok := ms.byKey[key]; ok {
				order = append(order, dm)
			}
		}
	}
	return order
}

// ResolveResource resolves a classpath resource location (e.g. "shared.yml" or
// "config/shared.yml") against m's classpath, returning the file path if found.
// Within each classpath module it matches first by exact resource-relative path,
// then by basename; modules are searched self-first, dependencies after.
func (ms *Modules) ResolveResource(m *Module, location string) (string, bool) {
	location = filepath.ToSlash(strings.TrimPrefix(location, "/"))
	base := filepath.Base(location)

	for _, cm := range ms.Classpath(m) {
		if p, ok := cm.ResourceFiles[location]; ok {
			return p, true
		}
		for rel, p := range cm.ResourceFiles {
			if filepath.Base(rel) == base {
				return p, true
			}
		}
	}
	return "", false
}
