"""
dbengine Python Client

A Python client for the dbengine HTTP API with connection pooling and JWT token caching.

Features:
- Connection pooling for high performance
- JWT token authentication with automatic refresh
- DB-API 2.0 compatible interface

Example usage:
    from dbclient import connect

    # Connect to the database (uses token auth by default)
    conn = connect("http://localhost:8080", username="admin", password="secret")

    # Execute a query
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM my_table WHERE Bloomberg.Ticker = ?", ["AAPL"])

    for row in cursor.fetchall():
        print(row)

    conn.close()
"""

import json
import threading
import time
from typing import Any, Dict, List, Optional, Tuple
from dataclasses import dataclass, field
from urllib.parse import urljoin, quote
import http.client
import base64
import socket


class Error(Exception):
    """Base exception for dbclient errors."""
    pass


class InterfaceError(Error):
    """Exception for interface errors."""
    pass


class DatabaseError(Error):
    """Exception for database errors."""
    pass


class OperationalError(DatabaseError):
    """Exception for operational errors."""
    pass


class ProgrammingError(DatabaseError):
    """Exception for programming errors."""
    pass


class AuthenticationError(Error):
    """Exception for authentication errors."""
    pass


@dataclass
class Column:
    """Represents a column definition."""
    name: str
    type: str
    primary_key: bool = False
    indexed: bool = False
    nullable: bool = True


@dataclass
class ColumnGroup:
    """Represents a group of columns."""
    name: str
    columns: List[Column] = field(default_factory=list)


@dataclass
class TableSchema:
    """Represents a table schema."""
    name: str
    primary_key: str
    column_groups: List[ColumnGroup] = field(default_factory=list)
    created_at: Optional[str] = None
    updated_at: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary for API request."""
        return {
            "name": self.name,
            "primary_key": self.primary_key,
            "column_groups": [
                {
                    "name": cg.name,
                    "columns": [
                        {
                            "name": c.name,
                            "type": c.type,
                            "primary_key": c.primary_key,
                            "indexed": c.indexed,
                            "nullable": c.nullable,
                        }
                        for c in cg.columns
                    ]
                }
                for cg in self.column_groups
            ]
        }


@dataclass
class QueryResult:
    """Represents the result of a query."""
    columns: List[str]
    rows: List[Dict[str, Any]]
    row_count: int
    execution_ms: int
    translated_sql: Optional[str] = None


@dataclass
class ExecuteResult:
    """Represents the result of an execute operation."""
    success: bool
    message: str
    rows_affected: int
    execution_ms: int


class ConnectionPool:
    """
    HTTP connection pool for efficient connection reuse.

    Maintains a pool of persistent HTTP connections to avoid
    the overhead of establishing new connections for each request.
    """

    def __init__(
        self,
        host: str,
        port: int,
        use_ssl: bool = False,
        max_connections: int = 10,
        timeout: float = 30.0,
    ):
        self._host = host
        self._port = port
        self._use_ssl = use_ssl
        self._max_connections = max_connections
        self._timeout = timeout
        self._pool: List[http.client.HTTPConnection] = []
        self._lock = threading.Lock()

    def get_connection(self) -> http.client.HTTPConnection:
        """Get a connection from the pool or create a new one."""
        with self._lock:
            if self._pool:
                conn = self._pool.pop()
                # Test if connection is still valid
                try:
                    conn.sock.setblocking(False)
                    try:
                        data = conn.sock.recv(1)
                        if not data:
                            # Connection closed by server
                            conn.close()
                            conn = None
                    except (socket.error, BlockingIOError):
                        # No data available, connection is good
                        conn.sock.setblocking(True)
                except (socket.error, AttributeError, OSError):
                    conn = None

                if conn is not None:
                    return conn

        # Create new connection
        if self._use_ssl:
            conn = http.client.HTTPSConnection(
                self._host, self._port, timeout=self._timeout
            )
        else:
            conn = http.client.HTTPConnection(
                self._host, self._port, timeout=self._timeout
            )
        return conn

    def return_connection(self, conn: http.client.HTTPConnection) -> None:
        """Return a connection to the pool."""
        with self._lock:
            if len(self._pool) < self._max_connections:
                self._pool.append(conn)
            else:
                conn.close()

    def close_all(self) -> None:
        """Close all connections in the pool."""
        with self._lock:
            for conn in self._pool:
                try:
                    conn.close()
                except Exception:
                    pass
            self._pool.clear()


class TokenManager:
    """
    Manages JWT token authentication with automatic refresh.

    - Caches access and refresh tokens
    - Automatically refreshes tokens before expiry
    - Thread-safe token access
    """

    def __init__(
        self,
        refresh_threshold: float = 300.0,  # 5 minutes before expiry
    ):
        self._access_token: Optional[str] = None
        self._refresh_token: Optional[str] = None
        self._expires_at: float = 0
        self._refresh_threshold = refresh_threshold
        self._lock = threading.RLock()

    def set_tokens(
        self,
        access_token: str,
        refresh_token: str,
        expires_at: int,
    ) -> None:
        """Set tokens from login/refresh response."""
        with self._lock:
            self._access_token = access_token
            self._refresh_token = refresh_token
            self._expires_at = float(expires_at)

    def get_access_token(self) -> Optional[str]:
        """Get the current access token."""
        with self._lock:
            return self._access_token

    def get_refresh_token(self) -> Optional[str]:
        """Get the current refresh token."""
        with self._lock:
            return self._refresh_token

    def needs_refresh(self) -> bool:
        """Check if the token needs to be refreshed."""
        with self._lock:
            if not self._access_token:
                return True
            return time.time() + self._refresh_threshold >= self._expires_at

    def has_token(self) -> bool:
        """Check if we have a valid token."""
        with self._lock:
            return self._access_token is not None

    def clear(self) -> None:
        """Clear all tokens."""
        with self._lock:
            self._access_token = None
            self._refresh_token = None
            self._expires_at = 0


class Cursor:
    """
    Database cursor for executing queries.

    Implements a subset of the Python DB-API 2.0 specification.
    """

    def __init__(self, connection: 'Connection'):
        self._connection = connection
        self._columns: List[str] = []
        self._rows: List[Dict[str, Any]] = []
        self._row_index: int = 0
        self._rowcount: int = -1
        self._description: Optional[List[Tuple]] = None
        self._lastrowid: Optional[str] = None

    @property
    def description(self) -> Optional[List[Tuple]]:
        """
        Returns column descriptions as per DB-API 2.0.
        Each tuple: (name, type_code, display_size, internal_size, precision, scale, null_ok)
        """
        return self._description

    @property
    def rowcount(self) -> int:
        """Returns the number of rows affected by the last operation."""
        return self._rowcount

    @property
    def lastrowid(self) -> Optional[str]:
        """Returns the ID of the last inserted row."""
        return self._lastrowid

    def execute(self, sql: str, parameters: Optional[List[Any]] = None) -> 'Cursor':
        """
        Execute a SQL statement.

        Args:
            sql: The SQL statement to execute
            parameters: Optional list of parameters for parameterized queries

        Returns:
            Self for method chaining
        """
        if parameters is None:
            parameters = []

        sql_upper = sql.strip().upper()

        if sql_upper.startswith("SELECT"):
            result = self._connection._query(sql, parameters)
            self._columns = result.columns
            self._rows = result.rows
            self._rowcount = result.row_count
            self._row_index = 0

            # Build description
            self._description = [
                (col, None, None, None, None, None, True)
                for col in result.columns
            ]
        else:
            result = self._connection._execute(sql, parameters)
            self._columns = []
            self._rows = []
            self._rowcount = result.rows_affected
            self._description = None

        return self

    def executemany(self, sql: str, seq_of_parameters: List[List[Any]]) -> 'Cursor':
        """
        Execute a SQL statement with multiple parameter sets.

        Args:
            sql: The SQL statement to execute
            seq_of_parameters: List of parameter lists

        Returns:
            Self for method chaining
        """
        total_affected = 0
        for params in seq_of_parameters:
            self.execute(sql, params)
            if self._rowcount > 0:
                total_affected += self._rowcount

        self._rowcount = total_affected
        return self

    def fetchone(self) -> Optional[Dict[str, Any]]:
        """Fetch the next row from the result set."""
        if self._row_index >= len(self._rows):
            return None

        row = self._rows[self._row_index]
        self._row_index += 1
        return row

    def fetchmany(self, size: Optional[int] = None) -> List[Dict[str, Any]]:
        """Fetch multiple rows from the result set."""
        if size is None:
            size = 1

        rows = []
        for _ in range(size):
            row = self.fetchone()
            if row is None:
                break
            rows.append(row)

        return rows

    def fetchall(self) -> List[Dict[str, Any]]:
        """Fetch all remaining rows from the result set."""
        rows = self._rows[self._row_index:]
        self._row_index = len(self._rows)
        return rows

    def close(self) -> None:
        """Close the cursor."""
        self._columns = []
        self._rows = []
        self._row_index = 0

    def __iter__(self):
        """Allow iteration over rows."""
        return self

    def __next__(self) -> Dict[str, Any]:
        """Get next row during iteration."""
        row = self.fetchone()
        if row is None:
            raise StopIteration
        return row


class Connection:
    """
    Database connection to dbengine with connection pooling and token caching.

    Features:
    - Connection pooling for efficient HTTP connection reuse
    - JWT token authentication with automatic refresh
    - Thread-safe operation

    Implements a subset of the Python DB-API 2.0 specification.
    """

    def __init__(
        self,
        host: str,
        port: int = 8080,
        username: Optional[str] = None,
        password: Optional[str] = None,
        use_ssl: bool = False,
        timeout: float = 30.0,
        use_token_auth: bool = True,
        auto_refresh: bool = True,
        refresh_threshold: float = 300.0,
        max_connections: int = 10,
    ):
        """
        Initialize a connection to dbengine.

        Args:
            host: The hostname or IP address
            port: The port number
            username: Username for authentication
            password: Password for authentication
            use_ssl: Whether to use HTTPS
            timeout: Request timeout in seconds
            use_token_auth: Use JWT tokens instead of Basic Auth (default: True)
            auto_refresh: Automatically refresh tokens before expiry (default: True)
            refresh_threshold: Seconds before expiry to trigger refresh (default: 300)
            max_connections: Maximum pooled connections (default: 10)
        """
        self._host = host
        self._port = port
        self._username = username
        self._password = password
        self._use_ssl = use_ssl
        self._timeout = timeout
        self._closed = False

        # Token authentication settings
        self._use_token_auth = use_token_auth and (username is not None and password is not None)
        self._auto_refresh = auto_refresh

        # Connection pool for HTTP connection reuse
        self._pool = ConnectionPool(
            host=host,
            port=port,
            use_ssl=use_ssl,
            max_connections=max_connections,
            timeout=timeout,
        )

        # Token manager for JWT token caching
        self._token_manager = TokenManager(refresh_threshold=refresh_threshold)

        # Lock for thread-safe login/refresh
        self._auth_lock = threading.Lock()

    def _ensure_valid_token(self) -> None:
        """Ensure we have a valid access token, logging in or refreshing as needed."""
        if not self._use_token_auth:
            return

        if not self._token_manager.needs_refresh():
            return

        with self._auth_lock:
            # Double-check after acquiring lock
            if not self._token_manager.needs_refresh():
                return

            if self._token_manager.has_token():
                # Try to refresh
                try:
                    self._refresh_tokens()
                    return
                except Exception:
                    # Refresh failed, try login
                    pass

            # Login to get new tokens
            self._login()

    def _login(self) -> None:
        """Authenticate and get new tokens."""
        if not self._username or not self._password:
            raise AuthenticationError("Username and password required")

        result = self._make_request_no_auth(
            "POST",
            "/api/v1/auth/login",
            {"username": self._username, "password": self._password},
        )

        self._token_manager.set_tokens(
            access_token=result["access_token"],
            refresh_token=result["refresh_token"],
            expires_at=result["expires_at"],
        )

    def _refresh_tokens(self) -> None:
        """Refresh tokens using the refresh token."""
        refresh_token = self._token_manager.get_refresh_token()
        if not refresh_token:
            raise AuthenticationError("No refresh token available")

        result = self._make_request_no_auth(
            "POST",
            "/api/v1/auth/refresh",
            {"refresh_token": refresh_token},
        )

        self._token_manager.set_tokens(
            access_token=result["access_token"],
            refresh_token=result["refresh_token"],
            expires_at=result["expires_at"],
        )

    def _make_request(
        self,
        method: str,
        path: str,
        body: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """Make an authenticated HTTP request to the API."""
        if self._closed:
            raise InterfaceError("Connection is closed")

        # Ensure we have a valid token
        if self._use_token_auth:
            self._ensure_valid_token()

        conn = self._pool.get_connection()
        try:
            # Build headers
            headers = {
                "Content-Type": "application/json",
                "Accept": "application/json",
                "Connection": "keep-alive",
            }

            # Add authentication
            if self._use_token_auth:
                token = self._token_manager.get_access_token()
                headers["Authorization"] = f"Bearer {token}"
            elif self._username and self._password:
                credentials = f"{self._username}:{self._password}"
                encoded = base64.b64encode(credentials.encode()).decode()
                headers["Authorization"] = f"Basic {encoded}"

            # Make request
            body_json = json.dumps(body) if body else None
            conn.request(method, path, body=body_json, headers=headers)

            # Get response
            response = conn.getresponse()
            response_body = response.read().decode()

            # Handle 401 - try to refresh and retry
            if response.status == 401 and self._use_token_auth:
                self._pool.return_connection(conn)
                conn = None

                with self._auth_lock:
                    self._refresh_tokens()

                # Retry with new token
                return self._make_request(method, path, body)

            if response_body:
                result = json.loads(response_body)
            else:
                result = {}

            if response.status >= 400:
                error_msg = result.get("message", result.get("error", "Unknown error"))
                raise DatabaseError(f"API error ({response.status}): {error_msg}")

            # Return connection to pool
            self._pool.return_connection(conn)
            conn = None

            return result

        except http.client.HTTPException as e:
            raise OperationalError(f"HTTP error: {e}")
        finally:
            if conn is not None:
                try:
                    conn.close()
                except Exception:
                    pass

    def _make_request_no_auth(
        self,
        method: str,
        path: str,
        body: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """Make an HTTP request without authentication."""
        conn = self._pool.get_connection()
        try:
            headers = {
                "Content-Type": "application/json",
                "Accept": "application/json",
                "Connection": "keep-alive",
            }

            body_json = json.dumps(body) if body else None
            conn.request(method, path, body=body_json, headers=headers)

            response = conn.getresponse()
            response_body = response.read().decode()

            if response.status == 401:
                raise AuthenticationError("Invalid credentials")

            if response_body:
                result = json.loads(response_body)
            else:
                result = {}

            if response.status >= 400:
                error_msg = result.get("message", result.get("error", "Unknown error"))
                raise DatabaseError(f"API error ({response.status}): {error_msg}")

            self._pool.return_connection(conn)
            conn = None

            return result

        except http.client.HTTPException as e:
            raise OperationalError(f"HTTP error: {e}")
        finally:
            if conn is not None:
                try:
                    conn.close()
                except Exception:
                    pass

    def _query(self, sql: str, params: List[Any]) -> QueryResult:
        """Execute a SELECT query."""
        result = self._make_request("POST", "/api/v1/query", {
            "sql": sql,
            "params": params,
        })

        return QueryResult(
            columns=result.get("columns", []),
            rows=result.get("rows", []),
            row_count=result.get("row_count", 0),
            execution_ms=result.get("execution_ms", 0),
            translated_sql=result.get("translated_sql"),
        )

    def _execute(self, sql: str, params: List[Any]) -> ExecuteResult:
        """Execute a non-SELECT statement."""
        result = self._make_request("POST", "/api/v1/execute", {
            "sql": sql,
            "params": params,
        })

        return ExecuteResult(
            success=result.get("success", False),
            message=result.get("message", ""),
            rows_affected=result.get("rows_affected", 0),
            execution_ms=result.get("execution_ms", 0),
        )

    def login(self) -> None:
        """
        Explicitly login and cache tokens.

        This is called automatically on first request when using token auth.
        """
        with self._auth_lock:
            self._login()

    def logout(self) -> None:
        """Logout and revoke tokens."""
        access_token = self._token_manager.get_access_token()
        refresh_token = self._token_manager.get_refresh_token()

        self._token_manager.clear()

        if access_token or refresh_token:
            try:
                self._make_request_no_auth(
                    "POST",
                    "/api/v1/auth/logout",
                    {
                        "access_token": access_token or "",
                        "refresh_token": refresh_token or "",
                    },
                )
            except Exception:
                pass  # Best effort logout

    def cursor(self) -> Cursor:
        """Create a new cursor."""
        return Cursor(self)

    def commit(self) -> None:
        """Commit the current transaction (no-op for dbengine)."""
        pass

    def rollback(self) -> None:
        """Rollback the current transaction (no-op for dbengine)."""
        pass

    def close(self) -> None:
        """Close the connection and release resources."""
        self._closed = True
        self._pool.close_all()
        self._token_manager.clear()

    def health(self) -> bool:
        """Check if the server is healthy."""
        try:
            result = self._make_request_no_auth("GET", "/health")
            return result.get("status") == "ok"
        except Exception:
            return False

    def list_tables(self) -> List[str]:
        """List all tables."""
        result = self._make_request("GET", "/api/v1/tables")
        tables = result.get("tables")
        return tables if tables is not None else []

    def create_table(self, schema: TableSchema) -> None:
        """Create a new table."""
        self._make_request("POST", "/api/v1/tables", schema.to_dict())

    def drop_table(self, table_name: str) -> None:
        """Drop a table."""
        self._make_request("DELETE", f"/api/v1/tables/{quote(table_name)}")

    def get_table_schema(self, table_name: str) -> Dict[str, Any]:
        """Get the schema for a table."""
        return self._make_request("GET", f"/api/v1/tables/{quote(table_name)}")

    def load_table(self, table_name: str) -> None:
        """Load a table into DuckDB."""
        self._make_request("POST", f"/api/v1/tables/{quote(table_name)}/load")

    def sync_table(self, table_name: str) -> None:
        """Sync a table with DuckDB."""
        self._make_request("POST", f"/api/v1/tables/{quote(table_name)}/sync")

    def insert_row(
        self,
        table_name: str,
        row_id: str,
        groups: Optional[Dict[str, Dict[str, Any]]] = None,
        data: Optional[Dict[str, Any]] = None,
    ) -> None:
        """Insert a row into a table."""
        body: Dict[str, Any] = {"_id": row_id}
        if groups:
            body["groups"] = groups
        if data:
            body["data"] = data

        self._make_request("POST", f"/api/v1/rows/{quote(table_name)}", body)

    def get_row(self, table_name: str, row_id: str) -> Dict[str, Any]:
        """Get a row by ID."""
        return self._make_request(
            "GET", f"/api/v1/rows/{quote(table_name)}/{quote(row_id)}"
        )

    def update_row(
        self,
        table_name: str,
        row_id: str,
        groups: Optional[Dict[str, Dict[str, Any]]] = None,
        data: Optional[Dict[str, Any]] = None,
    ) -> None:
        """Update a row."""
        body: Dict[str, Any] = {}
        if groups:
            body["groups"] = groups
        if data:
            body["data"] = data

        self._make_request(
            "PUT", f"/api/v1/rows/{quote(table_name)}/{quote(row_id)}", body
        )

    def delete_row(self, table_name: str, row_id: str) -> None:
        """Delete a row."""
        self._make_request(
            "DELETE", f"/api/v1/rows/{quote(table_name)}/{quote(row_id)}"
        )

    def __enter__(self) -> 'Connection':
        """Context manager entry."""
        return self

    def __exit__(self, exc_type, exc_val, exc_tb) -> None:
        """Context manager exit."""
        self.close()


def connect(
    url: str,
    username: Optional[str] = None,
    password: Optional[str] = None,
    timeout: float = 30.0,
    use_token_auth: bool = True,
    auto_refresh: bool = True,
    max_connections: int = 10,
) -> Connection:
    """
    Connect to a dbengine server with optimized settings.

    Args:
        url: The server URL (e.g., "http://localhost:8080")
        username: Username for authentication
        password: Password for authentication
        timeout: Request timeout in seconds
        use_token_auth: Use JWT tokens instead of Basic Auth (default: True)
        auto_refresh: Automatically refresh tokens before expiry (default: True)
        max_connections: Maximum pooled connections (default: 10)

    Returns:
        A Connection object with connection pooling and token caching

    Example:
        # Simple usage - tokens are automatically managed
        conn = connect("http://localhost:8080", username="admin", password="secret")
        cursor = conn.cursor()
        cursor.execute("SELECT * FROM my_table")
        for row in cursor.fetchall():
            print(row)
        conn.close()

        # Disable token auth to use Basic Auth on every request
        conn = connect("http://localhost:8080", username="admin", password="secret",
                       use_token_auth=False)
    """
    # Parse URL
    if url.startswith("https://"):
        use_ssl = True
        url = url[8:]
    elif url.startswith("http://"):
        use_ssl = False
        url = url[7:]
    else:
        use_ssl = False

    # Parse host and port
    if ":" in url:
        host, port_str = url.split(":", 1)
        # Remove any path
        port_str = port_str.split("/")[0]
        port = int(port_str)
    else:
        host = url.split("/")[0]
        port = 443 if use_ssl else 80

    return Connection(
        host=host,
        port=port,
        username=username,
        password=password,
        use_ssl=use_ssl,
        timeout=timeout,
        use_token_auth=use_token_auth,
        auto_refresh=auto_refresh,
        max_connections=max_connections,
    )


# DB-API 2.0 module globals
apilevel = "2.0"
threadsafety = 1  # Threads may share module but not connections
paramstyle = "qmark"  # Question mark style parameters (?)
