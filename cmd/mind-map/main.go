package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mind-map",
	Short: "A wiki engine with MCP interface for AI agents",
	Long:  "mind-map is a wiki that stores pages as markdown files, indexes them with SQLite FTS5, and exposes everything via MCP over HTTP/SSE. AI agents and humans use the same protocol.",
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the mind-map server",
	Long:  "Starts the MCP server over HTTP/SSE and serves the web UI. Agents and browsers connect to the same endpoint.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		addr, _ := cmd.Flags().GetString("addr")
		fmt.Printf("Starting mind-map server on %s (wiki dir: %s)\n", addr, dir)
		// TODO: wire up wiki + MCP + web server
		return nil
	},
}

func init() {
	serveCmd.Flags().StringP("dir", "d", ".", "Path to the wiki directory")
	serveCmd.Flags().StringP("addr", "a", ":8080", "Address to listen on")
	rootCmd.AddCommand(serveCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
