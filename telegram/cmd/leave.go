package cmd

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"github.com/yulrizka/bot"
)

var (
	inputFile string
	dryRun    bool
)

func init() {
	Leave.Flags().StringVar(&inputFile, "inputFile", "input.txt", "input file source contain channel id per line")
	Leave.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "don't actually leave, only print log")
}

var Leave = &cobra.Command{
	Use:   "leave",
	Short: "leave channel specified by in input file",
	Run: func(cmd *cobra.Command, args []string) {
		f, err := os.Open(inputFile)
		if err != nil {
			log.Fatal(err)
		}

		key := os.Getenv("TELEGRAM_KEY")
		if key == "" {
			log.Fatal("TELEGRAM_KEY can not be empty")
		}
		telegram := bot.NewTelegram(key)

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			chanID := scanner.Text()
			fmt.Printf("leaving: %s. ", chanID)
			if !dryRun {
				if err := telegram.Leave(chanID); err != nil {
					fmt.Printf("FAILED")
					log.Printf("ERROR: %s", err)
				} else {
					fmt.Printf("OK")
				}
			}
			fmt.Println()
		}

	},
}
