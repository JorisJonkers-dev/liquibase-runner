package changelog

import (
	"errors"
	"fmt"
	"strings"
)

// ErrLintAllow is a lint-allow line the lint cannot honour: one with no rule or no reason.
var ErrLintAllow = errors.New("a lint-allow line names its rules and why: `lint-allow <rule>[,<rule>…]: <reason>`")

const lintAllow = "lint-allow "

// LintAllow is the rules a changeset's comment excuses it from, from each of its lines
// `lint-allow <rule>[,<rule>…]: <reason>` (deploy-kit ADR 0108). Whether a rule exists is for
// squawk to say.
func LintAllow(comment string) ([]string, error) {
	var rules []string
	for _, line := range strings.Split(comment, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, lintAllow)
		if !ok {
			continue
		}
		names, reason, found := strings.Cut(rest, ":")
		if !found || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("%w, not %q", ErrLintAllow, line)
		}
		for _, name := range strings.Split(names, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("%w, not %q", ErrLintAllow, line)
			}
			rules = append(rules, name)
		}
	}
	return rules, nil
}

// SQL is the statements Liquibase rendered for one changeset.
type SQL struct {
	// Key names the changeset as ChangeSet.Key does.
	Key string
	SQL string
}

const changesetHeader = "-- Changeset "

// SplitSQL cuts update-sql's output into each changeset's statements, in order. What Liquibase
// writes for its own bookkeeping, its lock and its DATABASECHANGELOG rows, is left out: it is the
// same for every changeset and takes no lock on an application's table.
func SplitSQL(rendered string) []SQL {
	var out []SQL
	for _, line := range strings.Split(rendered, "\n") {
		if key, ok := strings.CutPrefix(line, changesetHeader); ok {
			out = append(out, SQL{Key: strings.TrimSpace(key)})
			continue
		}
		if len(out) == 0 || strings.HasPrefix(line, "-- ") || strings.Contains(strings.ToLower(line), "databasechangelog") {
			continue
		}
		out[len(out)-1].SQL += line + "\n"
	}
	for i := range out {
		out[i].SQL = strings.TrimSpace(out[i].SQL)
	}
	return out
}
