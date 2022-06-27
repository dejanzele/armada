package cmd

import (
	"github.com/G-Research/armada/internal/testsuite"
	"github.com/spf13/cobra"
)

// versionCmd prints version info and exits.
func versionCmd(app *testsuite.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version.",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return initParams(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Version()
		},
	}
	return cmd
}
