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

	"github.com/spf13/cobra"
)

// stretchCmd represents the stretch command
var stretchCmd = &cobra.Command{
	Use:   "stretch",
	Short: "Stretch adds a copy of the most recent task to the timesheet",
	Long: `Stretch creates a copy of the last entry on your timesheet
	with the current time, effectively 'stretching' it's total time.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("stretch called")
		client.Stretch()
	},
}

func init() {
	rootCmd.AddCommand(stretchCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// stretchCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// stretchCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}