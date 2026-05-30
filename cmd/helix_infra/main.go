package main

import (
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
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
