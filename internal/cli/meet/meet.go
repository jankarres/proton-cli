// Package meet is the `proton meet` tree: the Proton Meet meetings and rooms an
// account hosts, each one joined by a link.
package meet

import (
	"github.com/spf13/cobra"
)

func New() *cobra.Command {
	c := &cobra.Command{
		Use:   "meet",
		Short: "Video meetings with Proton Meet",
	}
	c.AddCommand(meetingsCmd(), roomsCmd())
	return c
}
