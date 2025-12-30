package dbclient

import (
	"context"
	"os"
	"testing"
)

func TestClient(t *testing.T) {
	// Skip if no server is running
	serverURL := os.Getenv("DBENGINE_URL")
	if serverURL == "" {
		serverURL = "http://localhost:8090"
	}

	username := os.Getenv("DBENGINE_USER")
	if username == "" {
		username = "testuser"
	}

	password := os.Getenv("DBENGINE_PASS")
	if password == "" {
		password = "testpass"
	}

	client, err := New(Config{
		BaseURL:  serverURL,
		Username: username,
		Password: password,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()

	// Test health
	t.Run("Health", func(t *testing.T) {
		if err := client.Health(ctx); err != nil {
			t.Skipf("Server not available: %v", err)
		}
	})

	// Test list tables
	t.Run("ListTables", func(t *testing.T) {
		tables, err := client.ListTables(ctx)
		if err != nil {
			t.Fatalf("Failed to list tables: %v", err)
		}
		t.Logf("Tables: %v", tables)
	})

	// Test query
	t.Run("Query", func(t *testing.T) {
		result, err := client.Query(ctx, "SELECT * FROM test_api LIMIT 3")
		if err != nil {
			t.Fatalf("Failed to execute query: %v", err)
		}
		t.Logf("Query returned %d rows", result.RowCount)
		if result.RowCount != 3 {
			t.Errorf("Expected 3 rows, got %d", result.RowCount)
		}
	})
}
