# dbengine Python Client

A Python client for the dbengine HTTP API. Implements a subset of the Python DB-API 2.0 specification for familiar database interaction.

## Installation

```bash
# From the dbclients/python directory
pip install -e .
```

## Quick Start

```python
from dbclient import connect

# Connect to the database
conn = connect("http://localhost:8080", username="admin", password="secret")

# Execute a query
cursor = conn.cursor()
cursor.execute("SELECT * FROM my_table WHERE Bloomberg.Ticker = ?", ["AAPL"])

# Fetch results
for row in cursor.fetchall():
    print(row)

# Close connection
conn.close()
```

## Usage Examples

### Using Context Manager

```python
from dbclient import connect

with connect("http://localhost:8080") as conn:
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM instruments LIMIT 10")

    for row in cursor:
        print(row["Common_InstrumentID"], row["Bloomberg_Ticker"])
```

### Creating Tables

```python
from dbclient import connect, TableSchema, ColumnGroup, Column

conn = connect("http://localhost:8080")

schema = TableSchema(
    name="instruments",
    primary_key="Common.InstrumentID",
    column_groups=[
        ColumnGroup(
            name="Common",
            columns=[
                Column(name="InstrumentID", type="VARCHAR", primary_key=True),
                Column(name="Name", type="VARCHAR"),
            ]
        ),
        ColumnGroup(
            name="Bloomberg",
            columns=[
                Column(name="ISIN", type="VARCHAR", indexed=True),
                Column(name="Ticker", type="VARCHAR", indexed=True),
            ]
        ),
    ]
)

conn.create_table(schema)
conn.close()
```

### Inserting Data

```python
from dbclient import connect

conn = connect("http://localhost:8080")

# Using groups format
conn.insert_row(
    table_name="instruments",
    row_id="abc123-uuid",
    groups={
        "Common": {
            "InstrumentID": "abc123-uuid",
            "Name": "Apple Inc"
        },
        "Bloomberg": {
            "ISIN": "US0378331005",
            "Ticker": "AAPL"
        }
    }
)

# Or using flat data format
conn.insert_row(
    table_name="instruments",
    row_id="def456-uuid",
    data={
        "Common.InstrumentID": "def456-uuid",
        "Common.Name": "Microsoft Corp",
        "Bloomberg.ISIN": "US5949181045",
        "Bloomberg.Ticker": "MSFT"
    }
)

conn.close()
```

### Parameterized Queries

```python
from dbclient import connect

conn = connect("http://localhost:8080")
cursor = conn.cursor()

# SELECT with parameters (currently processed by the engine)
cursor.execute("SELECT * FROM instruments WHERE Bloomberg.Ticker = 'AAPL'")

# INSERT with parameters
cursor.execute(
    "INSERT INTO instruments (Common_InstrumentID, Common_Name) VALUES (?, ?)",
    ["new-id", "New Company"]
)

print(f"Rows affected: {cursor.rowcount}")

conn.close()
```

### Table Management

```python
from dbclient import connect

conn = connect("http://localhost:8080")

# List tables
tables = conn.list_tables()
print(f"Tables: {tables}")

# Get table schema
schema = conn.get_table_schema("instruments")
print(f"Schema: {schema}")

# Load table into DuckDB
conn.load_table("instruments")

# Sync table with DuckDB
conn.sync_table("instruments")

# Drop table
conn.drop_table("old_table")

conn.close()
```

### Row Operations

```python
from dbclient import connect

conn = connect("http://localhost:8080")

# Get a row
row = conn.get_row("instruments", "abc123-uuid")
print(row)

# Update a row
conn.update_row(
    table_name="instruments",
    row_id="abc123-uuid",
    groups={
        "Bloomberg": {"Ticker": "AAPL-US"}
    }
)

# Delete a row
conn.delete_row("instruments", "abc123-uuid")

conn.close()
```

### Health Check

```python
from dbclient import connect

conn = connect("http://localhost:8080")

if conn.health():
    print("Server is healthy")
else:
    print("Server is not responding")

conn.close()
```

## Error Handling

```python
from dbclient import connect, DatabaseError, AuthenticationError

try:
    conn = connect("http://localhost:8080", username="admin", password="wrong")
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM instruments")
except AuthenticationError:
    print("Invalid credentials")
except DatabaseError as e:
    print(f"Database error: {e}")
finally:
    conn.close()
```

## DB-API 2.0 Compliance

This client implements a subset of PEP 249 (Python Database API Specification v2.0):

- `connect()` - Module level connect function
- `Connection` - Connection class with `cursor()`, `close()`, `commit()`, `rollback()`
- `Cursor` - Cursor class with `execute()`, `executemany()`, `fetchone()`, `fetchmany()`, `fetchall()`
- Standard exception hierarchy

Module level attributes:
- `apilevel = "2.0"`
- `threadsafety = 1` (Threads may share module but not connections)
- `paramstyle = "qmark"` (Question mark style: `WHERE col = ?`)
