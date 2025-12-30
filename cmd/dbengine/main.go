package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/dsandor/flatstor/dbengine/pkg/api"
	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
	"github.com/dsandor/flatstor/dbengine/pkg/engine"
)

const version = "1.0.0"

func main() {
	// Parse command line flags
	dataPath := flag.String("data", "./data", "Path to data directory")
	duckDBPath := flag.String("duckdb", "", "Path to DuckDB file (empty for in-memory)")
	queryStr := flag.String("query", "", "Execute a single query and exit")
	noLoad := flag.Bool("no-load", false, "Skip auto-loading tables on startup")
	apiMode := flag.Bool("api", false, "Run as HTTP API server")
	apiAddr := flag.String("api-addr", ":8080", "API server listen address")
	apiUser := flag.String("api-user", "", "API server username")
	apiPass := flag.String("api-pass", "", "API server password")
	showVersion := flag.Bool("version", false, "Show version and exit")
	help := flag.Bool("help", false, "Show help")
	flag.BoolVar(help, "h", false, "Show help")

	flag.Parse()

	if *help {
		printUsage()
		os.Exit(0)
	}

	if *showVersion {
		fmt.Printf("dbengine version %s\n", version)
		os.Exit(0)
	}

	// Initialize engine
	eng, err := engine.New(engine.Config{
		DataPath:   *dataPath,
		DuckDBPath: *duckDBPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	ctx := context.Background()

	// Auto-load tables unless --no-load is specified
	if !*noLoad && !*apiMode {
		if tables, err := eng.ListTables(ctx); err == nil && len(tables) > 0 {
			fmt.Printf("Loading %d tables...\n", len(tables))
			for _, table := range tables {
				start := time.Now()
				rebuilt, err := eng.SmartLoad(ctx, table, func(loaded, total int) {
					fmt.Printf("\r  Loading %s: %d/%d rows", table, loaded, total)
				})
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nFailed to load table %s: %v\n", table, err)
				} else {
					status := "loaded from cache"
					if rebuilt {
						status = "rebuilt index"
					}
					fmt.Printf("\r  %s: %s (%s)\n", table, status, time.Since(start).Round(time.Millisecond))
				}
			}
		}
	}

	// Handle API server mode
	if *apiMode {
		runAPIServer(eng, *apiAddr, *apiUser, *apiPass, *noLoad)
		return
	}

	// Handle single query mode
	if *queryStr != "" {
		result, err := eng.Query(ctx, *queryStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Query error: %v\n", err)
			os.Exit(1)
		}
		printQueryResult(result)
		return
	}

	// Run interactive REPL
	runREPL(eng)
}

func printUsage() {
	fmt.Println(`dbengine - A high-performance database engine for massively wide datasets

Usage:
  dbengine [options]

Options:
  -data <path>       Path to data directory (default: ./data)
  -duckdb <path>     Path to DuckDB file (empty for in-memory)
  -query <sql>       Execute a single query and exit
  --no-load          Skip auto-loading tables on startup
  -api               Run as HTTP API server
  -api-addr <addr>   API server listen address (default: :8080)
  -api-user <user>   API server username
  -api-pass <pass>   API server password
  -version           Show version and exit
  -help, -h          Show this help

Examples:
  # Run interactive REPL
  dbengine

  # Execute a single query
  dbengine -query "SELECT * FROM asset_table"

  # Use custom data directory
  dbengine -data /path/to/data

  # Use persistent DuckDB file
  dbengine -duckdb ./cache.duckdb

  # Skip auto-loading tables (for large datasets)
  dbengine --no-load

  # Start HTTP API server
  dbengine -api -api-addr :8080 -api-user admin -api-pass secret

REPL Commands:
  .tables            List all tables
  .status            Show index status for all tables
  .schema <table>    Show table schema
  .create <json>     Create table from JSON schema
  .insert <table> <json>   Insert row from JSON
  .load <table>      Load table into DuckDB
  .rebuild <table>   Force rebuild table index
  .sync <table>      Sync table with DuckDB
  .drop <table>      Drop a table
  .csv <file> <query>    Export query results to CSV
  .json <file> <query>   Export query results to JSON
  .help              Show help
  .quit              Exit the REPL`)
}

func runAPIServer(eng *engine.Engine, addr, username, password string, noLoad bool) {
	ctx := context.Background()

	// Load tables in API mode if not --no-load
	if !noLoad {
		if tables, err := eng.ListTables(ctx); err == nil && len(tables) > 0 {
			fmt.Printf("Loading %d tables...\n", len(tables))
			for _, table := range tables {
				start := time.Now()
				rebuilt, err := eng.SmartLoad(ctx, table, nil)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Failed to load table %s: %v\n", table, err)
				} else {
					status := "loaded from cache"
					if rebuilt {
						status = "rebuilt index"
					}
					fmt.Printf("  %s: %s (%s)\n", table, status, time.Since(start).Round(time.Millisecond))
				}
			}
		}
	}

	// Create and start API server
	server := api.New(eng, api.Config{
		Addr:     addr,
		Username: username,
		Password: password,
	})

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down API server...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	fmt.Printf("Starting API server on %s\n", addr)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "API server error: %v\n", err)
		os.Exit(1)
	}
}

func runREPL(eng *engine.Engine) {
	rl, err := readline.New("dbengine> ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize readline: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()

	fmt.Println("dbengine - Type .help for commands, .quit to exit")

	ctx := context.Background()

	for {
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			continue
		}
		if err == io.EOF {
			fmt.Println("Goodbye!")
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Handle commands
		if strings.HasPrefix(line, ".") {
			handleCommand(eng, ctx, line)
			continue
		}

		// Execute SQL query
		result, err := eng.Query(ctx, line)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		printQueryResult(result)
	}
}

func handleCommand(eng *engine.Engine, ctx context.Context, line string) {
	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case ".quit", ".exit", ".q":
		fmt.Println("Goodbye!")
		os.Exit(0)

	case ".help", ".h":
		printHelp()

	case ".tables":
		tables, err := eng.ListTables(ctx)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		if len(tables) == 0 {
			fmt.Println("No tables found.")
			return
		}
		fmt.Println("Tables:")
		for _, t := range tables {
			fmt.Printf("  %s\n", t)
		}

	case ".status":
		tables, err := eng.ListTables(ctx)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		if len(tables) == 0 {
			fmt.Println("No tables found.")
			return
		}
		fmt.Println("Table Index Status:")
		for _, t := range tables {
			status, err := eng.GetIndexStatus(ctx, t)
			if err != nil {
				fmt.Printf("  %s: error - %v\n", t, err)
			} else {
				fmt.Printf("  %s: %s\n", t, status)
			}
		}

	case ".schema":
		if len(parts) < 2 {
			fmt.Println("Usage: .schema <table>")
			return
		}
		schema, err := eng.GetTableSchema(ctx, parts[1])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		printSchema(schema)

	case ".create":
		if len(parts) < 2 {
			fmt.Println("Usage: .create <json>")
			return
		}
		jsonStr := strings.TrimPrefix(line, ".create ")
		var schema colgroup.TableSchema
		if err := json.Unmarshal([]byte(jsonStr), &schema); err != nil {
			fmt.Printf("Invalid JSON: %v\n", err)
			return
		}
		if err := eng.CreateTable(ctx, &schema); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Printf("Table %s created.\n", schema.Name)

	case ".insert":
		if len(parts) < 3 {
			fmt.Println("Usage: .insert <table> <json>")
			return
		}
		tableName := parts[1]
		jsonStr := strings.TrimPrefix(line, ".insert "+tableName+" ")
		var rowData struct {
			ID     string                    `json:"_id"`
			Groups map[string]map[string]any `json:"groups,omitempty"`
			Data   map[string]any            `json:"data,omitempty"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &rowData); err != nil {
			fmt.Printf("Invalid JSON: %v\n", err)
			return
		}
		row := colgroup.NewRow(rowData.ID)
		if rowData.Groups != nil {
			row.Groups = rowData.Groups
		}
		if rowData.Data != nil {
			for key, value := range rowData.Data {
				keyParts := strings.SplitN(key, ".", 2)
				if len(keyParts) == 2 {
					row.SetValue(keyParts[0], keyParts[1], value)
				}
			}
		}
		if err := eng.InsertRow(ctx, tableName, row); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Printf("Row %s inserted.\n", row.ID)

	case ".load":
		if len(parts) < 2 {
			fmt.Println("Usage: .load <table>")
			return
		}
		start := time.Now()
		err := eng.LoadTableWithProgress(ctx, parts[1], func(loaded, total int) {
			fmt.Printf("\rLoading: %d/%d rows", loaded, total)
		})
		if err != nil {
			fmt.Printf("\nError: %v\n", err)
			return
		}
		fmt.Printf("\nTable %s loaded (%s).\n", parts[1], time.Since(start).Round(time.Millisecond))

	case ".rebuild":
		if len(parts) < 2 {
			fmt.Println("Usage: .rebuild <table>")
			return
		}
		start := time.Now()
		err := eng.RebuildIndex(ctx, parts[1], func(loaded, total int) {
			fmt.Printf("\rRebuilding: %d/%d rows", loaded, total)
		})
		if err != nil {
			fmt.Printf("\nError: %v\n", err)
			return
		}
		fmt.Printf("\nTable %s index rebuilt (%s).\n", parts[1], time.Since(start).Round(time.Millisecond))

	case ".sync":
		if len(parts) < 2 {
			fmt.Println("Usage: .sync <table>")
			return
		}
		if err := eng.SyncTable(ctx, parts[1]); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Printf("Table %s synced.\n", parts[1])

	case ".drop":
		if len(parts) < 2 {
			fmt.Println("Usage: .drop <table>")
			return
		}
		if err := eng.DropTable(ctx, parts[1]); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Printf("Table %s dropped.\n", parts[1])

	case ".csv":
		if len(parts) < 3 {
			fmt.Println("Usage: .csv <file> <query>")
			return
		}
		filename := parts[1]
		query := strings.TrimPrefix(line, ".csv "+filename+" ")
		result, err := eng.Query(ctx, query)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		if err := exportCSV(filename, result); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Printf("Exported %d rows to %s\n", len(result.Rows), filename)

	case ".json":
		if len(parts) < 3 {
			fmt.Println("Usage: .json <file> <query>")
			return
		}
		filename := parts[1]
		query := strings.TrimPrefix(line, ".json "+filename+" ")
		result, err := eng.Query(ctx, query)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		if err := exportJSON(filename, result); err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Printf("Exported %d rows to %s\n", len(result.Rows), filename)

	default:
		fmt.Printf("Unknown command: %s. Type .help for available commands.\n", cmd)
	}
}

func printHelp() {
	fmt.Println(`Commands:
  .tables            List all tables
  .status            Show index status for all tables
  .schema <table>    Show table schema
  .create <json>     Create table from JSON schema
  .insert <table> <json>   Insert row from JSON
  .load <table>      Load table into DuckDB
  .rebuild <table>   Force rebuild table index
  .sync <table>      Sync table with DuckDB
  .drop <table>      Drop a table
  .csv <file> <query>    Export query results to CSV
  .json <file> <query>   Export query results to JSON
  .help              Show this help
  .quit              Exit

SQL queries are executed directly. Example:
  SELECT Bloomberg.ISIN, ICE.Ticker FROM asset_table WHERE Bloomberg.Ticker = 'AAPL'`)
}

func printSchema(schema *colgroup.TableSchema) {
	fmt.Printf("Table: %s\n", schema.Name)
	fmt.Printf("Primary Key: %s\n", schema.PrimaryKey)
	fmt.Printf("Column Groups:\n")
	for _, group := range schema.ColumnGroups {
		fmt.Printf("  %s:\n", group.Name)
		for _, col := range group.Columns {
			indexed := ""
			if col.Indexed {
				indexed = " (indexed)"
			}
			pk := ""
			if col.PrimaryKey {
				pk = " (primary key)"
			}
			fmt.Printf("    %s: %s%s%s\n", col.Name, col.Type, indexed, pk)
		}
	}
}

func printQueryResult(result *engine.QueryResult) {
	if len(result.Rows) == 0 {
		fmt.Println("(0 rows)")
		return
	}

	// Calculate column widths
	widths := make([]int, len(result.Columns))
	for i, col := range result.Columns {
		widths[i] = len(col)
	}
	for _, row := range result.Rows {
		for i, col := range result.Columns {
			val := fmt.Sprintf("%v", row[col])
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Print header
	for i, col := range result.Columns {
		fmt.Printf("%-*s  ", widths[i], col)
	}
	fmt.Println()

	// Print separator
	for i := range result.Columns {
		fmt.Printf("%s  ", strings.Repeat("-", widths[i]))
	}
	fmt.Println()

	// Print rows
	for _, row := range result.Rows {
		for i, col := range result.Columns {
			val := fmt.Sprintf("%v", row[col])
			fmt.Printf("%-*s  ", widths[i], val)
		}
		fmt.Println()
	}

	fmt.Printf("\n(%d rows)\n", len(result.Rows))
}

func exportCSV(filename string, result *engine.QueryResult) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	// Write header
	if err := w.Write(result.Columns); err != nil {
		return err
	}

	// Write rows
	for _, row := range result.Rows {
		record := make([]string, len(result.Columns))
		for i, col := range result.Columns {
			record[i] = fmt.Sprintf("%v", row[col])
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	return nil
}

func exportJSON(filename string, result *engine.QueryResult) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result.Rows)
}
