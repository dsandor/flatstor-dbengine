#!/usr/bin/env python3
"""
Clean up all generated data for flatstor dbengine.

This script removes:
- All data files in the data directory
- All DuckDB database files (.duckdb, .duckdb.wal)

Usage:
    python clean_data.py                    # Clean default ./data directory
    python clean_data.py --data-path ./mydata
    python clean_data.py --dry-run          # Show what would be deleted
    python clean_data.py --keep-duckdb      # Only clean data files, keep DuckDB
"""

import argparse
import os
import shutil
from pathlib import Path


def find_duckdb_files(base_path: Path) -> list[Path]:
    """Find all DuckDB database files."""
    duckdb_files = []
    for pattern in ["*.duckdb", "*.duckdb.wal"]:
        duckdb_files.extend(base_path.glob(pattern))
    return sorted(duckdb_files)


def get_dir_size(path: Path) -> int:
    """Get total size of a directory in bytes."""
    total = 0
    try:
        for entry in path.rglob("*"):
            if entry.is_file():
                total += entry.stat().st_size
    except (OSError, PermissionError):
        pass
    return total


def format_size(size_bytes: int) -> str:
    """Format bytes as human-readable string."""
    for unit in ["B", "KB", "MB", "GB", "TB"]:
        if size_bytes < 1024:
            return f"{size_bytes:.1f} {unit}"
        size_bytes /= 1024
    return f"{size_bytes:.1f} PB"


def count_files(path: Path) -> int:
    """Count all files in a directory."""
    count = 0
    try:
        for entry in path.rglob("*"):
            if entry.is_file():
                count += 1
    except (OSError, PermissionError):
        pass
    return count


def main():
    parser = argparse.ArgumentParser(
        description="Clean up all generated data for flatstor dbengine"
    )
    parser.add_argument(
        "--data-path", "-d",
        type=str,
        default="./data",
        help="Data directory path (default: ./data)"
    )
    parser.add_argument(
        "--dry-run", "-n",
        action="store_true",
        help="Show what would be deleted without actually deleting"
    )
    parser.add_argument(
        "--keep-duckdb",
        action="store_true",
        help="Keep DuckDB database files, only clean data directory"
    )
    parser.add_argument(
        "--force", "-f",
        action="store_true",
        help="Skip confirmation prompt"
    )

    args = parser.parse_args()

    data_path = Path(args.data_path)
    base_path = Path(".")

    # Find items to delete
    items_to_delete = []

    # Check data directory
    if data_path.exists() and data_path.is_dir():
        size = get_dir_size(data_path)
        file_count = count_files(data_path)
        items_to_delete.append({
            "type": "directory",
            "path": data_path,
            "size": size,
            "file_count": file_count
        })

    # Check DuckDB files
    if not args.keep_duckdb:
        for duckdb_file in find_duckdb_files(base_path):
            items_to_delete.append({
                "type": "file",
                "path": duckdb_file,
                "size": duckdb_file.stat().st_size,
                "file_count": 1
            })

    if not items_to_delete:
        print("Nothing to clean up.")
        return

    # Show what will be deleted
    total_size = sum(item["size"] for item in items_to_delete)
    total_files = sum(item["file_count"] for item in items_to_delete)

    print("Items to delete:")
    for item in items_to_delete:
        if item["type"] == "directory":
            print(f"  [DIR]  {item['path']} ({item['file_count']:,} files, {format_size(item['size'])})")
        else:
            print(f"  [FILE] {item['path']} ({format_size(item['size'])})")

    print()
    print(f"Total: {total_files:,} files, {format_size(total_size)}")
    print()

    if args.dry_run:
        print("Dry run - no files were deleted.")
        return

    # Confirm deletion
    if not args.force:
        response = input("Are you sure you want to delete these items? [y/N] ")
        if response.lower() not in ["y", "yes"]:
            print("Aborted.")
            return

    # Delete items
    print("Deleting...")
    for item in items_to_delete:
        try:
            if item["type"] == "directory":
                shutil.rmtree(item["path"])
                print(f"  Deleted directory: {item['path']}")
            else:
                item["path"].unlink()
                print(f"  Deleted file: {item['path']}")
        except Exception as e:
            print(f"  Error deleting {item['path']}: {e}")

    print()
    print("Cleanup complete.")


if __name__ == "__main__":
    main()
