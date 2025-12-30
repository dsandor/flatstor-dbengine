"""
dbengine Python Client

A Python client for the dbengine HTTP API.
"""

from .dbclient import (
    # Main entry point
    connect,
    Connection,
    Cursor,

    # Data types
    TableSchema,
    ColumnGroup,
    Column,
    QueryResult,
    ExecuteResult,

    # Exceptions
    Error,
    InterfaceError,
    DatabaseError,
    OperationalError,
    ProgrammingError,
    AuthenticationError,

    # DB-API 2.0 globals
    apilevel,
    threadsafety,
    paramstyle,
)

__all__ = [
    "connect",
    "Connection",
    "Cursor",
    "TableSchema",
    "ColumnGroup",
    "Column",
    "QueryResult",
    "ExecuteResult",
    "Error",
    "InterfaceError",
    "DatabaseError",
    "OperationalError",
    "ProgrammingError",
    "AuthenticationError",
    "apilevel",
    "threadsafety",
    "paramstyle",
]

__version__ = "1.0.0"
