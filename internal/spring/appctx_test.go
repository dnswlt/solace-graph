package spring

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

	// Resolve imports by basename against the two known files, mimicking a classpath
	// that contains both.
	fileIndex := map[string]string{
		"application.yml":          appYml,
		"application-imported.yml": importedYml,
	}
	resolve := func(location string) (string, bool) {
		p, ok := fileIndex[filepath.Base(location)]
		return p, ok
	}

	props, err := ReadApplicationProperties(appYml, resolve, nil)
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

func TestTopicSyntaxFor(t *testing.T) {
	tests := []struct {
		name       string
		binderType string
		dest       string
		want       TopicSyntax
	}{
		{"solace binder", "solace", "a/b/c", SyntaxSolace},
		{"kafka binder", "kafka", "a.b.c", SyntaxDotted},
		{"kafka binder, slash in topic still dotted", "kafka", "a/b", SyntaxDotted},
		{"tibrv binder", "tibrv", "a.b.c", SyntaxDotted},
		{"tibco binder", "tibco-rv", "a.b.c", SyntaxDotted},
		{"unknown binder, slash -> solace", "", "a/b/c", SyntaxSolace},
		{"unknown binder, dot -> dotted", "", "a.b.c", SyntaxDotted},
		{"unknown binder, no separator -> unknown", "", "abc", SyntaxUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := StreamBinding{BinderType: tt.binderType, Destination: tt.dest}
			if got := TopicSyntaxFor(b); got != tt.want {
				t.Errorf("TopicSyntaxFor(%+v) = %v, want %v", b, got, tt.want)
			}
		})
	}
}

func TestMatchDottedTopics(t *testing.T) {
	tests := []struct {
		name     string
		consumer string
		producer string
		want     bool
	}{
		{"exact match", "orders.created.v1", "orders.created.v1", true},
		{"mismatch", "orders.created.v1", "orders.updated.v1", false},
		{"single-word topic", "orders", "orders", true},
		{"different lengths", "orders.created", "orders.created.v1", false},
		{"unresolved placeholder acts as wildcard", "orders.${region}.v1", "orders.emea.v1", true},
		{"slash is not a separator", "orders/created", "orders/created", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := TopicLevels(tt.consumer, SyntaxDotted)
			p := TopicLevels(tt.producer, SyntaxDotted)
			if got := c != nil && p != nil && MatchLevels(c, p); got != tt.want {
				t.Errorf("dotted match(%q, %q) = %v, want %v", tt.consumer, tt.producer, got, tt.want)
			}
		})
	}
}

func TestStreamBindingsBinderType(t *testing.T) {
	tests := []struct {
		name           string
		props          map[string]string
		wantBinderType string
	}{
		{
			name: "single-binder: name is the type",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
				"spring.cloud.stream.bindings.myBinding-in-0.binder":      "solace",
			},
			wantBinderType: "solace",
		},
		{
			name: "multi-binder: named binder with explicit type",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
				"spring.cloud.stream.bindings.myBinding-in-0.binder":      "solace-prod",
				"spring.cloud.stream.binders.solace-prod.type":            "solace",
			},
			wantBinderType: "solace",
		},
		{
			name: "default binder falls back to type lookup",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
				"spring.cloud.stream.default-binder":                      "kafka-prod",
				"spring.cloud.stream.binders.kafka-prod.type":             "kafka",
			},
			wantBinderType: "kafka",
		},
		{
			name: "no binder configured",
			props: map[string]string{
				"spring.cloud.stream.bindings.myBinding-in-0.destination": "some/topic",
			},
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
			if b.BinderType != tt.wantBinderType {
				t.Errorf("BinderType: got %q, want %q", b.BinderType, tt.wantBinderType)
			}
		})
	}
}

func TestStreamBindingsCommaSeparatedDestinations(t *testing.T) {
	tests := []struct {
		name string
		key  string
		dest string
		want []string
	}{
		{
			name: "consumer: one binding per destination",
			key:  "spring.cloud.stream.bindings.myBinding-in-0.destination",
			dest: "a/topic/update, INTERNAL/a/topic/update",
			want: []string{"a/topic/update", "INTERNAL/a/topic/update"},
		},
		{
			name: "consumer: comma inside unresolved placeholder is kept",
			key:  "spring.cloud.stream.bindings.myBinding-in-0.destination",
			dest: "a/${X:b,c}/update,d/topic",
			want: []string{"a/${X:b,c}/update", "d/topic"},
		},
		{
			name: "consumer: reply topic after real topic is dropped independently",
			key:  "spring.cloud.stream.bindings.myBinding-in-0.destination",
			dest: "orders/created,${replyTopicWithWildcards|request|*}",
			want: []string{"orders/created"},
		},
		{
			name: "consumer: reply topic before real topic is dropped independently",
			key:  "spring.cloud.stream.bindings.myBinding-in-0.destination",
			dest: "${replyTopicWithWildcards|request|*}, orders/created",
			want: []string{"orders/created"},
		},
		{
			name: "consumer: standalone reply topic is dropped",
			key:  "spring.cloud.stream.bindings.myBinding-in-0.destination",
			dest: "${replyTopicWithWildcards|request|*}",
		},
		{
			name: "producer: reply topic is dropped",
			key:  "spring.cloud.stream.bindings.myBinding-out-0.destination",
			dest: "${replyTopicWithWildcards|request|*}",
		},
		{
			name: "producer: not split",
			key:  "spring.cloud.stream.bindings.myBinding-out-0.destination",
			dest: "a/topic,b/topic",
			want: []string{"a/topic,b/topic"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := StreamBindings(map[string]string{tt.key: tt.dest})
			var got []string
			for _, b := range bindings {
				got = append(got, b.Destination)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("destinations: got %q, want %q", got, tt.want)
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

func TestFlattenListOfMapsAcrossFiles(t *testing.T) {
	// Lists of maps from several files must be appended, not overwrite each other:
	// map items never occupy the plain "prefix[i]" key, only "prefix[i].field".
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("application-a.yml", `
spring.cloud.stream.requestreply.bindingMapping:
  - binding: one
  - binding: two
`)
	write("application-b.yml", `
spring.cloud.stream.requestreply.bindingMapping:
  - binding: three
`)
	props, err := ReadApplicationProperties(dir, nil, nil)
	if err != nil {
		t.Fatalf("ReadApplicationProperties: %v", err)
	}
	var got []string
	for i := range 3 {
		got = append(got, props[fmt.Sprintf("spring.cloud.stream.requestreply.bindingMapping[%d].binding", i)])
	}
	sort.Strings(got)
	if want := []string{"one", "three", "two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("bindings: got %q, want %q", got, want)
	}
}

func TestStreamBindingsRequestReply(t *testing.T) {
	const prefix = "spring.cloud.stream."
	props := map[string]string{
		prefix + "default-binder":      "solace",
		prefix + "binders.solace.type": "solace",
		// Requester declared in a binding mapping, with a static request topic.
		prefix + "bindings.reqA-out-0.destination":           "svc/a/request",
		prefix + "bindings.reqA-in-0.destination":            "${replyTopicWithWildcards|reqA|*}",
		prefix + "requestreply.bindingMapping[0].binding":    "reqA",
		prefix + "requestreply.bindingMapping[0].replyTopic": "svc/a/reply/${replyTopicWithWildcards|uuid}",
		// Requester recognised by its reply subscription only (no mapping).
		prefix + "bindings.reqB-out-0.destination": "svc/b/request",
		prefix + "bindings.reqB-in-0.destination":  "${replyTopicWithWildcards|reqB|*}",
		// Requester with runtime destinations, declared via topic patterns
		// (kebab-case spelling); the regex pattern is skipped.
		prefix + "bindings.reqC-out-0.binder":                        "solace",
		prefix + "requestreply.binding-mapping[1].binding":           "reqC",
		prefix + "requestreply.binding-mapping[1].topic-patterns[0]": "svc/${STAGE}/c1/request",
		prefix + "requestreply.binding-mapping[1].topic-patterns[1]": "svc/${STAGE}/c2/request",
		prefix + "requestreply.binding-mapping[1].topic-patterns[2]": ".*c3/request",
		// Plain producer and a replier's input: not requests.
		prefix + "bindings.publish-out-0.destination": "svc/events",
		prefix + "bindings.answerA-in-0.destination":  "svc/a/request",
	}

	type got struct {
		name, dest string
		dir        BindingDirection
		request    bool
		binder     string
	}
	var bs []got
	for _, b := range StreamBindings(props) {
		bs = append(bs, got{b.BindingName, b.Destination, b.Direction, b.Request, b.BinderType})
	}
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].name != bs[j].name {
			return bs[i].name < bs[j].name
		}
		return bs[i].dest < bs[j].dest
	})
	want := []got{
		{"answerA-in-0", "svc/a/request", BindingIn, false, "solace"},
		{"publish-out-0", "svc/events", BindingOut, false, "solace"},
		{"reqA-out-0", "svc/a/request", BindingOut, true, "solace"},
		{"reqB-out-0", "svc/b/request", BindingOut, true, "solace"},
		{"reqC-out-0", "svc/${STAGE}/c1/request", BindingOut, true, "solace"},
		{"reqC-out-0", "svc/${STAGE}/c2/request", BindingOut, true, "solace"},
	}
	if !reflect.DeepEqual(bs, want) {
		t.Errorf("bindings:\n got %v\nwant %v", bs, want)
	}
}

func TestIsRegexPattern(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"svc/${STAGE}/v1/foo/request", false},
		{"svc.v1.foo.request", false},
		{".*topo/Foo/request", true},
		{"svc/(a|b)/request", true},
		{"svc/[^/]+/request", true},
	}
	for _, tt := range tests {
		if got := isRegexPattern(tt.pattern); got != tt.want {
			t.Errorf("isRegexPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
		}
	}
}
