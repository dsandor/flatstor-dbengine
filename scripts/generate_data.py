#!/usr/bin/env python3
"""
Generate test data for flatstor dbengine.

Usage:
    python generate_data.py --rows 1000 --columns 100
    python generate_data.py --rows 100 --columns 10000 --groups 50
    python generate_data.py --rows 10 --columns 20 --table my_table

For large datasets (to avoid memory issues), use batch mode:
    python generate_data.py --rows 500000 --columns 2000 --batch-size 1000
"""

import argparse
import gc
import json
import os
import random
import string
import uuid
from datetime import datetime, timedelta
from pathlib import Path


def random_string(length: int = 10) -> str:
    """Generate a random string."""
    return ''.join(random.choices(string.ascii_uppercase + string.digits, k=length))


def random_isin() -> str:
    """Generate a random ISIN-like identifier."""
    country = random.choice(['US', 'GB', 'DE', 'FR', 'JP', 'CH'])
    return f"{country}{random_string(10)}"


def random_ticker() -> str:
    """Generate a random ticker symbol."""
    length = random.randint(2, 5)
    return ''.join(random.choices(string.ascii_uppercase, k=length))


def random_value(col_type: str):
    """Generate a random value based on column type."""
    if col_type == "VARCHAR":
        return random_string(random.randint(5, 20))
    elif col_type == "INTEGER":
        return random.randint(-1000000, 1000000)
    elif col_type == "BIGINT":
        return random.randint(-10000000000, 10000000000)
    elif col_type == "DOUBLE":
        return round(random.uniform(-1000000, 1000000), 4)
    elif col_type == "BOOLEAN":
        return random.choice([True, False])
    elif col_type == "DATE":
        base = datetime(2020, 1, 1)
        delta = timedelta(days=random.randint(0, 1500))
        return (base + delta).strftime("%Y-%m-%d")
    elif col_type == "TIMESTAMP":
        base = datetime(2020, 1, 1)
        delta = timedelta(seconds=random.randint(0, 126230400))
        return (base + delta).isoformat()
    else:
        return random_string(10)


def generate_schema(table_name: str, num_columns: int, num_groups: int) -> dict:
    """Generate a table schema with the specified number of columns distributed across groups."""

    # Calculate columns per group
    cols_per_group = max(1, num_columns // num_groups)
    remaining = num_columns - (cols_per_group * num_groups)

    # Column types to use
    col_types = ["VARCHAR", "INTEGER", "DOUBLE", "BOOLEAN", "DATE"]

    column_groups = []
    col_index = 0

    for g in range(num_groups):
        group_name = f"Group{g:03d}"

        # First group is "Common" with the primary key
        if g == 0:
            group_name = "Common"
            columns = [
                {"name": "InstrumentID", "type": "VARCHAR", "primary_key": True}
            ]
            col_index += 1
            group_cols = cols_per_group - 1
        else:
            columns = []
            group_cols = cols_per_group

            # Add extra columns to earlier groups if there's remainder
            if g <= remaining:
                group_cols += 1

        # Generate columns for this group
        for c in range(group_cols):
            col_type = random.choice(col_types)
            col_name = f"Col{col_index:05d}"

            # Make some columns indexed (about 5%)
            indexed = random.random() < 0.05

            columns.append({
                "name": col_name,
                "type": col_type,
                "indexed": indexed
            })
            col_index += 1

        if columns:  # Only add groups with columns
            column_groups.append({
                "name": group_name,
                "columns": columns
            })

    return {
        "name": table_name,
        "primary_key": "Common.InstrumentID",
        "column_groups": column_groups
    }


def generate_and_write_row_streaming(data_path: Path, table_name: str, schema: dict):
    """Generate and write a row directly to disk without holding full row in memory."""
    row_id = str(uuid.uuid4())
    row_dir = data_path / table_name / row_id
    row_dir.mkdir(parents=True, exist_ok=True)

    for group in schema["column_groups"]:
        group_name = group["name"]
        group_file = row_dir / f"{group_name}.json"

        # Write directly to file using a streaming approach
        with open(group_file, 'w') as f:
            f.write('{\n')
            first = True
            for col in group["columns"]:
                col_name = col["name"]
                col_type = col["type"]

                if not first:
                    f.write(',\n')
                first = False

                if col_name == "InstrumentID":
                    value = row_id
                else:
                    value = random_value(col_type)

                # Write key-value pair
                f.write(f'  "{col_name}": {json.dumps(value)}')
            f.write('\n}')

    return row_id


def generate_row(schema: dict) -> dict:
    """Generate a single row of data based on the schema (legacy, memory-intensive)."""
    row_id = str(uuid.uuid4())
    groups = {}

    for group in schema["column_groups"]:
        group_name = group["name"]
        group_data = {}

        for col in group["columns"]:
            col_name = col["name"]
            col_type = col["type"]

            if col_name == "InstrumentID":
                group_data[col_name] = row_id
            else:
                group_data[col_name] = random_value(col_type)

        groups[group_name] = group_data

    return {
        "_id": row_id,
        "groups": groups
    }


def generate_batch_streaming(data_path: Path, table_name: str, schema: dict, batch_num: int, batch_size: int):
    """Generate a batch of rows and write to batched files."""
    batch_dir = data_path / table_name / f"batch_{batch_num:06d}"
    batch_dir.mkdir(parents=True, exist_ok=True)

    # Generate row IDs upfront
    row_ids = [str(uuid.uuid4()) for _ in range(batch_size)]

    # Write each group's data for all rows in the batch to a single file
    for group in schema["column_groups"]:
        group_name = group["name"]
        group_file = batch_dir / f"{group_name}.jsonl"

        with open(group_file, 'w') as f:
            for row_idx, row_id in enumerate(row_ids):
                row_data = {}
                for col in group["columns"]:
                    col_name = col["name"]
                    col_type = col["type"]

                    if col_name == "InstrumentID":
                        row_data[col_name] = row_id
                    else:
                        row_data[col_name] = random_value(col_type)

                # Add row ID for reference
                row_data["_row_id"] = row_id
                f.write(json.dumps(row_data) + '\n')

                # Clear row_data to help GC
                row_data = None

    return row_ids


def write_schema(data_path: Path, schema: dict):
    """Write the schema file."""
    table_dir = data_path / schema["name"]
    table_dir.mkdir(parents=True, exist_ok=True)

    schema_file = table_dir / "_schema.json"
    schema["created_at"] = datetime.now().astimezone().isoformat()
    schema["updated_at"] = schema["created_at"]

    with open(schema_file, 'w') as f:
        json.dump(schema, f, indent=2)

    print(f"Created schema: {schema_file}")


def write_row(data_path: Path, table_name: str, row: dict):
    """Write a row's data files."""
    row_dir = data_path / table_name / row["_id"]
    row_dir.mkdir(parents=True, exist_ok=True)

    for group_name, group_data in row["groups"].items():
        group_file = row_dir / f"{group_name}.json"
        with open(group_file, 'w') as f:
            json.dump(group_data, f, indent=2)


def main():
    parser = argparse.ArgumentParser(
        description="Generate test data for flatstor dbengine"
    )
    parser.add_argument(
        "--rows", "-r",
        type=int,
        default=100,
        help="Number of rows to generate (default: 100)"
    )
    parser.add_argument(
        "--columns", "-c",
        type=int,
        default=50,
        help="Total number of columns to generate (default: 50)"
    )
    parser.add_argument(
        "--groups", "-g",
        type=int,
        default=None,
        help="Number of column groups (default: columns/10, min 3)"
    )
    parser.add_argument(
        "--table", "-t",
        type=str,
        default="test_table",
        help="Table name (default: test_table)"
    )
    parser.add_argument(
        "--data-path", "-d",
        type=str,
        default="./data",
        help="Data directory path (default: ./data)"
    )
    parser.add_argument(
        "--seed", "-s",
        type=int,
        default=None,
        help="Random seed for reproducible data"
    )
    parser.add_argument(
        "--batch-size", "-b",
        type=int,
        default=1,
        help="Number of rows per batch file (default: 1, one file per row). "
             "Use higher values like 1000 for large datasets to reduce file count."
    )

    args = parser.parse_args()

    # Set random seed if provided
    if args.seed is not None:
        random.seed(args.seed)

    # Calculate number of groups
    num_groups = args.groups
    if num_groups is None:
        num_groups = max(3, args.columns // 10)

    # Ensure we don't have more groups than columns
    num_groups = min(num_groups, args.columns)

    data_path = Path(args.data_path)

    print(f"Generating data for table: {args.table}")
    print(f"  Rows: {args.rows:,}")
    print(f"  Columns: {args.columns:,}")
    print(f"  Column Groups: {num_groups}")
    print(f"  Data Path: {data_path.absolute()}")
    print()

    # Generate schema
    print("Generating schema...")
    schema = generate_schema(args.table, args.columns, num_groups)
    write_schema(data_path, schema)

    # Count actual columns
    total_cols = sum(len(g["columns"]) for g in schema["column_groups"])
    print(f"  Total columns created: {total_cols}")
    print()

    # Generate rows using streaming to minimize memory usage
    print(f"Generating {args.rows:,} rows...")
    if args.batch_size > 1:
        print(f"  Using batch mode: {args.batch_size} rows per batch file")
    start_time = datetime.now()

    if args.batch_size > 1:
        # Batch mode: write multiple rows per file to reduce file count
        num_batches = (args.rows + args.batch_size - 1) // args.batch_size
        rows_generated = 0

        for batch_num in range(num_batches):
            # Calculate actual batch size (last batch may be smaller)
            current_batch_size = min(args.batch_size, args.rows - rows_generated)

            generate_batch_streaming(data_path, args.table, schema, batch_num, current_batch_size)
            rows_generated += current_batch_size

            # Garbage collection after each batch
            gc.collect()

            # Progress update
            if (batch_num + 1) % max(1, num_batches // 20) == 0 or batch_num == num_batches - 1:
                elapsed = (datetime.now() - start_time).total_seconds()
                rate = rows_generated / elapsed if elapsed > 0 else 0
                print(f"  {rows_generated:,} / {args.rows:,} rows ({rate:.1f} rows/sec)")
    else:
        # Single row mode: one file per row (original behavior, streaming)
        gc_interval = max(1000, args.rows // 100)
        progress_interval = max(100, args.rows // 10)

        for i in range(args.rows):
            generate_and_write_row_streaming(data_path, args.table, schema)

            # Periodic garbage collection to prevent memory buildup
            if (i + 1) % gc_interval == 0:
                gc.collect()

            # Progress update
            if (i + 1) % progress_interval == 0:
                elapsed = (datetime.now() - start_time).total_seconds()
                rate = (i + 1) / elapsed if elapsed > 0 else 0
                print(f"  {i + 1:,} / {args.rows:,} rows ({rate:.1f} rows/sec)")

    elapsed = (datetime.now() - start_time).total_seconds()
    print()
    print(f"Done! Generated {args.rows:,} rows in {elapsed:.2f} seconds")
    print(f"  Rate: {args.rows / elapsed:.1f} rows/sec")
    print()
    print("To load this data into dbengine:")
    print(f"  ./dbengine -data {data_path}")
    print(f"  dbengine> .load {args.table}")
    print(f"  dbengine> SELECT * FROM {args.table} LIMIT 10")


if __name__ == "__main__":
    main()
