// Copyright © 2019 David McPike
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Server starts the hotkey-listener and GUI pop-up",
	Long: `Server starts the primary functionality of omw.  It:
	- Creates a server on port 38999 that communicates with the
	headless Chrome window and provides our HTML/JS GUI

	- Listens for the global hotkey <LEFT_SHIFT>+<RIGHT_SHIFT> that
	triggers the GUI`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("server called")
		if len(args) > 0 {
			fmt.Fprintf(os.Stderr, "Unused arguments provided after server command\n")
			os.Exit(1)
		}
		return client.Run(args)
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// serverCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// serverCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
