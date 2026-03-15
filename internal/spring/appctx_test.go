package spring

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestReadApplicationProperties(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)

	props, err := ReadApplicationProperties(dir, nil, nil)
	if err != nil {
		t.Fatalf("ReadApplicationProperties: %v", err)
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		if !strings.HasPrefix(k, "spring.cloud.stream.bindings.") || !strings.HasSuffix(k, ".destination") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		fmt.Printf("%s = %s\n", k, props[k])
	}
}

func TestReadApplicationPropertiesMixedYAMLKeys(t *testing.T) {
	// A YAML key can itself contain dots, e.g. "nested.dots" under "topic.some".
	// flattenValue joins these with ".", producing "topic.some.nested.dots" —
	// the same key that fully-nested YAML would produce. Placeholder resolution
	// must find the value regardless of which format was used.
	content := []byte(`
topic:
  some:
    nested.dots: my_topic_value
spring:
  cloud:
    stream:
      bindings:
        myBinding-in-0:
          destination: ${topic.some.nested.dots}
`)
	f, err := os.CreateTemp(t.TempDir(), "application*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	f.Close()

	props, err := ReadApplicationProperties(f.Name(), nil, nil)
	if err != nil {
		t.Fatalf("ReadApplicationProperties: %v", err)
	}

	dest := props["spring.cloud.stream.bindings.myBinding-in-0.destination"]
	if dest != "my_topic_value" {
		t.Errorf("expected %q, got %q", "my_topic_value", dest)
	}
}

func TestReadApplicationPropertiesWithImports(t *testing.T) {
	// Create a temporary directory structure for testing:
	// root/
	//   app/
	//     application.yml
	//   config/
	//     application-imported.yml

	tempDir := t.TempDir()
	appDir := filepath.Join(tempDir, "app")
	configDir := filepath.Join(tempDir, "config")

	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	appYml := filepath.Join(appDir, "application.yml")
	importedYml := filepath.Join(configDir, "application-imported.yml")

	appContent := []byte(`
spring:
  config:
    import: "classpath:application-imported.yml"
  cloud:
    stream:
      bindings:
        myBinding-in-0:
          destination: ${PREFIX}_${topic.name:default_topic}_${application.topics.update-inbound-queuename}
`)
	if err := os.WriteFile(appYml, appContent, 0644); err != nil {
		t.Fatal(err)
	}

	importedContent := []byte(`
PREFIX: ${ENV_VAR}
topic:
  name: my_resolved_topic
  other: ${missing.property}
application:
  topics:
    updateInboundQueuename: my_resolved_relaxed_queue
`)
	if err := os.WriteFile(importedYml, importedContent, 0644); err != nil {
		t.Fatal(err)
	}

	fileIndex := map[string]string{
		"application.yml":          appYml,
		"application-imported.yml": importedYml,
	}

	props, err := ReadApplicationProperties(appYml, fileIndex, nil)
	if err != nil {
		t.Fatalf("ReadApplicationProperties: %v", err)
	}

	dest := props["spring.cloud.stream.bindings.myBinding-in-0.destination"]

	// Expectations:
	// ${PREFIX} -> ${ENV_VAR} (since ENV_VAR is unresolved)
	// ${topic.name:default_topic} -> my_resolved_topic
	// ${application.topics.update-inbound-queuename} -> my_resolved_relaxed_queue (via relaxed binding)
	// Result: ${ENV_VAR}_my_resolved_topic_my_resolved_relaxed_queue
	expected := "${ENV_VAR}_my_resolved_topic_my_resolved_relaxed_queue"

	if dest != expected {
		t.Errorf("Expected destination %q, got %q", expected, dest)
	}
}

func TestMatchTopics(t *testing.T) {
	tests := []struct {
		name     string
		consumer string
		producer string
		want     bool
	}{
		{
			name:     "Exact match",
			consumer: "a/b/c",
			producer: "a/b/c",
			want:     true,
		},
		{
			name:     "Mismatch",
			consumer: "a/b/c",
			producer: "a/x/c",
			want:     false,
		},
		{
			name:     "Consumer single wildcard",
			consumer: "a/*/c",
			producer: "a/b/c",
			want:     true,
		},
		{
			name:     "Producer single wildcard (e.g. from unresolved prop)",
			consumer: "a/b/c",
			producer: "a/*/c",
			want:     true,
		},
		{
			name:     "Consumer multi-level wildcard",
			consumer: "a/>",
			producer: "a/b/c/d",
			want:     true,
		},
		{
			name:     "Consumer multi-level wildcard exact match",
			consumer: "a/b/c/>",
			producer: "a/b/c",
			want:     true,
		},
		{
			name:     "Consumer unresolved property as wildcard",
			consumer: "a/${missing}/c",
			producer: "a/b/c",
			want:     true,
		},
		{
			name:     "Producer unresolved property as wildcard",
			consumer: "a/b/c",
			producer: "a/${missing}/c",
			want:     true,
		},
		{
			name:     "Multiple unresolved properties",
			consumer: "a/${c_missing}/${d_missing}",
			producer: "a/b/c",
			want:     true,
		},
		{
			name:     "Unresolved property prefixing wildcard",
			consumer: "${missing}/>",
			producer: "foo/bar/baz",
			want:     true,
		},
		{
			name:     "Consumer multi-level wildcard fail",
			consumer: "a/b/>",
			producer: "x/y/z",
			want:     false,
		},
		{
			name:     "Different lengths without wildcards",
			consumer: "a/b",
			producer: "a/b/c",
			want:     false,
		},
		{
			name:     "Multiple asterisks in one level",
			consumer: "a/*middle*/c",
			producer: "a/prefix_middle_suffix/c",
			want:     true,
		},
		{
			name:     "Both consumer and producer have unresolved properties",
			consumer: "a/${CONSUMER_MISSING}/c",
			producer: "a/${PRODUCER_MISSING}/c",
			want:     true,
		},
		{
			name:     "Both unresolved properties as suffixes",
			consumer: "a/prefix_${CONSUMER_MISSING}/c",
			producer: "a/prefix_${PRODUCER_MISSING}/c",
			want:     true, // Treated as `prefix_*` matching `prefix_*`, which trivially yields true under path.Match
		},
		{
			name:     "Unresolved properties with mismatches",
			consumer: "a/foo_${CONSUMER_MISSING}/c",
			producer: "a/bar_${PRODUCER_MISSING}/c",
			want:     false, // `foo_*` does not match `bar_*`
		},
		{
			name:     "Ignore consumer replyTopicWithWildcards",
			consumer: "${replyTopicWithWildcards|requestTagesFahrplanAbleitung|*}",
			producer: "some/producer/topic",
			want:     false,
		},
		{
			name:     "Ignore producer replyTopicWithWildcards",
			consumer: "some/consumer/topic",
			producer: "a/b/${replyTopicWithWildcards|uuid}",
			want:     false,
		},
		{
			name:     "Ignore both replyTopicWithWildcards",
			consumer: "${replyTopicWithWildcards|requestTaxi|*}",
			producer: "a/b/${replyTopicWithWildcards|uuid}",
			want:     false,
		},
		{
			name:     "Fully unresolved consumer does not match",
			consumer: "${unresolved.topic}",
			producer: "a/b/c",
			want:     false,
		},
		{
			name:     "Fully unresolved producer does not match",
			consumer: "a/b/c",
			producer: "${unresolved.topic}",
			want:     false,
		},
		{
			name:     "Multiple adjacent placeholders with no structure do not match",
			consumer: "${prefix}${suffix}",
			producer: "a/b/c",
			want:     false,
		},
		{
			name:     "Placeholder with literal structure still matches",
			consumer: "${env}/b/c",
			producer: "prod/b/c",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchTopics(tt.consumer, tt.producer); got != tt.want {
				t.Errorf("MatchTopics(%q, %q) = %v, want %v", tt.consumer, tt.producer, got, tt.want)
			}
		})
	}
}

func TestStreamBindingsBinderType(t *testing.T) {
	tests := []struct {
		name            string
		props           map[string]string
		wantBinder      string
		wantBinderType  string
	}{
		{
			name: "single-binder: name is the type",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
				"spring.cloud.stream.bindings.myBinding-in-0.binder":      "solace",
			},
			wantBinder:     "solace",
			wantBinderType: "solace",
		},
		{
			name: "multi-binder: named binder with explicit type",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
				"spring.cloud.stream.bindings.myBinding-in-0.binder":      "solace-prod",
				"spring.cloud.stream.binders.solace-prod.type":            "solace",
			},
			wantBinder:     "solace-prod",
			wantBinderType: "solace",
		},
		{
			name: "default binder falls back to type lookup",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
				"spring.cloud.stream.default-binder":                      "kafka-prod",
				"spring.cloud.stream.binders.kafka-prod.type":             "kafka",
			},
			wantBinder:     "kafka-prod",
			wantBinderType: "kafka",
		},
		{
			name: "no binder configured",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
			},
			wantBinder:     "",
			wantBinderType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := StreamBindings(tt.props)
			if len(bindings) != 1 {
				t.Fatalf("expected 1 binding, got %d", len(bindings))
			}
			b := bindings[0]
			if b.Binder != tt.wantBinder {
				t.Errorf("Binder: got %q, want %q", b.Binder, tt.wantBinder)
			}
			if b.BinderType != tt.wantBinderType {
				t.Errorf("BinderType: got %q, want %q", b.BinderType, tt.wantBinderType)
			}
		})
	}
}

func TestReadAndMergeMavenPlaceholders(t *testing.T) {
	content := []byte(`
app:
  name: @project.name@
  version: "@project.version@"
  description: This is @project.description@
  already_quoted: "@already.quoted@"
`)
	f, err := os.CreateTemp(t.TempDir(), "application*.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	f.Close()

	result := make(map[string]string)
	if err := readAndMerge(f.Name(), result); err != nil {
		t.Fatalf("readAndMerge failed: %v", err)
	}

	tests := []struct {
		key  string
		want string
	}{
		{"app.name", "project.name"},
		{"app.version", "project.version"},
		{"app.description", "This is project.description"},
		{"app.already_quoted", "already.quoted"},
	}

	for _, tt := range tests {
		if got := result[tt.key]; got != tt.want {
			t.Errorf("result[%q] = %q, want %q", tt.key, got, tt.want)
		}
	}
}
