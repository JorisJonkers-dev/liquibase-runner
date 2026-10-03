// Package proof writes the Migration Proof an application's CI publishes beside its project
// file: per Application that moves its schema with a changelog, the serving revision its
// compatibility suite ran against and whether the release holds a non-transactional changeset.
package proof

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	apiVersion    = "proof.jorisjonkers.dev/v1"
	kind          = "MigrationProof"
	schemaVersion = "1.0.0"
)

var revisionPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Application is one Application's entry.
type Application struct {
	ID string `yaml:"id"`
	// TestedAgainst is absent on a first release, when nothing serves yet.
	TestedAgainst    string `yaml:"testedAgainst,omitempty"`
	NonTransactional bool   `yaml:"nonTransactional"`
}

// Document is the whole proof.
type Document struct {
	APIVersion    string        `yaml:"apiVersion"`
	Kind          string        `yaml:"kind"`
	SchemaVersion string        `yaml:"schemaVersion"`
	Applications  []Application `yaml:"applications"`
}

// Merge returns the proof existing with entry in it, replacing the Application's earlier entry.
// An empty existing starts a new proof. Entries are ordered by id, so a proof that says the same
// thing is the same bytes, and the fragment's digest does not move.
func Merge(existing []byte, entry Application) ([]byte, error) {
	if entry.ID == "" {
		return nil, errors.New("the proof entry names no application")
	}
	if entry.TestedAgainst != "" && !revisionPattern.MatchString(entry.TestedAgainst) {
		return nil, fmt.Errorf("testedAgainst %q is not a sha256 revision", entry.TestedAgainst)
	}

	doc := Document{APIVersion: apiVersion, Kind: kind, SchemaVersion: schemaVersion}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(existing))
		dec.KnownFields(true)
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("read the existing proof: %w", err)
		}
		if doc.APIVersion != apiVersion || doc.Kind != kind {
			return nil, fmt.Errorf("the existing document is %s %s, not a Migration Proof", doc.APIVersion, doc.Kind)
		}
	}

	kept := []Application{entry}
	for _, a := range doc.Applications {
		if a.ID != entry.ID {
			kept = append(kept, a)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
	doc.Applications = kept

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("write the proof: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("write the proof: %w", err)
	}
	return out.Bytes(), nil
}

// MarshalYAML quotes testedAgainst: every value carrying a colon is quoted in the documents the
// model reads.
func (a Application) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, value *yaml.Node) {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
	}
	add("id", &yaml.Node{Kind: yaml.ScalarNode, Value: a.ID})
	if a.TestedAgainst != "" {
		add("testedAgainst", &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Value: a.TestedAgainst})
	}
	add("nonTransactional", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(a.NonTransactional)})
	return node, nil
}
