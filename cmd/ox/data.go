package main

import "github.com/spf13/cobra"

var dataCmd = &cobra.Command{
	Use:   "data",
	Short: "Manage structured data in team context",
	Long:  "Upload and manage structured data from external sources in the team context repository.",
}

func init() {
	dataCmd.AddCommand(dataPutCmd)
	rootCmd.AddCommand(dataCmd)
}
