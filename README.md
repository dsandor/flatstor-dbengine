# Flatstor DBEngine

A high-performance database engine for querying massively wide datasets (10,000+ columns) with TSQL-style syntax, column group namespacing, and O(1) row lookups.

## Technology Stack

- **Language:** Go
- **SQL Engine:** Embedded DuckDB (via go-duckdb)
- **Storage Format:** JSON files (one file per column group per row)
- **Storage Backend:** Disk (initially), S3 (future)

## Architecture

```mermaid
flowchart TB
    subgraph Client["Client Layer"]
        CLI["CLI Interface"]
        API["HTTP API Server"]
        GoClient["Go Client"]
        PyClient["Python Client"]
    end

    subgraph Query["Query Translation Layer"]
        Parser["SQL Parser<br/>(Column Group Syntax)"]
        Translator["Query Translator<br/>(Expands to DuckDB SQL)"]
    end

    subgraph Engine["DuckDB Engine"]
        DuckDB["Embedded DuckDB<br/>(Query Execution)"]
    end

    subgraph ColGroup["Column Group Manager"]
        Registry["Column Group Registry"]
        Assembler["Row Assembler"]
    end

    subgraph Storage["Storage Abstraction"]
        Disk["Disk Storage"]
        S3["S3 Storage<br/>(Future)"]
    end

    subgraph Data["Data Files (JSON)"]
        Schema["_schema.json"]
        RowFiles["Row JSON Files<br/>(Per column group)"]
    end

    GoClient --> API
    PyClient --> API
    CLI --> Parser
    API --> Parser
    Parser --> Translator
    Translator --> DuckDB
    DuckDB --> Registry
    Registry --> Assembler
    Assembler --> Disk
    Assembler -.-> S3
    Disk --> Schema
    Disk --> RowFiles
```

## Project Structure

```
dbengine/
├── cmd/
│   └── dbengine/
│       └── main.go              # CLI entry point (REPL + API server)
├── pkg/
│   ├── api/
│   │   ├── server.go            # HTTP API server
│   │   ├── auth.go              # JWT authentication manager
│   │   └── API.md               # API documentation
│   ├── query/
│   │   ├── parser.go            # Column group SQL parser
│   │   └── translator.go        # Translates to DuckDB SQL
│   ├── duckdb/
│   │   ├── engine.go            # DuckDB wrapper
│   │   └── loader.go            # Load rows into DuckDB
│   ├── storage/
│   │   ├── interface.go         # Storage abstraction
│   │   └── disk.go              # Disk implementation
│   ├── colgroup/
│   │   ├── manager.go           # Column group registry
│   │   ├── assembler.go         # Assembles row from JSON files
│   │   └── schema.go            # Schema definitions
│   └── engine/
│       └── engine.go            # Main engine coordinator
├── dbclients/
│   ├── go/
│   │   ├── client.go            # Go client with connection pooling
│   │   └── go.mod               # Go module file
│   └── python/
│       └── dbclient.py          # Python DB-API 2.0 client
├── scripts/
│   ├── generate_data.py         # Test data generator
│   └── clean_data.py            # Data cleanup script
├── data/                        # Data directory
│   └── {table_name}/
│       ├── _schema.json         # Table schema
│       └── {row_id}/
│           ├── Common.json
│           ├── Bloomberg.json
│           └── ICE.json
├── go.mod
├── go.sum
├── go.work                      # Go workspace (for multi-module)
├── CLAUDE.md
└── README.md
```

## Building

```bash
# Build the project
go build -o dbengine ./cmd/dbengine

# Run tests
go test ./...
```

## Generating Test Data

Use the `generate_data.py` script to create test datasets with configurable size:

```bash
# Generate 100 rows with 50 columns (default)
python3 scripts/generate_data.py

# Generate 1000 rows with 500 columns across 25 groups
python3 scripts/generate_data.py --rows 1000 --columns 500 --groups 25

# Generate a massively wide dataset (10,000 columns)
python3 scripts/generate_data.py --rows 100 --columns 10000 --groups 100

# Specify custom table name and data path
python3 scripts/generate_data.py --rows 500 --columns 200 --table my_assets --data-path ./mydata

# Use a seed for reproducible data
python3 scripts/generate_data.py --rows 100 --columns 50 --seed 42
```

### Data Generator Options

| Option | Short | Default | Description |
|--------|-------|---------|-------------|
| `--rows` | `-r` | 100 | Number of rows to generate |
| `--columns` | `-c` | 50 | Total number of columns |
| `--groups` | `-g` | columns/10 | Number of column groups |
| `--table` | `-t` | test_table | Table name |
| `--data-path` | `-d` | ./data | Data directory path |
| `--seed` | `-s` | None | Random seed for reproducibility |

### Generated Data Types

The script randomly assigns these types to columns:
- `VARCHAR` - Random strings
- `INTEGER` - Random integers (-1M to 1M)
- `DOUBLE` - Random floats with 4 decimal places
- `BOOLEAN` - True/False
- `DATE` - Random dates (2020-2024)

About 5% of columns are marked as indexed.

### Memory-Efficient Data Generation

When generating large datasets (100k+ rows or 1000+ columns), use these strategies to avoid running out of memory:

#### Use Batch Mode

The `--batch-size` option groups multiple rows into single files, reducing both memory usage and file I/O overhead:

```bash
# Generate 500,000 rows with 2,000 columns using batches of 1,000 rows
python3 scripts/generate_data.py --rows 500000 --columns 2000 --batch-size 1000

# For very large datasets, larger batch sizes are more efficient
python3 scripts/generate_data.py --rows 1000000 --columns 5000 --batch-size 5000
```

**How batch mode helps:**
- Writes multiple rows to a single JSONL file per column group, reducing file handle overhead
- Triggers garbage collection after each batch to release memory
- Shows progress with rows/sec metrics

#### Single-Row Streaming Mode (Default)

Without `--batch-size`, the script uses streaming writes that minimize memory:
- Each row is written directly to disk without building the full row in memory
- Garbage collection runs periodically (every 1000 rows or 1% of total, whichever is larger)
- JSON is written incrementally to avoid large string allocations

```bash
# Default streaming mode - good for moderate datasets
python3 scripts/generate_data.py --rows 50000 --columns 1000
```

### Parallel Data Generation

For faster data generation, run multiple instances of the script in parallel. Each instance should generate a separate table that can be merged later, or use table partitioning.

#### Method 1: Parallel Table Generation (Recommended)

Generate multiple tables concurrently using shell parallelization:

```bash
# Generate 4 tables in parallel, each with 250,000 rows (total: 1M rows)
for i in {0..3}; do
  python3 scripts/generate_data.py \
    --rows 250000 \
    --columns 2000 \
    --batch-size 1000 \
    --table "assets_part_${i}" \
    --seed $((42 + i)) &
done
wait

echo "All partitions generated"
```

#### Method 2: Using GNU Parallel

If you have GNU parallel installed, you can parallelize more elegantly:

```bash
# Install GNU parallel if needed: brew install parallel (macOS) or apt install parallel (Linux)

# Generate 8 partitions across all CPU cores
seq 0 7 | parallel -j8 \
  python3 scripts/generate_data.py \
    --rows 125000 \
    --columns 2000 \
    --batch-size 1000 \
    --table "assets_part_{}" \
    --seed {}
```

#### Method 3: Python Multiprocessing Wrapper

Create a wrapper script for parallel generation:

```python
#!/usr/bin/env python3
"""parallel_generate.py - Generate data in parallel using multiprocessing."""

import subprocess
import sys
from multiprocessing import Pool, cpu_count

def generate_partition(args):
    partition_id, total_rows, columns, batch_size, base_table = args
    rows_per_partition = total_rows // num_partitions

    cmd = [
        sys.executable, "scripts/generate_data.py",
        "--rows", str(rows_per_partition),
        "--columns", str(columns),
        "--batch-size", str(batch_size),
        "--table", f"{base_table}_part_{partition_id}",
        "--seed", str(partition_id)
    ]

    subprocess.run(cmd, check=True)
    return partition_id

if __name__ == "__main__":
    num_partitions = cpu_count()
    total_rows = 1000000
    columns = 2000
    batch_size = 1000
    base_table = "assets"

    args_list = [
        (i, total_rows, columns, batch_size, base_table)
        for i in range(num_partitions)
    ]

    with Pool(num_partitions) as pool:
        results = pool.map(generate_partition, args_list)

    print(f"Generated {num_partitions} partitions with {total_rows} total rows")
```

Run with:
```bash
python3 parallel_generate.py
```

### Performance Guidelines

| Dataset Size | Recommended Approach |
|-------------|---------------------|
| < 10k rows | Default streaming mode |
| 10k - 100k rows | Batch mode (`--batch-size 1000`) |
| 100k - 500k rows | Batch mode + 2-4 parallel instances |
| 500k+ rows | Batch mode + parallel (one per CPU core) |

**Memory estimates:**
- Default mode: ~50MB per 10k rows with 1000 columns
- Batch mode: ~20MB per batch regardless of total rows
- Parallel mode: Memory usage per instance × number of instances

**Disk space estimates:**
- ~1KB per column group file (uncompressed JSON)
- With 100 column groups × 100k rows = ~10GB
- JSONL batch files are more compact (~30% smaller)

## Usage

### Command Line Options

```bash
# Run interactive REPL
./dbengine

# Execute a single query
./dbengine -query "SELECT * FROM asset_table"

# Specify custom data directory
./dbengine -data /path/to/data

# Use persistent DuckDB file (instead of in-memory)
./dbengine -duckdb ./cache.duckdb

# Skip auto-loading tables on startup (for large datasets)
./dbengine --no-load

# Start HTTP API server
./dbengine -api -api-addr :8080 -api-user admin -api-pass secret

# Show help
./dbengine -help
```

**Note:** On startup, the engine loads all tables into DuckDB. For large datasets (50k+ rows), use `--no-load` to skip this and manually load tables with `.load <table>`.

## HTTP API Server

The database engine can run as an HTTP API server, allowing remote access from Go, Python, and other clients.

### Starting the API Server

```bash
# Start API server with authentication
./dbengine -api -api-addr :8080 -api-user admin -api-pass secret

# Start with custom data directory
./dbengine -api -data ./mydata -duckdb ./cache.duckdb -api-user admin -api-pass secret
```

### API Server Options

| Flag | Default | Description |
|------|---------|-------------|
| `-api` | false | Enable HTTP API server mode |
| `-api-addr` | `:8080` | Server listen address |
| `-api-user` | "" | Username for authentication |
| `-api-pass` | "" | Password for authentication |

### Authentication

The API supports two authentication methods:

1. **HTTP Basic Authentication** - Username/password in each request
2. **JWT Bearer Tokens** - Token-based auth for optimized performance (recommended)

#### JWT Token Authentication

JWT tokens provide better performance by eliminating credential validation on each request. The server validates the token signature cryptographically, which is much faster than password hashing.

```bash
# 1. Login to get tokens
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"secret"}'

# Response:
# {
#   "access_token": "eyJhbGciOiJIUzI1NiIs...",
#   "refresh_token": "eyJhbGciOiJIUzI1NiIs...",
#   "token_type": "Bearer",
#   "expires_in": 3600,
#   "expires_at": 1767110141
# }

# 2. Use access token for API requests
curl http://localhost:8080/api/v1/tables \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIs..."

# 3. Refresh tokens when access token expires
curl -X POST http://localhost:8080/api/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{"refresh_token":"eyJhbGciOiJIUzI1NiIs..."}'
```

### API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Health check (no auth required) |
| POST | `/api/v1/auth/login` | Get JWT token pair |
| POST | `/api/v1/auth/refresh` | Refresh tokens |
| POST | `/api/v1/auth/logout` | Revoke tokens |
| POST | `/api/v1/query` | Execute SELECT queries |
| POST | `/api/v1/execute` | Execute INSERT/UPDATE/DELETE |
| GET | `/api/v1/tables` | List all tables |
| POST | `/api/v1/tables` | Create a table |
| GET | `/api/v1/tables/{name}` | Get table schema |
| DELETE | `/api/v1/tables/{name}` | Drop a table |

See [pkg/api/API.md](pkg/api/API.md) for complete API documentation.

## Client Libraries

Official client libraries are available for Go and Python with built-in support for:
- JWT token authentication with automatic refresh
- HTTP connection pooling for optimal performance
- Thread-safe token caching

### Go Client

Located in `dbclients/go/`.

```go
package main

import (
    "context"
    "fmt"
    dbclient "github.com/dsandor/flatstor/dbengine/dbclients/go"
)

func main() {
    // Create client with JWT auth and connection pooling
    client, _ := dbclient.New(dbclient.Config{
        BaseURL:      "http://localhost:8080",
        Username:     "admin",
        Password:     "secret",
        UseTokenAuth: true,  // Use JWT tokens
        AutoRefresh:  true,  // Auto-refresh before expiry
    })
    defer client.Close()

    ctx := context.Background()

    // Login to get tokens
    client.Login(ctx)

    // Execute queries (uses cached token)
    result, _ := client.Query(ctx, "SELECT * FROM asset_table LIMIT 10")
    fmt.Printf("Got %d rows\n", result.RowCount)
}
```

### Python Client

Located in `dbclients/python/`. DB-API 2.0 compliant.

```python
from dbclient import connect

# Create connection with JWT auth and pooling
conn = connect(
    url="http://localhost:8080",
    username="admin",
    password="secret",
    use_token_auth=True,  # Use JWT tokens
    auto_refresh=True,    # Auto-refresh before expiry
)

# Execute queries using DB-API 2.0 cursor
cursor = conn.cursor()
cursor.execute("SELECT * FROM asset_table LIMIT 10")
rows = cursor.fetchall()
print(f"Got {len(rows)} rows")

cursor.close()
conn.close()
```

## Performance

### JWT + Connection Pooling Performance

Performance tests were conducted to measure the effectiveness of JWT token authentication and HTTP connection pooling.

#### Test Environment
- **Hardware:** Apple Silicon Mac
- **Server:** dbengine HTTP API running locally on port 8090
- **Test Method:** Sequential API requests using Go and Python clients with JWT auth enabled and connection pooling
- **Metrics:** Round-trip time per request measured with `time.Now()` (Go) and `time.time()` (Python)

#### Test Results

| Metric | Go Client | Python Client |
|--------|-----------|---------------|
| Initial login (JWT token generation) | 4.2ms | 2.0ms |
| Health check (with token) | 0.19ms | 0.45ms |
| List tables (first request) | 0.23ms | 0.46ms |
| List tables (pooled connection) | 0.13-0.23ms | 0.13-0.21ms |
| **Average request time** | **0.19ms** | **0.15ms** |
| Token refresh | 0.12ms | N/A* |

*Python client uses automatic internal refresh

#### Connection Pooling Results (10 Sequential Requests)

**Go Client:**
```
Request 1:  0.23ms
Request 2:  0.25ms
Request 3:  0.23ms
Request 4:  0.20ms
Request 5:  0.24ms
Request 6:  0.18ms
Request 7:  0.16ms
Request 8:  0.15ms
Request 9:  0.13ms
Request 10: 0.14ms
Average:    0.19ms
```

**Python Client:**
```
Request 1:  0.17ms
Request 2:  0.15ms
Request 3:  0.14ms
Request 4:  0.14ms
Request 5:  0.17ms
Request 6:  0.17ms
Request 7:  0.14ms
Request 8:  0.15ms
Request 9:  0.13ms
Request 10: 0.15ms
Average:    0.15ms
```

#### Key Findings

1. **Sub-millisecond requests**: After initial connection, both clients achieve consistent sub-millisecond response times
2. **Connection reuse**: HTTP Keep-Alive connections are reused effectively, eliminating TCP handshake overhead
3. **Token caching**: JWT tokens are cached and reused, avoiding credential validation on each request
4. **Warm-up effect**: Request times decrease after first few requests as connections are established and pooled

#### Why JWT + Connection Pooling Matters

| Without Optimization | With JWT + Pooling |
|---------------------|-------------------|
| Password hash verification per request (~2-5ms) | Token signature verification (~0.01ms) |
| New TCP connection per request (~1-3ms) | Reused pooled connection (~0.05ms) |
| TLS handshake per request (~5-10ms) | Single handshake, then reuse |
| **Total overhead: 8-18ms/request** | **Total overhead: <0.2ms/request** |

This represents a **40-90x improvement** in authentication and connection overhead for high-frequency API access.

### REPL Commands

| Command | Description |
|---------|-------------|
| `.tables` | List all tables |
| `.status` | Show index status for all tables |
| `.schema <table>` | Show table schema |
| `.create <json>` | Create table from JSON schema |
| `.insert <table> <json>` | Insert row from JSON |
| `.load <table>` | Load table into DuckDB |
| `.rebuild <table>` | Force rebuild table index |
| `.sync <table>` | Sync table with DuckDB |
| `.drop <table>` | Drop a table |
| `.csv <file> <query>` | Export query results to CSV file |
| `.json <file> <query>` | Export query results to JSON file |
| `.help` | Show help |
| `.quit` | Exit the REPL |

## Data Storage

### Wide Column Architecture

The following diagram illustrates how massively wide datasets (10,000+ columns) are organized into column groups and stored as separate JSON files per row:

```mermaid
flowchart TB
    subgraph QueryLayer["Query Layer"]
        Query["SELECT Bloomberg.ISIN, ICE.Ticker<br/>FROM assets<br/>WHERE Bloomberg.Ticker = 'AAPL'"]
    end

    subgraph Translation["Query Translation"]
        Translator["Column Group Translator"]
        DuckSQL["SELECT Bloomberg_ISIN, ICE_Ticker<br/>FROM assets<br/>WHERE Bloomberg_Ticker = 'AAPL'"]
    end

    subgraph LogicalView["Logical View (Flattened Wide Table)"]
        subgraph Headers["Column Headers (10,000+ columns possible)"]
            H1["_row_id"]
            H2["Common_<br/>InstrumentID"]
            H3["Bloomberg_<br/>ISIN"]
            H4["Bloomberg_<br/>Ticker"]
            H5["Bloomberg_<br/>BBGLOBAL"]
            H6["ICE_<br/>ISIN"]
            H7["ICE_<br/>Ticker"]
            H8["..."]
        end
    end

    subgraph PhysicalStorage["Physical Storage (Per-Row JSON Files)"]
        subgraph Row1["Row: abc123_guid/"]
            R1Common["Common.json<br/>{InstrumentID: 'abc123'}"]
            R1Bloomberg["Bloomberg.json<br/>{ISIN: 'AAA1',<br/>Ticker: 'AAPL',<br/>BBGLOBAL: 'AAA1BBG'}"]
            R1ICE["ICE.json<br/>{ISIN: 'AAA1',<br/>Ticker: 'AAPL-US'}"]
        end

        subgraph Row2["Row: def456_guid/"]
            R2Common["Common.json<br/>{InstrumentID: 'def456'}"]
            R2Bloomberg["Bloomberg.json<br/>{ISIN: 'BBB2',<br/>Ticker: 'MSFT',<br/>BBGLOBAL: 'BBB2BBG'}"]
            R2ICE["ICE.json<br/>{ISIN: 'BBB2',<br/>Ticker: 'MSFT-US'}"]
        end
    end

    Query --> Translator
    Translator --> DuckSQL
    DuckSQL --> LogicalView
    LogicalView --> PhysicalStorage
```

### Column Group Concept

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                           SINGLE ROW (abc123_guid)                                   │
├─────────────────┬───────────────────────────┬───────────────────────────┬───────────┤
│  Common Group   │     Bloomberg Group       │       ICE Group           │    ...    │
│  (1 column)     │     (50 columns)          │       (30 columns)        │ (N groups)│
├─────────────────┼───────────────────────────┼───────────────────────────┼───────────┤
│ InstrumentID    │ ISIN, Ticker, BBGLOBAL,   │ ISIN, Ticker, Exchange,   │           │
│                 │ Currency, Price, Volume,  │ Region, AssetClass,       │           │
│                 │ MarketCap, Sector, ...    │ Liquidity, Rating, ...    │           │
├─────────────────┼───────────────────────────┼───────────────────────────┼───────────┤
│                 │                           │                           │           │
│  Common.json    │     Bloomberg.json        │       ICE.json            │   ....    │
│  (1 file)       │     (1 file)              │       (1 file)            │           │
└─────────────────┴───────────────────────────┴───────────────────────────┴───────────┘

Benefits of Column Group Storage:
• Update Bloomberg data without touching ICE data
• Add new data sources (column groups) without schema migration
• Query only the column groups you need
• Human-readable JSON files for debugging
• O(1) row lookup by ID
```

### Query Flow with Column Groups

```
User Query:                                    Physical File Access:
─────────────────────────────────────────      ─────────────────────────────────────
SELECT Bloomberg.ISIN,
       Bloomberg.Ticker,                  ──►  Only reads Bloomberg.json
       ICE.Ticker                              and ICE.json per matching row
FROM assets                                    (skips Common.json and other groups)
WHERE Bloomberg.Ticker = 'AAPL'

                    │
                    ▼
            ┌───────────────┐
            │ Query Parser  │
            │ (Column Group │
            │   Syntax)     │
            └───────┬───────┘
                    │
                    ▼
            ┌───────────────┐
            │  Translator   │
            │ Bloomberg.ISIN│
            │      ▼        │
            │ Bloomberg_ISIN│
            └───────┬───────┘
                    │
                    ▼
            ┌───────────────┐
            │    DuckDB     │
            │  (Execution)  │
            └───────┬───────┘
                    │
                    ▼
            ┌───────────────┐
            │    Result     │
            │ Bloomberg_ISIN│
            │ Bloomberg_... │
            │ ICE_Ticker    │
            └───────────────┘
```

### JSON File Format

Each row is stored as multiple JSON files, one per column group:

**`data/asset_table/abc123_guid/Common.json`**
```json
{
  "InstrumentID": "abc123_guid"
}
```

**`data/asset_table/abc123_guid/Bloomberg.json`**
```json
{
  "ISIN": "AAA1",
  "BBGLOBAL": "AAA1BBG",
  "Ticker": "AAPL"
}
```

**`data/asset_table/abc123_guid/ICE.json`**
```json
{
  "ISIN": "AAA1",
  "Ticker": "AAPL-US"
}
```

### On-Disk Layout

```
data/
└── asset_table/
    ├── _schema.json              # Table schema + column groups
    ├── abc123_guid/
    │   ├── Common.json
    │   ├── Bloomberg.json
    │   └── ICE.json
    └── def456_guid/
        ├── Common.json
        ├── Bloomberg.json
        └── ICE.json
```

## SQL Examples

### Column Group Syntax

Use `ColumnGroup.ColumnName` syntax to reference columns:

```sql
-- Query specific columns from different groups
SELECT Common.InstrumentID, Bloomberg.ISIN, ICE.Ticker
FROM asset_table
WHERE Bloomberg.Ticker = 'AAPL'

-- Query all columns
SELECT * FROM asset_table WHERE Common.InstrumentID = 'abc123_guid'

-- Filter by column group
SELECT Bloomberg.ISIN, Bloomberg.Ticker
FROM asset_table
WHERE Bloomberg.BBGLOBAL = 'AAA1BBG'
```

### Creating Tables

```sql
-- Via REPL command (JSON format)
.create {"name":"asset_table","primary_key":"Common.InstrumentID","column_groups":[{"name":"Common","columns":[{"name":"InstrumentID","type":"VARCHAR","primary_key":true}]},{"name":"Bloomberg","columns":[{"name":"ISIN","type":"VARCHAR"},{"name":"BBGLOBAL","type":"VARCHAR"},{"name":"Ticker","type":"VARCHAR","indexed":true}]},{"name":"ICE","columns":[{"name":"ISIN","type":"VARCHAR"},{"name":"Ticker","type":"VARCHAR"}]}]}
```

### Inserting Data

```sql
-- Via REPL command (JSON format with grouped data)
.insert asset_table {"_id":"abc123_guid","groups":{"Common":{"InstrumentID":"abc123_guid"},"Bloomberg":{"ISIN":"AAA1","BBGLOBAL":"AAA1BBG","Ticker":"AAPL"},"ICE":{"ISIN":"AAA1","Ticker":"AAPL-US"}}}

-- Alternative format with dot notation
.insert asset_table {"_id":"def456_guid","data":{"Common.InstrumentID":"def456_guid","Bloomberg.ISIN":"BBB2","Bloomberg.Ticker":"MSFT","ICE.ISIN":"BBB2","ICE.Ticker":"MSFT-US"}}
```

## Query Translation

The engine translates column group SQL to DuckDB SQL:

| Input | Translated |
|-------|------------|
| `Bloomberg.ISIN` | `Bloomberg_ISIN` |
| `ICE.Ticker` | `ICE_Ticker` |
| `Common.InstrumentID` | `Common_InstrumentID` |

**Example:**
```sql
-- Input
SELECT Bloomberg.ISIN FROM asset_table WHERE Bloomberg.Ticker = 'AAPL'

-- Translated to DuckDB
SELECT Bloomberg_ISIN FROM asset_table WHERE Bloomberg_Ticker = 'AAPL'
```

## Supported Data Types

| Type | Description |
|------|-------------|
| `VARCHAR` | Variable-length string |
| `INTEGER` | 32-bit integer |
| `BIGINT` | 64-bit integer |
| `DOUBLE` | 64-bit floating point |
| `BOOLEAN` | True/false |
| `DATE` | Date without time |
| `TIMESTAMP` | Date with time |
| `JSON` | JSON data |

## Key Design Decisions

### 1. JSON File-Per-Column-Group Storage

Each column group (Bloomberg, ICE, Common) stored as separate JSON file per row:

**Benefits:**
- Independent updates per data source
- Human-readable for debugging
- Granular updates (only write changed column groups)
- Easy to add new column groups without migrating data

### 2. DuckDB as Query Engine

- Handles SQL parsing, optimization, execution
- Data provided via virtual table/table function
- Indexing handled by DuckDB on materialized views
- Proven query engine with excellent performance

### 3. Column Group Namespacing

Syntax: `ColumnGroup.ColumnName` (e.g., `Bloomberg.ISIN`)

**Benefits:**
- Resolves naming conflicts (multiple sources can have `ISIN`)
- Clear data provenance
- Easy to extend with new data sources

### 4. Storage Interface Abstraction

```go
type Storage interface {
    ReadJSON(ctx context.Context, path string, v any) error
    WriteJSON(ctx context.Context, path string, v any) error
    Delete(ctx context.Context, path string) error
    ListDirs(ctx context.Context, path string) ([]string, error)
    Exists(ctx context.Context, path string) (bool, error)
}
```

This allows easy addition of S3 storage support in the future.

## Implementation Phases

### Phase 1: Core Foundation (Complete)
- Project setup with Go module
- Storage abstraction layer
- Schema management

### Phase 2: Column Group System (Complete)
- Column group manager
- Row assembler
- Row storage operations

### Phase 3: DuckDB Integration (Complete)
- DuckDB engine wrapper
- Data loader
- Index management

### Phase 4: Query Translation (Complete)
- SQL parser for column groups
- Query translator

### Phase 5: CLI Interface (Complete)
- Interactive REPL mode
- Single query execution
- Data import from JSON

### Phase 6: S3 Support (Future)
- Implement Storage interface for S3
- Local caching layer
- Batch operations for performance

## Example Workflow

```bash
# Start the REPL
./dbengine

# Create a table
dbengine> .create {"name":"asset_table","primary_key":"Common.InstrumentID","column_groups":[{"name":"Common","columns":[{"name":"InstrumentID","type":"VARCHAR","primary_key":true}]},{"name":"Bloomberg","columns":[{"name":"ISIN","type":"VARCHAR"},{"name":"Ticker","type":"VARCHAR","indexed":true}]},{"name":"ICE","columns":[{"name":"ISIN","type":"VARCHAR"},{"name":"Ticker","type":"VARCHAR"}]}]}
Table asset_table created.

# Insert a row
dbengine> .insert asset_table {"_id":"abc123","groups":{"Common":{"InstrumentID":"abc123"},"Bloomberg":{"ISIN":"AAA1","Ticker":"AAPL"},"ICE":{"ISIN":"AAA1","Ticker":"AAPL-US"}}}
Row abc123 inserted.

# Query the data
dbengine> SELECT Bloomberg.ISIN, ICE.Ticker FROM asset_table WHERE Bloomberg.Ticker = 'AAPL'
Bloomberg_ISIN  ICE_Ticker
--------------  ----------
AAA1            AAPL-US

(1 rows)

# View schema
dbengine> .schema asset_table
Table: asset_table
Primary Key: Common.InstrumentID
...

# Exit
dbengine> .quit
Goodbye!
```

## License

This project uses a **Source Available License** with the following terms:

- **Non-Commercial Use**: Free for personal, educational, academic, research, and non-profit use
- **Commercial Use**: Requires a paid annual license agreement

See [LICENSE.md](LICENSE.md) for full details.

For commercial licensing inquiries, contact the repository owner.
