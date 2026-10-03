package runner

import (
	"context"
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
