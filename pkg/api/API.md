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

When `-api-user` and `-api-pass` are provided, all API endpoints (except `/health`) require HTTP Basic Authentication.

```bash
# Example with curl
curl -u admin:secret http://localhost:8080/api/v1/tables
```

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
