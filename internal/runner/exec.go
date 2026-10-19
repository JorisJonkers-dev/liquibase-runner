package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Exec is the Liquibase the image carries, at binary. The login travels in the environment, so
// it is in no argument list a process listing shows.
func Exec(binary string, stdout, stderr io.Writer) Liquibase {
	return func(ctx context.Context, db Database, searchPath []string, args ...string) error {
		// The global option goes before the command, the command's own options after it.
		argv := append([]string{"--search-path=" + strings.Join(searchPath, ",")}, args...)
		cmd := exec.CommandContext(ctx, binary, argv...) //nolint:gosec // the image's own Liquibase, with arguments the runner built.
		cmd.Stdout, cmd.Stderr = stdout, stderr
		cmd.Env = append(os.Environ(),
			"LIQUIBASE_COMMAND_URL="+db.URL(),
			"LIQUIBASE_COMMAND_USERNAME="+db.Username,
			"LIQUIBASE_COMMAND_PASSWORD="+db.Password,
		)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("liquibase %s: %w", commandOf(args), err)
		}
		return nil
	}
}

// commandOf names the Liquibase command among its options, for the error.
func commandOf(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			return a
		}
	}
	return ""
}

// SquawkExec is the squawk the image carries, at binary. It exits 1 with its findings on stdout,
// and anything else it reports, such as a rule it does not know, is an error.
func SquawkExec(binary string) Squawk {
	return func(ctx context.Context, file string, exclude []string, inTransaction bool) (string, error) {
		argv := []string{"--reporter=gcc", "--exclude=" + strings.Join(exclude, ",")}
		if inTransaction {
			argv = append(argv, "--assume-in-transaction")
		}
		var out, errs strings.Builder
		cmd := exec.CommandContext(ctx, binary, append(argv, file)...) //nolint:gosec // the image's own squawk, with arguments the runner built.
		cmd.Stdout, cmd.Stderr = &out, &errs
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return "", nil
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			return out.String(), nil
		default:
			return "", fmt.Errorf("squawk: %w: %s", err, strings.TrimSpace(errs.String()))
		}
	}
}
