// Command eclass-mcp is a Model Context Protocol server for Open eClass, the
// open source learning platform used by Greek universities.
//
// It speaks MCP over stdio, so any MCP client (Claude Code, Claude Desktop,
// Cursor, VS Code, Codex, Gemini CLI, ...) can start it as a subprocess.
// Configuration comes from environment variables:
//
//	ECLASS_URL           eClass base URL (default https://eclass.uniwa.gr)
//	ECLASS_USERNAME      your eClass username (required)
//	ECLASS_PASSWORD      your eClass password (required)
//	ECLASS_DOWNLOAD_DIR  where eclass_download saves files (default ~/Downloads/eclass)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridden at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("eclass-mcp", version)
		return
	}

	// stdout belongs to the MCP protocol, so logs go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("eclass-mcp: ")
	log.SetFlags(0)

	user, pass := os.Getenv("ECLASS_USERNAME"), os.Getenv("ECLASS_PASSWORD")
	if user == "" || pass == "" {
		log.Fatal("ECLASS_USERNAME and ECLASS_PASSWORD must be set")
	}
	client, err := NewClient(envOr("ECLASS_URL", "https://eclass.uniwa.gr"), user, pass)
	if err != nil {
		log.Fatal(err)
	}

	downloadDir := os.Getenv("ECLASS_DOWNLOAD_DIR")
	if downloadDir == "" {
		home, _ := os.UserHomeDir()
		downloadDir = filepath.Join(home, "Downloads", "eclass")
	}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "eclass-mcp", Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)
	addTools(server, client, downloadDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
