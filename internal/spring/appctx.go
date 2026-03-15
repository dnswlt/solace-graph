package spring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
	BindingName string           // full name, e.g. "yankeeDoodle-out-0"
	Direction   BindingDirection // "in" or "out"
	Destination string           // topic/queue name
	Binder      string           // binder name, e.g. "kafka" or "solace"
	BinderType  string           // binder type, e.g. "solace", "kafka", "rabbit"; equals Binder if not separately configured
}

// ReadApplicationProperties reads a directory or file as Spring application properties YAML.
// For directories, all application[-*].yml files are read; profile-specific files whose
// suffix matches any of the excludeProfiles regexes are skipped.
//
// It returns a mapping from flattened keys ("my.funny.property") to their values.
func ReadApplicationProperties(path string, fileIndex map[string]string, excludeProfiles []*regexp.Regexp) (map[string]string, error) {
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

	if err := processImports(result, fileIndex); err != nil {
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
		// list items from multiple files (e.g. spring.config.import lists) are
		// all preserved instead of being silently dropped by first-wins.
		idx := 0
		for _, child := range val {
			for {
				if _, exists := result[fmt.Sprintf("%s[%d]", prefix, idx)]; !exists {
					break
				}
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

func processImports(result map[string]string, fileIndex map[string]string) error {
	imported := make(map[string]bool)

	for {
		var toImport []string
		for k, v := range result {
			if importKeyPattern.MatchString(k) {
				for p := range strings.SplitSeq(v, ",") {
					p = strings.TrimSpace(p)
					p = strings.TrimPrefix(p, "optional:")
					p = strings.TrimPrefix(p, "classpath:")
					p = strings.TrimPrefix(p, "file:")

					if p != "" && !imported[p] {
						toImport = append(toImport, p)
						imported[p] = true
					}
				}
			}
		}

		if len(toImport) == 0 {
			break
		}

		for _, imp := range toImport {
			basename := filepath.Base(imp)
			if path, ok := fileIndex[basename]; ok {
				if err := readAndMerge(path, result); err != nil {
					slog.Warn("spring: skipping import", "import", imp, "err", err)
				}
			} else {
				slog.Warn("spring: import not found in file index", "import", imp)
			}
		}
	}
	return nil
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

				var defaultVal string
				hasDefault := false

				// Handle default values separated by ':'
				if idx := strings.IndexByte(key, ':'); idx != -1 {
					defaultVal = key[idx+1:]
					key = key[:idx]
					hasDefault = true
				}

				// Attempt to resolve the key, using relaxed binding to handle mismatching cases/dashes
				if resolved, ok := lookupRelaxed(result, key); ok {
					return resolved
				}

				// If not found in properties, use default if provided
				if hasDefault {
					return defaultVal
				}

				// If no match and no default, leave the placeholder unchanged (might be an ENV var)
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

// findRepoRoots returns the paths of all git repositories under the given roots,
// sorted longest-first so that nested repos match before their parent.
func findRepoRoots(roots []string) ([]string, error) {
	var repoRoots []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == ".git" {
				repoRoots = append(repoRoots, filepath.Dir(path))
				return fs.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(repoRoots, func(i, j int) bool {
		return len(repoRoots[i]) > len(repoRoots[j])
	})
	return repoRoots, nil
}

// repoRootFor returns the deepest repo root that is an ancestor of fpath,
// or an empty string if fpath is not inside any known repo.
func repoRootFor(fpath string, repoRoots []string) string {
	for _, r := range repoRoots {
		if strings.HasPrefix(fpath, r+string(filepath.Separator)) || fpath == r {
			return r
		}
	}
	return ""
}

func logUnresolvedPlaceholders(ctxDir string, bindings []StreamBinding) {
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

// FindStreamBindings walks each of the given roots recursively and finds all Spring
// application contexts. A context is usually a 'src/main/resources' directory.
// For each context, it reads all application*.yml files together, allowing for
// placeholder resolution across files.
// It returns a map from context directory path to its non-empty list of stream bindings.
func FindStreamBindings(roots []string, excludeProfiles []*regexp.Regexp) (map[string][]StreamBinding, error) {
	repoRoots, err := findRepoRoots(roots)
	if err != nil {
		return nil, err
	}

	// fileIndex maps repo root -> (YAML basename -> full path) to resolve
	// spring.config.import directives. Scoping per repo prevents files from one
	// service from satisfying imports in another unrelated service.
	fileIndex := make(map[string]map[string]string)
	contextDirs := make(map[string]bool)

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		// Always update fileIndex for YAML files (needed for spring.config.import).
		if ext := filepath.Ext(path); ext == ".yml" || ext == ".yaml" {
			repo := repoRootFor(path, repoRoots)
			if fileIndex[repo] == nil {
				fileIndex[repo] = make(map[string]string)
			}
			fileIndex[repo][filepath.Base(path)] = path
		}

		// Only care about Spring configuration files.
		if !applicationYMLPattern.MatchString(d.Name()) {
			return nil
		}

		// Only include contexts under src/main/resources; skip src/test/resources and others.
		resPath := filepath.Join("src", "main", "resources")
		idx := strings.LastIndex(path, resPath)
		if idx == -1 {
			return nil
		}
		contextDirs[path[:idx+len(resPath)]] = true
		return nil
	}

	for _, root := range roots {
		if err := filepath.WalkDir(root, walkFn); err != nil {
			return nil, err
		}
	}

	// Process each identified context.
	result := make(map[string][]StreamBinding)
	var sortedContexts []string
	for cd := range contextDirs {
		sortedContexts = append(sortedContexts, cd)
	}
	sort.Strings(sortedContexts)

	for _, ctxDir := range sortedContexts {
		slog.Debug("spring: processing context", "dir", ctxDir)
		repo := repoRootFor(ctxDir, repoRoots)
		props, err := ReadApplicationProperties(ctxDir, fileIndex[repo], excludeProfiles)
		if err != nil {
			slog.Warn("spring: could not read application properties", "dir", ctxDir, "err", err)
			continue
		}
		if bindings := StreamBindings(props); len(bindings) > 0 {
			slog.Info("spring: found bindings", "count", len(bindings), "dir", ctxDir)
			logUnresolvedPlaceholders(ctxDir, bindings)
			result[ctxDir] = bindings
		}
	}

	return result, nil
}

// StreamBindings extracts all Spring Cloud Stream bindings from a flattened properties map.
// It returns one StreamBinding per binding that has a destination property.
func StreamBindings(props map[string]string) []StreamBinding {
	defaultBinder, _ := lookupRelaxed(props, "spring.cloud.stream.default-binder")

	var bindings []StreamBinding
	for k, v := range props {
		m := bindingDestinationKey.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		bindingName := m[1]
		b := StreamBinding{
			BindingName: bindingName,
			Destination: v,
		}
		if nm := bindingNamePattern.FindStringSubmatch(bindingName); nm != nil {
			b.Direction = BindingDirection(nm[2])
		}
		if binder, ok := lookupRelaxed(props, "spring.cloud.stream.bindings."+bindingName+".binder"); ok {
			b.Binder = binder
		} else {
			b.Binder = defaultBinder
		}
		if b.Binder != "" {
			if t, ok := lookupRelaxed(props, "spring.cloud.stream.binders."+b.Binder+".type"); ok {
				b.BinderType = t
			} else {
				// No explicit type configured: the binder name is the type itself.
				b.BinderType = b.Binder
			}
		}
		bindings = append(bindings, b)
	}
	return bindings
}

// MatchTopics compares a consumer topic (which may contain Solace wildcards '*' or '>')
// against a producer topic. Both topics may contain unresolved properties (e.g., "${...}"),
// which are treated equivalently to a single-level '*' wildcard.
// Bindings containing Spring Cloud Stream Request/Reply variables like
// ${replyTopicWithWildcards|...} are ignored and will never match.
func MatchTopics(consumerTopic, producerTopic string) bool {
	cLevels := TopicLevels(consumerTopic)
	pLevels := TopicLevels(producerTopic)
	if cLevels == nil || pLevels == nil {
		return false
	}
	return MatchLevels(cLevels, pLevels)
}

// TopicLevels normalizes a topic (placeholders to '*') and splits it into levels.
// It returns nil if the topic contains a Request/Reply variable, or if the topic
// consists entirely of unresolved placeholders with no literal structure — in that
// case we have no information about topic shape and matching would produce false positives.
func TopicLevels(topic string) []string {
	if strings.Contains(topic, "${replyTopicWithWildcards|") {
		return nil
	}
	if placeholderRe.ReplaceAllString(topic, "") == "" {
		return nil
	}
	normalized := placeholderRe.ReplaceAllString(topic, "*")
	return strings.Split(normalized, "/")
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
