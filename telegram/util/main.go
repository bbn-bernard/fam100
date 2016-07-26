package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "fam100",
		Short: "fam100 game",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("hello world")
		},
	}

	rootCmd.AddCommand(cmdLeave)

	if err := rootCmd.Execute(); err != nil {
		panic(err)
	}
}

var cmdLeave = &cobra.Command{
	Use:   "leave",
	Short: "leave channel specified by in input file",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("leave")
	},
}
