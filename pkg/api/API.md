# dbengine HTTP API Documentation

## Overview

The dbengine HTTP API provides a RESTful interface for executing SQL queries, managing tables, and performing CRUD operations on data. The API supports basic authentication and returns JSON responses.

## Starting the API Server

```bash
# Start API server on default port 8080
./dbengine -api

# Start API server on custom port with authentication
./dbengine -api -api-addr :9000 -api-user admin -api-pass secret

# Start API server with data directory and DuckDB file
./dbengine -api -data ./mydata -duckdb ./mydb.duckdb -api-user admin -api-pass secret
```

## Authentication

When `-api-user` and `-api-pass` are provided, all API endpoints (except `/health` and `/api/v1/auth/*`) require authentication.

### Authentication Methods

The API supports two authentication methods:

1. **HTTP Basic Authentication** - Simple username/password in each request
2. **JWT Bearer Tokens** - Token-based auth for optimized performance (recommended)

### Basic Authentication

```bash
# Example with curl
curl -u admin:secret http://localhost:8080/api/v1/tables
```

### JWT Token Authentication (Recommended)

JWT tokens provide better performance by eliminating credential validation on each request. The server validates the token signature instead, which is much faster.

**Benefits:**
- Faster request processing (no password hashing on each request)
- Stateless authentication
- Automatic token refresh support in clients
- Connection pooling compatibility

#### Login (Get Tokens)

Exchange credentials for an access/refresh token pair.

**Request:**
```
POST /api/v1/auth/login
Content-Type: application/json

{
  "username": "admin",
  "password": "secret"
}
```

**Response:**
```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "expires_at": 1705320000
}
```

| Field | Description |
|-------|-------------|
| `access_token` | Short-lived token for API requests (default: 1 hour) |
| `refresh_token` | Long-lived token to get new access tokens (default: 24 hours) |
| `token_type` | Always "Bearer" |
| `expires_in` | Seconds until access token expires |
| `expires_at` | Unix timestamp when access token expires |

#### Using Access Tokens

Include the access token in the `Authorization` header:

```bash
curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." \
     http://localhost:8080/api/v1/tables
```

#### Refresh Tokens

When the access token expires (or is about to expire), use the refresh token to get a new token pair without re-authenticating:

**Request:**
```
POST /api/v1/auth/refresh
Content-Type: application/json

{
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

**Response:**
```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "expires_at": 1705323600
}
```

**Note:** The old refresh token is revoked after use. Use the new refresh token for subsequent refresh requests.

#### Logout (Revoke Tokens)

Revoke a token to prevent further use:

**Request:**
```
POST /api/v1/auth/logout
Content-Type: application/json

{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

**Response:**
```json
{
  "success": true,
  "message": "Token revoked"
}
```

### Token Configuration

Configure JWT tokens via command line:

```bash
./dbengine -api \
  -api-user admin \
  -api-pass secret \
  -jwt-signing-key "your-secret-key" \
  -access-token-duration 1h \
  -refresh-token-duration 24h
```

| Flag | Default | Description |
|------|---------|-------------|
| `-jwt-signing-key` | (random) | Secret key for signing tokens |
| `-access-token-duration` | 1h | How long access tokens are valid |
| `-refresh-token-duration` | 24h | How long refresh tokens are valid |

**Security Note:** If no signing key is provided, a random 32-byte key is generated at startup. This means tokens will be invalidated if the server restarts.

## Endpoints

### Health Check

Check if the server is running.

**Request:**
```
GET /health
```

**Response:**
```json
{
  "status": "ok",
  "service": "dbengine",
  "time": "2024-01-15T10:30:00Z"
}
```

---

### Execute Query (SELECT)

Execute a SELECT query and return results.

**Request:**
```
POST /api/v1/query
Content-Type: application/json

{
  "sql": "SELECT * FROM my_table LIMIT 10",
  "params": []
}
```

**Response:**
```json
{
  "columns": ["Common_InstrumentID", "Bloomberg_ISIN", "Bloomberg_Ticker"],
  "rows": [
    {
      "Common_InstrumentID": "abc123",
      "Bloomberg_ISIN": "US1234567890",
      "Bloomberg_Ticker": "AAPL"
    }
  ],
  "row_count": 1,
  "execution_ms": 5,
  "translated_sql": "SELECT * FROM my_table LIMIT 10"
}
```

---

### Execute Statement (INSERT/UPDATE/DELETE)

Execute non-SELECT SQL statements.

**Request:**
```
POST /api/v1/execute
Content-Type: application/json

{
  "sql": "INSERT INTO my_table (col1, col2) VALUES (?, ?)",
  "params": ["value1", "value2"]
}
```

**Response:**
```json
{
  "success": true,
  "message": "Insert successful",
  "rows_affected": 1,
  "execution_ms": 3
}
```

---

### List Tables

Get a list of all tables.

**Request:**
```
GET /api/v1/tables
```

**Response:**
```json
{
  "tables": ["asset_table", "test_table", "instruments"]
}
```

---

### Create Table

Create a new table with schema.

**Request:**
```
POST /api/v1/tables
Content-Type: application/json

{
  "name": "my_table",
  "primary_key": "Common.InstrumentID",
  "column_groups": [
    {
      "name": "Common",
      "columns": [
        {"name": "InstrumentID", "type": "VARCHAR", "primary_key": true},
        {"name": "Name", "type": "VARCHAR"}
      ]
    },
    {
      "name": "Bloomberg",
      "columns": [
        {"name": "ISIN", "type": "VARCHAR", "indexed": true},
        {"name": "Ticker", "type": "VARCHAR", "indexed": true}
      ]
    }
  ]
}
```

**Response:**
```json
{
  "success": true,
  "message": "Table my_table created"
}
```

---

### Get Table Schema

Get the schema for a specific table.

**Request:**
```
GET /api/v1/tables/{table_name}
```

**Response:**
```json
{
  "name": "my_table",
  "primary_key": "Common.InstrumentID",
  "created_at": "2024-01-15T10:00:00Z",
  "updated_at": "2024-01-15T10:30:00Z",
  "column_groups": [
    {
      "name": "Common",
      "columns": [
        {"name": "InstrumentID", "type": "VARCHAR", "primary_key": true}
      ]
    }
  ]
}
```

---

### Drop Table

Delete a table and all its data.

**Request:**
```
DELETE /api/v1/tables/{table_name}
```

**Response:**
```json
{
  "success": true,
  "message": "Table my_table dropped"
}
```

---

### Load Table

Load a table from JSON files into DuckDB for querying.

**Request:**
```
POST /api/v1/tables/{table_name}/load
```

**Response:**
```json
{
  "success": true,
  "message": "Table my_table loaded",
  "execution_ms": 150
}
```

---

### Sync Table

Synchronize a table between JSON files and DuckDB.

**Request:**
```
POST /api/v1/tables/{table_name}/sync
```

**Response:**
```json
{
  "success": true,
  "message": "Table my_table synced",
  "execution_ms": 50
}
```

---

### Insert Row

Insert a new row into a table.

**Request:**
```
POST /api/v1/rows/{table_name}
Content-Type: application/json

{
  "_id": "abc123-uuid",
  "groups": {
    "Common": {
      "InstrumentID": "abc123-uuid",
      "Name": "Apple Inc"
    },
    "Bloomberg": {
      "ISIN": "US0378331005",
      "Ticker": "AAPL"
    }
  }
}
```

Alternative format using flat data:
```json
{
  "_id": "abc123-uuid",
  "data": {
    "Common.InstrumentID": "abc123-uuid",
    "Common.Name": "Apple Inc",
    "Bloomberg.ISIN": "US0378331005",
    "Bloomberg.Ticker": "AAPL"
  }
}
```

**Response:**
```json
{
  "success": true,
  "message": "Row abc123-uuid inserted"
}
```

---

### Get Row

Get a specific row by ID.

**Request:**
```
GET /api/v1/rows/{table_name}/{row_id}
```

**Response:**
```json
{
  "id": "abc123-uuid",
  "groups": {
    "Common": {
      "InstrumentID": "abc123-uuid",
      "Name": "Apple Inc"
    },
    "Bloomberg": {
      "ISIN": "US0378331005",
      "Ticker": "AAPL"
    }
  }
}
```

---

### Update Row

Update an existing row.

**Request:**
```
PUT /api/v1/rows/{table_name}/{row_id}
Content-Type: application/json

{
  "groups": {
    "Bloomberg": {
      "Ticker": "AAPL-US"
    }
  }
}
```

**Response:**
```json
{
  "success": true,
  "message": "Row abc123-uuid updated"
}
```

---

### Delete Row

Delete a row from a table.

**Request:**
```
DELETE /api/v1/rows/{table_name}/{row_id}
```

**Response:**
```json
{
  "success": true,
  "message": "Row abc123-uuid deleted"
}
```

---

## Error Responses

All error responses follow this format:

```json
{
  "error": "Bad Request",
  "code": "query_error",
  "message": "syntax error at or near 'SELEC'"
}
```

Common error codes:
- `unauthorized` - Invalid or missing credentials
- `invalid_credentials` - Username or password incorrect
- `invalid_token` - JWT token is invalid or expired
- `token_revoked` - JWT token has been revoked
- `invalid_json` - Malformed JSON body
- `missing_sql` - SQL query not provided
- `query_error` - SQL query execution error
- `execute_error` - SQL statement execution error
- `not_found` - Table or row not found
- `method_not_allowed` - HTTP method not supported for endpoint

---

## SQL Query Syntax

dbengine supports a TSQL-style syntax with column group prefixes:

```sql
-- Select specific columns from groups
SELECT Bloomberg.ISIN, Bloomberg.Ticker, ICE.ISIN
FROM asset_table
WHERE Bloomberg.Ticker = 'AAPL'

-- Select all columns
SELECT * FROM asset_table WHERE Common.InstrumentID = 'abc123'

-- Select all columns from a specific group
SELECT Bloomberg.* FROM asset_table LIMIT 10

-- Use LIMIT and ORDER BY
SELECT * FROM asset_table ORDER BY Bloomberg.Ticker LIMIT 100
```

---

## Parameterized Queries

For INSERT, UPDATE, and DELETE operations, use parameterized queries to prevent SQL injection:

```json
{
  "sql": "INSERT INTO my_table (col1, col2) VALUES (?, ?)",
  "params": ["value1", 123]
}
```

```json
{
  "sql": "UPDATE my_table SET col1 = ? WHERE col2 = ?",
  "params": ["new_value", "old_value"]
}
```

```json
{
  "sql": "DELETE FROM my_table WHERE col1 = ?",
  "params": ["value_to_delete"]
}
```

---

## Client Libraries

Official client libraries are available for Go and Python in the `dbclients/` directory.

### Go Client

Located in `dbclients/go/`. Install as a module:

```bash
go get github.com/yourusername/flatstor/dbengine/dbclients/go
```

**Basic Usage:**

```go
package main

import (
    "context"
    "fmt"
    dbclient "github.com/yourusername/flatstor/dbengine/dbclients/go"
)

func main() {
    // Create client with connection pooling and token auth
    client, err := dbclient.New(dbclient.Config{
        BaseURL:      "http://localhost:8080",
        Username:     "admin",
        Password:     "secret",
        UseTokenAuth: true,  // Use JWT tokens (recommended)
        AutoRefresh:  true,  // Auto-refresh tokens before expiry
    })
    if err != nil {
        panic(err)
    }
    defer client.Close()

    ctx := context.Background()

    // Login to get tokens (if using token auth)
    if err := client.Login(ctx); err != nil {
        panic(err)
    }

    // Execute queries
    result, err := client.Query(ctx, "SELECT * FROM my_table LIMIT 10")
    if err != nil {
        panic(err)
    }
    fmt.Printf("Got %d rows\n", result.RowCount)
}
```

**Configuration Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `BaseURL` | required | API server URL |
| `Username` | "" | Username for authentication |
| `Password` | "" | Password for authentication |
| `Timeout` | 30s | Request timeout |
| `UseTokenAuth` | false | Use JWT tokens instead of Basic Auth |
| `AutoRefresh` | true | Auto-refresh tokens before expiry |
| `RefreshThreshold` | 5m | Refresh tokens this long before expiry |
| `MaxIdleConns` | 100 | Max idle connections in pool |
| `MaxConnsPerHost` | 100 | Max connections per host |
| `IdleConnTimeout` | 90s | How long idle connections stay open |

### Python Client

Located in `dbclients/python/`. Install:

```bash
pip install /path/to/dbclients/python
# or copy dbclient.py to your project
```

**Basic Usage:**

```python
from dbclient import connect

# Create connection with token auth and pooling
conn = connect(
    url="http://localhost:8080",
    username="admin",
    password="secret",
    use_token_auth=True,  # Use JWT tokens (recommended)
    auto_refresh=True,    # Auto-refresh tokens before expiry
)

# Execute queries using DB-API 2.0 cursor
cursor = conn.cursor()
cursor.execute("SELECT * FROM my_table LIMIT 10")

# Fetch results
rows = cursor.fetchall()
print(f"Got {len(rows)} rows")

# Column names
print(cursor.description)

cursor.close()
conn.close()
```

**Configuration Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `url` | required | API server URL |
| `username` | None | Username for authentication |
| `password` | None | Password for authentication |
| `timeout` | 30.0 | Request timeout in seconds |
| `use_token_auth` | True | Use JWT tokens instead of Basic Auth |
| `auto_refresh` | True | Auto-refresh tokens before expiry |
| `max_connections` | 10 | Max connections in pool |

**DB-API 2.0 Compliance:**

The Python client follows the DB-API 2.0 specification (PEP 249):

```python
# Parameterized queries
cursor.execute("SELECT * FROM users WHERE id = ?", [user_id])

# Fetch methods
cursor.fetchone()   # Single row
cursor.fetchmany(5) # Up to 5 rows
cursor.fetchall()   # All remaining rows

# Row count
cursor.rowcount     # Number of rows affected/returned

# Column info
cursor.description  # [(name, type_code, ...), ...]
```

---

## Performance Tips

### Use JWT Token Authentication

JWT tokens are validated cryptographically (fast) rather than checking credentials against storage (slow). For high-throughput applications, always use token auth:

```bash
# Initial login
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"secret"}'

# Subsequent requests use the token
curl http://localhost:8080/api/v1/tables \
  -H "Authorization: Bearer <access_token>"
```

### Enable Connection Keep-Alive

The server sends `Connection: keep-alive` headers. Ensure your HTTP client reuses connections:

- **Go**: The client uses `http.Transport` with connection pooling by default
- **Python**: The client uses `urllib3.HTTPConnectionPool` by default
- **curl**: Use `--keepalive-time` flag

### Batch Operations

For bulk inserts, use the `/api/v1/execute` endpoint with transactions or batch SQL statements when possible.

### Monitor Token Expiry

Access tokens expire after 1 hour by default. Use the `expires_at` field to proactively refresh tokens before they expire, avoiding request failures.
