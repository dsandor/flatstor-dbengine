# Flatstor DBEngine

A high-performance database engine for querying massively wide datasets (10,000+ columns) with TSQL-style syntax, column group namespacing, and O(1) row lookups.

## Technology Stack

- **Language:** Go
- **SQL Engine:** Embedded DuckDB (via go-duckdb)
- **Storage Format:** JSON files (one file per column group per row)
- **Storage Backend:** Disk (initially), S3 (future)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Client Layer                           │
│                   CLI Interface / API                       │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│                Query Translation Layer                      │
│         SQL Parser (Column Group Syntax)                    │
│         Query Translator (Expands to DuckDB SQL)            │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│                    DuckDB Engine                            │
│              Embedded DuckDB (Query Execution)              │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│                Column Group Manager                         │
│         Column Group Registry / Row Assembler               │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│                 Storage Abstraction                         │
│              Disk Storage / S3 Storage (Future)             │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│                   Data Files (JSON)                         │
│     _schema.json / Row JSON Files / Per column group        │
└─────────────────────────────────────────────────────────────┘
```

## Project Structure

```
dbengine/
├── cmd/
│   └── dbengine/
│       └── main.go              # CLI entry point
├── pkg/
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
├── scripts/
│   └── generate_data.py         # Test data generator
├── data/                        # Data directory
│   └── {table_name}/
│       ├── _schema.json         # Table schema
│       └── {row_id}/
│           ├── Common.json
│           ├── Bloomberg.json
│           └── ICE.json
├── go.mod
├── go.sum
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

# Show help
./dbengine -help
```

**Note:** On startup, the engine loads all tables into DuckDB. For large datasets (50k+ rows), use `--no-load` to skip this and manually load tables with `.load <table>`.

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

[Add license information here]
