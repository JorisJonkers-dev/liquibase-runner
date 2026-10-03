package changelog

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChangeSet is one changeset of an application's changelog, identified the way Liquibase
// identifies it in DATABASECHANGELOG.
type ChangeSet struct {
	ID               string
	Author           string
	File             string
	NonTransactional bool
	// Reruns is set for a changeset Liquibase runs again although the database holds it:
	// one marked runAlways or runOnChange is part of every release.
	Reruns bool
}

// ErrNotYAML is returned for a changelog the checks cannot read. The estate has one migration
// format, so a changelog in another one is refused instead of being passed unchecked.
var ErrNotYAML = errors.New("not a YAML changelog")

// ErrAmbiguous is returned for a changelog this reader and Liquibase could read differently.
// The checks answer for what Liquibase will run, so anything they cannot model is refused.
var ErrAmbiguous = errors.New("the changelog cannot be checked")

type document struct {
	Entries []yaml.Node `yaml:"databaseChangeLog"`
}

type changeSet struct {
	ID               yaml.Node `yaml:"id"`
	Author           yaml.Node `yaml:"author"`
	LogicalFilePath  string    `yaml:"logicalFilePath"`
	RunInTransaction *bool     `yaml:"runInTransaction"`
	RunAlways        bool      `yaml:"runAlways"`
	RunOnChange      bool      `yaml:"runOnChange"`
}

type include struct {
	File     string `yaml:"file"`
	Relative bool   `yaml:"relativeToChangelogFile"`
}

type includeAll struct {
	Path     string `yaml:"path"`
	Relative bool   `yaml:"relativeToChangelogFile"`
}

// The keys this reader acts on. A key that differs from one of them only by case is refused:
// whether Liquibase honours it is not something the checks may guess.
var (
	entryKeys     = []string{"changeSet", "include", "includeAll", "logicalFilePath"}                           //nolint:gochecknoglobals // a constant list.
	changeSetKeys = []string{"id", "author", "logicalFilePath", "runInTransaction", "runAlways", "runOnChange"} //nolint:gochecknoglobals // a constant list.
	includeKeys   = []string{"file", "path", "relativeToChangelogFile"}                                         //nolint:gochecknoglobals // a constant list.
)

// Read returns every changeset of the changelog at file, in the order Liquibase runs them,
// following include and includeAll.
func Read(root fs.FS, file string) ([]ChangeSet, error) {
	return read(root, path.Clean(file), map[string]bool{})
}

func read(root fs.FS, file string, seen map[string]bool) ([]ChangeSet, error) {
	if !isYAML(file) {
		return nil, fmt.Errorf("%s: %w", file, ErrNotYAML)
	}
	if seen[file] {
		return nil, fmt.Errorf("%s includes itself", file)
	}
	seen[file] = true
	defer delete(seen, file)

	raw, err := fs.ReadFile(root, file)
	if err != nil {
		return nil, fmt.Errorf("read changelog: %w", err)
	}
	var doc document
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}

	r := reader{root: root, file: file, logical: file, seen: seen}
	for i := range doc.Entries {
		if err := r.entry(&doc.Entries[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	return r.sets, nil
}

// reader walks one changelog file.
type reader struct {
	root    fs.FS
	file    string
	logical string
	seen    map[string]bool
	sets    []ChangeSet
}

// entry reads one item of databaseChangeLog: every key of it, in order, not only the first.
func (r *reader) entry(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: line %d is not a mapping", ErrAmbiguous, node.Line)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if err := plain(key, value, entryKeys); err != nil {
			return err
		}
		var err error
		switch key.Value {
		case "logicalFilePath":
			r.logical = value.Value
		case "changeSet":
			err = r.changeSet(value)
		case "include":
			err = r.include(value)
		case "includeAll":
			err = r.includeAll(value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *reader) changeSet(node *yaml.Node) error {
	if err := exactKeys(node, changeSetKeys); err != nil {
		return err
	}
	var c changeSet
	if err := node.Decode(&c); err != nil {
		return err
	}
	file := r.logical
	if c.LogicalFilePath != "" {
		file = c.LogicalFilePath
	}
	r.sets = append(r.sets, ChangeSet{
		ID:               c.ID.Value,
		Author:           c.Author.Value,
		File:             file,
		NonTransactional: c.RunInTransaction != nil && !*c.RunInTransaction,
		Reruns:           c.RunAlways || c.RunOnChange,
	})
	return nil
}

func (r *reader) include(node *yaml.Node) error {
	if err := exactKeys(node, includeKeys); err != nil {
		return err
	}
	var inc include
	if err := node.Decode(&inc); err != nil {
		return err
	}
	more, err := read(r.root, resolve(r.file, inc.File, inc.Relative), r.seen)
	r.sets = append(r.sets, more...)
	return err
}

// includeAll follows every file under the directory, in path order, which is Liquibase's
// order. A filter Liquibase applies is not modelled, so the check counts at least the
// changesets Liquibase runs.
func (r *reader) includeAll(node *yaml.Node) error {
	if err := exactKeys(node, includeKeys); err != nil {
		return err
	}
	var inc includeAll
	if err := node.Decode(&inc); err != nil {
		return err
	}
	dir := resolve(r.file, inc.Path, inc.Relative)

	var files []string
	err := fs.WalkDir(r.root, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read changelog directory: %w", err)
	}
	sort.Strings(files)

	for _, f := range files {
		more, err := read(r.root, f, r.seen)
		if err != nil {
			return err
		}
		r.sets = append(r.sets, more...)
	}
	return nil
}

// exactKeys refuses a mapping the reader and Liquibase could read differently.
func exactKeys(node *yaml.Node, known []string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: line %d is not a mapping", ErrAmbiguous, node.Line)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if err := plain(node.Content[i], node.Content[i+1], known); err != nil {
			return err
		}
	}
	return nil
}

// plain refuses one key of a mapping the reader acts on when it is an alias or a merge key, when
// it spells a known key in another case, or when a known key's value is an alias. Liquibase's parser
// resolves merges and aliases before Liquibase sees the mapping; this reader walks what is
// written, so a changeset or a runInTransaction brought in by either would go unseen.
func plain(key, value *yaml.Node, known []string) error {
	// An alias used as a key carries the anchor's name here, not the key Liquibase will see.
	if key.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: line %d has a key that is not written out", ErrAmbiguous, key.Line)
	}
	if key.Tag == "!!merge" || key.Value == "<<" {
		return fmt.Errorf("%w: line %d merges another mapping in", ErrAmbiguous, key.Line)
	}
	for _, k := range known {
		if key.Value != k && strings.EqualFold(key.Value, k) {
			return fmt.Errorf("%w: line %d spells %q as %q", ErrAmbiguous, key.Line, k, key.Value)
		}
		if key.Value == k && value.Kind == yaml.AliasNode {
			return fmt.Errorf("%w: line %d takes %q from an alias", ErrAmbiguous, key.Line, k)
		}
	}
	return nil
}

func resolve(from, target string, relative bool) string {
	if relative {
		return path.Join(path.Dir(from), target)
	}
	return path.Clean(target)
}

func isYAML(file string) bool {
	return strings.HasSuffix(file, ".yaml") || strings.HasSuffix(file, ".yml")
}
