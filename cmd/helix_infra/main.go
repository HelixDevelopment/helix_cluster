package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "helix_infra",
	Short: "Helix Cluster Infrastructure Manager",
	Long: `helix_infra manages the Helix Cluster infrastructure stack:
- PostgreSQL, Redis, etcd clusters
- NATS, Kafka, RabbitMQ messaging
- Prometheus, Grafana, Jaeger observability
- HashiCorp Vault secrets
- QEMU VM-based node simulation`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// errExit carries a process exit code from a command's core action up to
// main() without calling os.Exit inside the logic (which would be untestable).
type errExit struct {
	code int
}

func (e *errExit) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// finish translates an action's (exitCode, err) result into the single error
// value cobra's RunE expects. A real error is returned verbatim; a non-zero
// exit code with no error becomes an errExit sentinel that main() unwraps.
func finish(code int, err error) error {
	if err != nil {
		return err
	}
	if code != 0 {
		return &errExit{code: code}
	}
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		var ee *errExit
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
