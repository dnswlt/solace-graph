package maven

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	content := `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
    <parent>
        <groupId>com.example.parent</groupId>
        <artifactId>my-parent</artifactId>
        <version>1.2.3</version>
    </parent>
    <artifactId>my-artifact</artifactId>
</project>`

	tmpDir := t.TempDir()
	pomPath := filepath.Join(tmpDir, "pom.xml")
	if err := os.WriteFile(pomPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp pom: %v", err)
	}

	pom, err := Load(pomPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if pom.ArtifactId != "my-artifact" {
		t.Errorf("expected ArtifactId 'my-artifact', got %q", pom.ArtifactId)
	}

	if g := pom.GetGroupId(); g != "com.example.parent" {
		t.Errorf("expected GroupId 'com.example.parent' (from parent), got %q", g)
	}

	if v := pom.GetVersion(); v != "1.2.3" {
		t.Errorf("expected Version '1.2.3' (from parent), got %q", v)
	}
}

func TestLoadExplicit(t *testing.T) {
	content := `<?xml version="1.0" encoding="UTF-8"?>
<project>
    <groupId>com.example.explicit</groupId>
    <artifactId>my-artifact</artifactId>
    <version>2.0.0</version>
</project>`

	tmpDir := t.TempDir()
	pomPath := filepath.Join(tmpDir, "pom.xml")
	if err := os.WriteFile(pomPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp pom: %v", err)
	}

	pom, err := Load(pomPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if g := pom.GetGroupId(); g != "com.example.explicit" {
		t.Errorf("expected GroupId 'com.example.explicit', got %q", g)
	}

	if v := pom.GetVersion(); v != "2.0.0" {
		t.Errorf("expected Version '2.0.0', got %q", v)
	}
}
