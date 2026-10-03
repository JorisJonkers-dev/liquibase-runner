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
}

// ErrNotYAML is returned for a changelog the checks cannot read. The estate has one migration
// format, so a changelog in another one is refused instead of being passed unchecked.
var ErrNotYAML = errors.New("not a YAML changelog")

type document struct {
	LogicalFilePath string  `yaml:"logicalFilePath"`
	Entries         []entry `yaml:"databaseChangeLog"`
}

type entry struct {
	LogicalFilePath *string     `yaml:"logicalFilePath"`
	ChangeSet       *changeSet  `yaml:"changeSet"`
	Include         *include    `yaml:"include"`
	IncludeAll      *includeAll `yaml:"includeAll"`
}

type changeSet struct {
	ID               yaml.Node `yaml:"id"`
	Author           yaml.Node `yaml:"author"`
	LogicalFilePath  string    `yaml:"logicalFilePath"`
	RunInTransaction *bool     `yaml:"runInTransaction"`
}

type include struct {
	File     string `yaml:"file"`
	Relative bool   `yaml:"relativeToChangelogFile"`
}

type includeAll struct {
	Path     string `yaml:"path"`
	Relative bool   `yaml:"relativeToChangelogFile"`
}

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

	logical := file
	var sets []ChangeSet
	for _, e := range doc.Entries {
		switch {
		case e.LogicalFilePath != nil:
			logical = *e.LogicalFilePath
		case e.ChangeSet != nil:
			sets = append(sets, e.ChangeSet.at(logical))
		case e.Include != nil:
			more, err := read(root, resolve(file, e.Include.File, e.Include.Relative), seen)
			if err != nil {
				return nil, err
			}
			sets = append(sets, more...)
		case e.IncludeAll != nil:
			more, err := readAll(root, resolve(file, e.IncludeAll.Path, e.IncludeAll.Relative), seen)
			if err != nil {
				return nil, err
			}
			sets = append(sets, more...)
		}
	}
	return sets, nil
}

func (c *changeSet) at(file string) ChangeSet {
	if c.LogicalFilePath != "" {
		file = c.LogicalFilePath
	}
	return ChangeSet{
		ID:               c.ID.Value,
		Author:           c.Author.Value,
		File:             file,
		NonTransactional: c.RunInTransaction != nil && !*c.RunInTransaction,
	}
}

// readAll follows includeAll: every file under dir, in path order, which is Liquibase's order.
func readAll(root fs.FS, dir string, seen map[string]bool) ([]ChangeSet, error) {
	var files []string
	err := fs.WalkDir(root, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read changelog directory: %w", err)
	}
	sort.Strings(files)

	var sets []ChangeSet
	for _, f := range files {
		more, err := read(root, f, seen)
		if err != nil {
			return nil, err
		}
		sets = append(sets, more...)
	}
	return sets, nil
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
