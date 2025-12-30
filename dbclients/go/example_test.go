package dbclient_test

import (
	"context"
	"fmt"
	"log"

	dbclient "github.com/dsandor/flatstor/dbengine/dbclients/go"
)

func Example_basicUsage() {
	// Create a new client
	client, err := dbclient.New(dbclient.Config{
		BaseURL:  "http://localhost:8080",
		Username: "admin",
		Password: "secret",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()

	// Check server health
	if err := client.Health(ctx); err != nil {
		log.Fatal(err)
	}

	// Execute a query
	result, err := client.Query(ctx, "SELECT * FROM my_table LIMIT 10")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Query returned %d rows\n", result.RowCount)
	for _, row := range result.Rows {
		fmt.Printf("Row: %v\n", row)
	}
}

func Example_createTable() {
	client, _ := dbclient.New(dbclient.Config{
		BaseURL: "http://localhost:8080",
	})
	defer client.Close()

	ctx := context.Background()

	// Create a table with schema
	schema := &dbclient.TableSchema{
		Name:       "instruments",
		PrimaryKey: "Common.InstrumentID",
		ColumnGroups: []dbclient.ColumnGroup{
			{
				Name: "Common",
				Columns: []dbclient.Column{
					{Name: "InstrumentID", Type: "VARCHAR", PrimaryKey: true},
					{Name: "Name", Type: "VARCHAR"},
				},
			},
			{
				Name: "Bloomberg",
				Columns: []dbclient.Column{
					{Name: "ISIN", Type: "VARCHAR", Indexed: true},
					{Name: "Ticker", Type: "VARCHAR", Indexed: true},
				},
			},
		},
	}

	if err := client.CreateTable(ctx, schema); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Table created successfully")
}

func Example_insertAndQuery() {
	client, _ := dbclient.New(dbclient.Config{
		BaseURL: "http://localhost:8080",
	})
	defer client.Close()

	ctx := context.Background()

	// Insert a row using groups format
	err := client.InsertRow(ctx, "instruments", &dbclient.InsertRowData{
		ID: "abc123-uuid",
		Groups: map[string]map[string]any{
			"Common": {
				"InstrumentID": "abc123-uuid",
				"Name":         "Apple Inc",
			},
			"Bloomberg": {
				"ISIN":   "US0378331005",
				"Ticker": "AAPL",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Query for the row
	result, err := client.Query(ctx, "SELECT * FROM instruments WHERE Bloomberg.Ticker = 'AAPL'")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Found %d rows\n", result.RowCount)
}

func Example_parameterizedQuery() {
	client, _ := dbclient.New(dbclient.Config{
		BaseURL: "http://localhost:8080",
	})
	defer client.Close()

	ctx := context.Background()

	// Execute with parameters (for INSERT/UPDATE/DELETE)
	result, err := client.Execute(ctx,
		"INSERT INTO instruments (Common_InstrumentID, Common_Name) VALUES (?, ?)",
		"new-id", "New Company",
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Rows affected: %d\n", result.RowsAffected)
}
