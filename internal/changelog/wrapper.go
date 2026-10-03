// Package changelog writes the runner's own changelog around an application's, and reads an
// application's changelog for the checks its CI runs.
package changelog

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	// WrapperFile is the file every tag changeset is recorded under in DATABASECHANGELOG.
	// Renaming it orphans every tag row already written, so it never changes.
	WrapperFile = "liquibase-runner.yaml"
	// Author is the author of every tag changeset.
	Author = "liquibase-runner"
)

var tagPattern = regexp.MustCompile(`^[a-f0-9]{12}$`)

// ValidTag reports whether tag is an Application revision's first 12 hex digits.
func ValidTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("tag %q is not 12 lowercase hex digits", tag)
	}
	return nil
}

// Wrapper is the changelog the runner hands Liquibase: the application's changelog, then one
// changeset per tag, each tagging its own row. A revision that changes no schema still gets a
// row and a tag of its own this way, and a rollback that names the tag changesets written after
// its target removes their rows too.
func Wrapper(include string, tags []string) ([]byte, error) {
	entries := []map[string]any{{"include": map[string]any{"file": include}}}
	for _, tag := range tags {
		if err := ValidTag(tag); err != nil {
			return nil, err
		}
		entries = append(entries, map[string]any{"changeSet": map[string]any{
			"id":      tag,
			"author":  Author,
			"changes": []map[string]any{{"tagDatabase": map[string]any{"tag": tag}}},
		}})
	}
	out, err := yaml.Marshal(map[string]any{"databaseChangeLog": entries})
	if err != nil {
		return nil, fmt.Errorf("write the runner's changelog: %w", err)
	}
	return out, nil
}
