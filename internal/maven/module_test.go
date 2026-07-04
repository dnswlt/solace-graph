package maven

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes content to path, creating parent directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// pomXML builds a minimal pom.xml with the given artifactId and dependency artifactIds
// (all under groupId "com.example").
func pomXML(artifactId string, deps ...string) string {
	s := `<?xml version="1.0" encoding="UTF-8"?>
<project>
  <groupId>com.example</groupId>
  <artifactId>` + artifactId + `</artifactId>
  <version>1.0.0</version>
  <dependencies>
`
	for _, d := range deps {
		s += `    <dependency><groupId>com.example</groupId><artifactId>` + d + `</artifactId><version>1.0.0</version></dependency>
`
	}
	s += `  </dependencies>
</project>`
	return s
}

// TestScanAndResolve builds a small module tree:
//
//	app     depends on -> lib
//	lib     provides config/shared.yml
//	other   unrelated module, also has a shared.yml (must NOT satisfy app's import)
func TestScanAndResolve(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "app", "pom.xml"), pomXML("app", "lib"))
	writeFile(t, filepath.Join(root, "app", "src", "main", "resources", "application.yml"), "foo: bar\n")

	writeFile(t, filepath.Join(root, "lib", "pom.xml"), pomXML("lib"))
	writeFile(t, filepath.Join(root, "lib", "src", "main", "resources", "config", "shared.yml"), "shared: true\n")

	writeFile(t, filepath.Join(root, "other", "pom.xml"), pomXML("other"))
	writeFile(t, filepath.Join(root, "other", "src", "main", "resources", "shared.yml"), "other: true\n")

	mods, err := Scan([]string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(mods.All) != 3 {
		t.Fatalf("expected 3 modules, got %d", len(mods.All))
	}

	app := mods.byKey["com.example:app"]
	lib := mods.byKey["com.example:lib"]
	if app == nil || lib == nil {
		t.Fatalf("missing modules: app=%v lib=%v", app, lib)
	}
	if len(app.Dependencies) != 1 || app.Dependencies[0].ArtifactId != "lib" {
		t.Fatalf("app dependencies wrong: %+v", app.Dependencies)
	}
	if app.Version != "1.0.0" {
		t.Errorf("app version = %q, want 1.0.0", app.Version)
	}

	// app's classpath is [app, lib]; shared.yml resolves to lib's copy.
	cp := mods.Classpath(app)
	if len(cp) != 2 || cp[0] != app || cp[1] != lib {
		t.Fatalf("classpath = %v, want [app lib]", cp)
	}

	// Resolve by basename, and by exact resource-relative path.
	got, ok := mods.ResolveResource(app, "shared.yml")
	if !ok {
		t.Fatal("expected shared.yml to resolve via lib")
	}
	wantLib := filepath.Join("config", "shared.yml")
	if filepath.Base(filepath.Dir(got)) != "config" || filepath.Base(got) != "shared.yml" {
		t.Errorf("resolved %q, want it under lib's %q", got, wantLib)
	}

	if got, _ := mods.ResolveResource(app, "config/shared.yml"); filepath.Base(got) != "shared.yml" {
		t.Errorf("exact-path resolve failed, got %q", got)
	}

	// app does not depend on 'other', so 'other's resources are off the classpath.
	// The only shared.yml reachable from app is lib's (verified above); other's copy
	// must be reachable from other itself but not from app. Assert app never returns
	// other's file.
	other := mods.byKey["com.example:other"]
	otherShared := other.ResourceFiles["shared.yml"]
	if got, _ := mods.ResolveResource(app, "shared.yml"); got == otherShared {
		t.Errorf("app resolved shared.yml to other's copy %q; classpath leaked", otherShared)
	}
}
