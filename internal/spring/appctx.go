package spring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var applicationYMLPattern = regexp.MustCompile(`^application(-[^.]+)?\.ya?ml$`)

var bindingDestinationKey = regexp.MustCompile(`^spring\.cloud\.stream\.bindings\.(.+)\.destination$`)
var bindingNamePattern = regexp.MustCompile(`^(.+)-(in|out)-\d+$`)

var placeholderRe = regexp.MustCompile(`\${([^}]+)}`)
var importKeyPattern = regexp.MustCompile(`^spring\.config\.import(\[\d+\])?$`)

// BindingDirection indicates whether a stream binding is an input (consumer) or output (producer).
type BindingDirection string

const (
	BindingIn  BindingDirection = "in"
	BindingOut BindingDirection = "out"
)

// StreamBinding represents a Spring Cloud Stream binding with its destination and binder.
type StreamBinding struct {
	BindingName string           `json:"bindingName"` // full name, e.g. "yankeeDoodle-out-0"
	Direction   BindingDirection `json:"direction"`   // "in" or "out"
	Destination string           `json:"destination"` // topic/queue name
	BinderType  string           `json:"binderType"`  // binder technology, e.g. "solace", "kafka", "rabbit"
	// Request marks an output binding that sends request/reply requests: the
	// application depends on whoever consumes Destination (the replier), not the
	// other way round as for a plain producer.
	Request bool `json:"request,omitempty"`
}

// ImportResolver resolves a spring.config.import location (with the optional:/classpath:/
// file: prefixes already stripped, e.g. "application-imported.yml" or "config/shared.yml")
// to the file path holding it. It returns ok=false if the location cannot be resolved.
type ImportResolver func(location string) (path string, ok bool)

// ReadApplicationProperties reads a directory or file as Spring application properties YAML.
// For directories, all application[-*].yml files are read; profile-specific files whose
// suffix matches any of the excludeProfiles regexes are skipped.
//
// spring.config.import locations are resolved via resolve (which may be nil to skip
// imports entirely).
//
// It returns a mapping from flattened keys ("my.funny.property") to their values.
func ReadApplicationProperties(path string, resolve ImportResolver, excludeProfiles []*regexp.Regexp) (map[string]string, error) {
	result := make(map[string]string)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("spring properties: cannot stat %s: %w", path, err)
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("spring properties: cannot read directory %s: %w", path, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && applicationYMLPattern.MatchString(entry.Name()) && !excludedProfile(entry.Name(), excludeProfiles) {
				err := readAndMerge(filepath.Join(path, entry.Name()), result)
				if err != nil {
					slog.Warn("spring: skipping file", "file", entry.Name(), "err", err)
				}
			}
		}
	} else {
		if err := readAndMerge(path, result); err != nil {
			return nil, err
		}
	}

	if err := processImports(result, resolve); err != nil {
		return nil, err
	}

	resolvePlaceholders(result)

	return result, nil
}

// excludedProfile reports whether the profile suffix of the given application YAML filename
// matches any of the given exclude regexes.
// The profile suffix is the part between "application-" and the file extension,
// e.g. "dev" for "application-dev.yml" or "kafka-dev" for "application-kafka-dev.yml".
// Files without a profile suffix (i.e. "application.yml") are never excluded.
func excludedProfile(filename string, excludeProfiles []*regexp.Regexp) bool {
	m := applicationYMLPattern.FindStringSubmatch(filename)
	if m == nil || m[1] == "" {
		return false // base application.yml, never excluded
	}
	profile := m[1][1:] // strip leading "-"
	for _, re := range excludeProfiles {
		if re.MatchString(profile) {
			return true
		}
	}
	return false
}

var mavenPlaceholderRe = regexp.MustCompile(`@([^@\s]+)@`)

func readAndMerge(path string, result map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("spring properties: cannot read %s: %w", path, err)
	}

	// Pre-process to handle Maven placeholders like @project.name@ which are
	// invalid YAML if unquoted. Strip the @ delimiters; the inner content
	// (e.g. "project.name") is a valid YAML bare scalar and is harmless
	// whether the value was already quoted or not.
	data = mavenPlaceholderRe.ReplaceAll(data, []byte(`$1`))

	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var root any
		if err := dec.Decode(&root); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("spring properties: cannot parse %s: %w", path, err)
		}
		flattenValue(root, "", result)
	}
	return nil
}

func flattenValue(v any, prefix string, result map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenValue(child, key, result)
		}
	case []any:
		// Use the next unoccupied index rather than the sequential one, so that
		// list items from multiple files (e.g. spring.config.import lists, or
		// request/reply bindingMapping lists spread over profile files) are all
		// preserved instead of being silently dropped by first-wins.
		idx := 0
		for _, child := range val {
			for listIndexOccupied(result, fmt.Sprintf("%s[%d]", prefix, idx)) {
				idx++
			}
			flattenValue(child, fmt.Sprintf("%s[%d]", prefix, idx), result)
			idx++
		}
	case nil:
		// skip null values
	default:
		if _, exists := result[prefix]; !exists {
			result[prefix] = fmt.Sprintf("%v", val)
		}
	}
}

// listIndexOccupied reports whether the list item key (e.g. "a.b[2]") is already
// taken. A scalar item occupies the key itself; a map or list item only creates
// nested keys ("a.b[2].c", "a.b[2][0]"), which must count as well.
func listIndexOccupied(result map[string]string, key string) bool {
	if _, ok := result[key]; ok {
		return true
	}
	for k := range result {
		if strings.HasPrefix(k, key+".") || strings.HasPrefix(k, key+"[") {
			return true
		}
	}
	return false
}

// anImport is a parsed spring.config.import location.
type anImport struct {
	location string // resource location with prefixes stripped
	optional bool   // true if declared with the "optional:" prefix
}

func processImports(result map[string]string, resolve ImportResolver) error {
	if resolve == nil {
		return nil
	}
	imported := make(map[string]bool)

	for {
		var toImport []anImport
		for k, v := range result {
			if !importKeyPattern.MatchString(k) {
				continue
			}
			for p := range strings.SplitSeq(v, ",") {
				imp := parseImport(p)
				if imp.location != "" && !imported[imp.location] {
					toImport = append(toImport, imp)
					imported[imp.location] = true
				}
			}
		}

		if len(toImport) == 0 {
			break
		}

		for _, imp := range toImport {
			path, ok := resolve(imp.location)
			if !ok {
				if !imp.optional {
					slog.Warn("spring: import not found on classpath", "import", imp.location)
				}
				continue
			}
			if err := readAndMerge(path, result); err != nil {
				slog.Warn("spring: skipping import", "import", imp.location, "err", err)
			}
		}
	}
	return nil
}

// parseImport strips the optional:/classpath:/file: prefixes from a single
// spring.config.import location and reports whether it was marked optional.
func parseImport(p string) anImport {
	p = strings.TrimSpace(p)
	var optional bool
	if s, ok := strings.CutPrefix(p, "optional:"); ok {
		optional = true
		p = s
	}
	p = strings.TrimPrefix(p, "classpath:")
	p = strings.TrimPrefix(p, "file:")
	return anImport{location: strings.TrimSpace(p), optional: optional}
}

// normalizeKey converts a Spring property key to a normalized form for relaxed binding.
// It removes dashes and underscores and converts the key to lowercase.
func normalizeKey(key string) string {
	k := strings.ReplaceAll(key, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	return strings.ToLower(k)
}

// lookupRelaxed attempts to find a key in the properties map using Spring's relaxed binding rules.
func lookupRelaxed(props map[string]string, key string) (string, bool) {
	// Fast path: exact match
	if val, ok := props[key]; ok {
		return val, true
	}

	// Slow path: normalized match
	normKey := normalizeKey(key)
	// For better performance on repeated lookups, a normalized index could be built,
	// but mapping sizes are typically small enough that a linear scan is fine.
	for k, v := range props {
		if normalizeKey(k) == normKey {
			return v, true
		}
	}

	return "", false
}

// resolvePlaceholders iteratively resolves Spring property placeholders in the format ${key} or ${key:default}.
// It performs up to 10 passes to handle nested placeholders (e.g. ${prefix_${suffix}}).
func resolvePlaceholders(result map[string]string) {
	changed := true
	iterations := 0

	// Limit iterations to prevent infinite loops in cases of circular references
	for changed && iterations < 10 {
		changed = false
		for k, v := range result {
			newV := placeholderRe.ReplaceAllStringFunc(v, func(match string) string {
				// match is e.g. "${my.property:default}" -> key becomes "my.property:default"
				key := match[2 : len(match)-1]

				// Strip any default value (separated by ':') before looking up the key.
				if idx := strings.IndexByte(key, ':'); idx != -1 {
					key = key[:idx]
				}

				// Attempt to resolve the key, using relaxed binding to handle mismatching cases/dashes
				if resolved, ok := lookupRelaxed(result, key); ok {
					return resolved
				}

				// Not found: leave the placeholder unchanged rather than substituting the
				// default. A default is a compile-time fallback that rarely reflects the
				// real deployed value; keeping it unresolved lets it be treated as a
				// wildcard during topic matching instead of a misleading concrete value.
				return match
			})
			if newV != v {
				result[k] = newV
				changed = true
			}
		}
		iterations++
	}
}

// LogUnresolvedPlaceholders emits a debug log listing the property placeholders that
// remain unresolved in the given bindings' destinations (ignoring Request/Reply
// variables). It is a no-op unless debug logging is enabled.
func LogUnresolvedPlaceholders(ctxDir string, bindings []StreamBinding) {
	if !slog.Default().Enabled(context.TODO(), slog.LevelDebug) {
		return
	}
	unresolved := make(map[string]bool)
	for _, b := range bindings {
		for _, m := range placeholderRe.FindAllString(b.Destination, -1) {
			if !strings.Contains(m, "replyTopicWithWildcards") {
				unresolved[m] = true
			}
		}
	}
	if len(unresolved) > 0 {
		vars := make([]string, 0, len(unresolved))
		for v := range unresolved {
			vars = append(vars, v)
		}
		sort.Strings(vars)
		slog.Debug("spring: unresolved placeholders in bindings", "dir", ctxDir, "vars", vars)
	}
}

// StreamBindings extracts all Spring Cloud Stream bindings from a flattened properties map.
// It returns one StreamBinding per destination, splitting comma-separated consumer
// destinations and excluding request/reply reply-topics.
//
// Request/reply requesters are recognised (see requestReplyConfig): their output
// bindings are marked Request, and the literal topicPatterns of a mapping become
// additional request bindings, since such requests have no static destination.
func StreamBindings(props map[string]string) []StreamBinding {
	defaultBinder, _ := lookupRelaxed(props, "spring.cloud.stream.default-binder")
	binderTypeOf := func(bindingName string) string {
		// Resolve the binding's binder name (falling back to the default binder), then
		// map it to its technology type. The binder name is user-chosen and not kept.
		binder, ok := lookupRelaxed(props, "spring.cloud.stream.bindings."+bindingName+".binder")
		if !ok {
			binder = defaultBinder
		}
		if binder == "" {
			return ""
		}
		if t, ok := lookupRelaxed(props, "spring.cloud.stream.binders."+binder+".type"); ok {
			return t
		}
		// No explicit type configured: the binder name is the type itself.
		return binder
	}
	requesters, topicPatterns := requestReplyConfig(props)

	var bindings []StreamBinding
	for k, v := range props {
		m := bindingDestinationKey.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		bindingName := m[1]
		var direction BindingDirection
		var request bool
		if nm := bindingNamePattern.FindStringSubmatch(bindingName); nm != nil {
			direction = BindingDirection(nm[2])
			request = direction == BindingOut && requesters[nm[1]]
		}
		// Consumer bindings may subscribe to several comma-separated destinations;
		// emit one binding per destination so each is matched as its own topic.
		dests := []string{v}
		if direction == BindingIn {
			dests = splitDestinations(v)
		}
		binderType := binderTypeOf(bindingName)
		for _, d := range dests {
			// Reply-topics are runtime-generated per-request queues. Filter each
			// destination separately so real topics in the same list are retained.
			if strings.Contains(d, "${replyTopicWithWildcards|") {
				continue
			}
			bindings = append(bindings, StreamBinding{
				BindingName: bindingName,
				Destination: d,
				Direction:   direction,
				BinderType:  binderType,
				Request:     request,
			})
		}
	}

	// Requests sent to a destination chosen at runtime have no static destination;
	// the mapping's topicPatterns are the only record of where they go.
	for base, patterns := range topicPatterns {
		bindingName := base + "-out-0"
		binderType := binderTypeOf(bindingName)
		for _, p := range patterns {
			if isRegexPattern(p) {
				// Regexes (e.g. ".*topo/Foo/request") are not topics; matching them
				// would complicate topic matching for little gain. Skip them.
				slog.Debug("spring: skipping request/reply topic pattern with regex syntax",
					"binding", base, "pattern", p)
				continue
			}
			if slices.ContainsFunc(bindings, func(b StreamBinding) bool {
				return b.BindingName == bindingName && b.Destination == p
			}) {
				continue
			}
			bindings = append(bindings, StreamBinding{
				BindingName: bindingName,
				Destination: p,
				Direction:   BindingOut,
				BinderType:  binderType,
				Request:     true,
			})
		}
	}
	return bindings
}

// requestReplyMappingKey matches a normalized (see normalizeKey) request/reply
// binding-mapping entry: its binding name, or one of its topic patterns.
var requestReplyMappingKey = regexp.MustCompile(`^spring\.cloud\.stream\.requestreply\.bindingmapping\[(\d+)\]\.(binding|topicpatterns(?:\[\d+\])?)$`)

// requestReplyConfig finds the request/reply requesters configured in props. It
// returns the base names (binding name without "-in-N"/"-out-N") of requester
// functions, and the topic patterns declared per base name.
//
// A function is a requester if it appears in a
// spring.cloud.stream.requestreply.bindingMapping entry, or if its input binding
// subscribes to a ${replyTopicWithWildcards|...} reply topic.
func requestReplyConfig(props map[string]string) (requesters map[string]bool, topicPatterns map[string][]string) {
	type mapping struct {
		binding  string
		patterns []string
	}
	mappings := make(map[string]*mapping) // by list index
	requesters = make(map[string]bool)
	for k, v := range props {
		if m := bindingDestinationKey.FindStringSubmatch(k); m != nil {
			if nm := bindingNamePattern.FindStringSubmatch(m[1]); nm != nil &&
				nm[2] == string(BindingIn) && strings.Contains(v, "${replyTopicWithWildcards|") {
				requesters[nm[1]] = true
			}
			continue
		}
		m := requestReplyMappingKey.FindStringSubmatch(normalizeKey(k))
		if m == nil {
			continue
		}
		mp := mappings[m[1]]
		if mp == nil {
			mp = &mapping{}
			mappings[m[1]] = mp
		}
		if m[2] == "binding" {
			mp.binding = strings.TrimSpace(v)
		} else {
			// A scalar value may hold a comma-separated list, as Spring binds it.
			for p := range strings.SplitSeq(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					mp.patterns = append(mp.patterns, p)
				}
			}
		}
	}
	topicPatterns = make(map[string][]string)
	for _, mp := range mappings {
		if mp.binding == "" {
			continue
		}
		requesters[mp.binding] = true
		topicPatterns[mp.binding] = append(topicPatterns[mp.binding], mp.patterns...)
	}
	for base := range topicPatterns {
		sort.Strings(topicPatterns[base])
	}
	return requesters, topicPatterns
}

// regexSyntax matches characters that only occur in a topic pattern if it is a
// regular expression rather than a plain topic. '.' is deliberately absent: it is
// a legitimate topic character (and a regex '.' matches a literal one anyway).
var regexSyntax = regexp.MustCompile(`[*+?\\\[\](){}|^$]`)

// isRegexPattern reports whether a request/reply topic pattern uses regex syntax,
// ignoring unresolved ${...} placeholders.
func isRegexPattern(p string) bool {
	return regexSyntax.MatchString(placeholderRe.ReplaceAllString(p, ""))
}

// splitDestinations splits a comma-separated destination list, ignoring commas inside
// unresolved ${...} placeholders, and drops empty parts.
func splitDestinations(v string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '$' && i+1 < len(v) && v[i+1] == '{':
			depth++
			i++
		case v[i] == '}' && depth > 0:
			depth--
		case v[i] == ',' && depth == 0:
			parts = append(parts, v[start:i])
			start = i + 1
		}
	}
	parts = append(parts, v[start:])
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TopicSyntax describes how a binder technology structures its topic strings.
type TopicSyntax int

const (
	// SyntaxUnknown means the topic structure could not be determined; such bindings
	// are not matched.
	SyntaxUnknown TopicSyntax = iota
	// SyntaxSolace uses '/' level separators and the '*' (single level) and '>'
	// (multi level) wildcards.
	SyntaxSolace
	// SyntaxDotted uses '.' level separators and no wildcards (e.g. Kafka, TIBCO RV).
	SyntaxDotted
)

// separator returns the topic level separator for the syntax.
func (s TopicSyntax) separator() string {
	if s == SyntaxDotted {
		return "."
	}
	return "/"
}

// TopicSyntaxFor determines a binding's topic syntax from its binder technology,
// falling back to the shape of the destination when the binder type is unknown.
func TopicSyntaxFor(b StreamBinding) TopicSyntax {
	switch bt := strings.ToLower(b.BinderType); {
	case strings.Contains(bt, "solace"):
		return SyntaxSolace
	case strings.Contains(bt, "kafka"), strings.Contains(bt, "tibrv"), strings.Contains(bt, "tibco"):
		return SyntaxDotted
	}
	// Unknown binder: infer from the destination's structure.
	switch {
	case strings.Contains(b.Destination, "/"):
		return SyntaxSolace
	case strings.Contains(b.Destination, "."):
		return SyntaxDotted
	default:
		return SyntaxUnknown
	}
}

// MatchTopics compares a consumer topic (which may contain Solace wildcards '*' or '>')
// against a producer topic, using Solace topic syntax. Both topics may contain
// unresolved properties (e.g., "${...}"), which are treated equivalently to a
// single-level '*' wildcard.
// Bindings containing Spring Cloud Stream Request/Reply variables like
// ${replyTopicWithWildcards|...} are ignored and will never match.
func MatchTopics(consumerTopic, producerTopic string) bool {
	cLevels := TopicLevels(consumerTopic, SyntaxSolace)
	pLevels := TopicLevels(producerTopic, SyntaxSolace)
	if cLevels == nil || pLevels == nil {
		return false
	}
	return MatchLevels(cLevels, pLevels)
}

// TopicLevels normalizes a topic (placeholders to '*') and splits it into levels
// using the given syntax's separator.
// It returns nil if the topic contains a Request/Reply variable, or if the topic
// consists entirely of unresolved placeholders with no literal structure — in that
// case we have no information about topic shape and matching would produce false positives.
func TopicLevels(topic string, syntax TopicSyntax) []string {
	if strings.Contains(topic, "${replyTopicWithWildcards|") {
		return nil
	}
	if placeholderRe.ReplaceAllString(topic, "") == "" {
		return nil
	}
	normalized := placeholderRe.ReplaceAllString(topic, "*")
	return strings.Split(normalized, syntax.separator())
}

// MatchLevels compares consumer topic levels against producer topic levels.
func MatchLevels(cLevels, pLevels []string) bool {
	i, j := 0, 0
	for i < len(cLevels) && j < len(pLevels) {
		if cLevels[i] == ">" {
			// '>' matches remaining levels, but must be the last token in consumer topic
			return i == len(cLevels)-1
		}

		cMatch, _ := path.Match(cLevels[i], pLevels[j])
		pMatch, _ := path.Match(pLevels[j], cLevels[i])
		if !cMatch && !pMatch {
			return false
		}
		i++
		j++
	}

	// If we've exhausted consumer levels, producer levels must also be exhausted.
	if i == len(cLevels) {
		return j == len(pLevels)
	}

	// If we've exhausted producer levels but not consumer, check if the only remaining
	// consumer level is '>' (which can match an empty sequence of remaining levels).
	if j == len(pLevels) && i == len(cLevels)-1 && cLevels[i] == ">" {
		return true
	}

	return false
}
