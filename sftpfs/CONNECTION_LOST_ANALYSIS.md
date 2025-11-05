# SFTP Connection Lost Error Analysis

## Overview
This document analyzes where "connection lost" errors can occur in the sftpfs package and provides recommendations for implementing automatic reconnection.

## Current Architecture

### Connection Management
- **Location**: `sftpfs/sftp.go`
- **Primary Client**: Stored in `fileSystem.client` (line 70)
- **Connection Establishment**: `dial()` function (line 174-194)
- **Client Access**: `getClient()` method (line 214-241)

### Connection Lifecycle
1. **Initial Connection**: Created via `Dial()`, `DialAndRegister()`, or `EnsureRegistered()`
2. **Client Storage**: Stored in `fileSystem.client` field
3. **Connection Close**: `Close()` method (line 456-467) sets `client = nil`

## Where Connection Lost Errors Can Occur

### 1. File Operations
All file operations can experience connection loss:

- **`OpenReader()`** (line 397-399)
- **`OpenWriter()`** (line 401-403)
- **`OpenAppendWriter()`** (line 405-419)
- **`OpenReadWriter()`** (line 421-423)

**Error Path**: `openFile()` → `getClient()` → SFTP operation

### 2. Directory Operations

- **`MakeDir()`** (line 319-327)
- **`ListDirInfo()`** (line 342-374)
- **`Stat()`** (line 329-337)

**Error Path**: Method → `getClient()` → SFTP operation

### 3. File Management Operations

- **`Move()`** (line 436-444) - Rename operation
- **`Remove()`** (line 446-454) - Delete operation
- **`Truncate()`** (line 425-434) - Size modification

**Error Path**: Method → `getClient()` → SFTP operation

### 4. Long-Running Operations

File read/write operations through `sftpFile` (line 376-383) can lose connection during:
- Large file transfers
- Long-duration reads/writes
- Network interruptions mid-operation

## Types of Connection Errors

Based on SSH/SFTP behavior, connection lost errors typically manifest as:

1. **`io.EOF`** - Connection closed unexpectedly
2. **"connection reset by peer"** - TCP connection forcibly closed
3. **"broken pipe"** - Write to closed connection
4. **"use of closed network connection"** - Attempt to use closed socket
5. **SSH protocol errors** - Session/channel closed
6. **Timeout errors** - Read/write deadlines exceeded

## Current Error Handling

### Limitations
1. **No Retry Logic**: Errors are immediately returned to caller
2. **No Connection Health Checks**: No ping/keepalive mechanism
3. **Single Failure Point**: Connection loss causes complete operation failure
4. **No State Recovery**: Lost connections require manual reconnection

### Existing Fallback Mechanism
The `getClient()` method (line 214-241) has a fallback:
- If `f.client == nil`, attempts to dial using URL credentials
- Only works if username/password are in the URL
- Creates a temporary client that must be released
- **Limitation**: Only handles nil client, not broken connections

## Automatic Reconnection Strategies

### Strategy 1: Connection Health Monitoring (Proactive)

**Approach**: Detect broken connections before operations fail

```go
type fileSystem struct {
    client     *sftp.Client
    sshClient  *ssh.Client  // Keep reference to SSH client
    prefix     string
    mu         sync.Mutex

    // Connection params for reconnection
    host       string
    username   string
    password   string
    hostKeyCallback ssh.HostKeyCallback
}

func (f *fileSystem) isConnectionAlive() bool {
    if f.client == nil || f.sshClient == nil {
        return false
    }
    // Test connection with a lightweight operation
    _, err := f.client.Getwd()
    return err == nil
}
```

### Strategy 2: Retry with Exponential Backoff (Reactive)

**Approach**: Retry failed operations with automatic reconnection

```go
func (f *fileSystem) executeWithRetry(
    ctx context.Context,
    operation func(*sftp.Client) error,
    maxRetries int,
) error {
    var lastErr error
    backoff := 100 * time.Millisecond

    for attempt := 0; attempt <= maxRetries; attempt++ {
        if attempt > 0 {
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(backoff):
                backoff *= 2 // Exponential backoff
            }
        }

        client, err := f.ensureConnection(ctx)
        if err != nil {
            lastErr = err
            continue
        }

        err = operation(client)
        if err == nil {
            return nil
        }

        if isConnectionError(err) {
            f.markConnectionBroken()
            lastErr = err
            continue
        }

        return err // Non-connection error, fail immediately
    }

    return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

func isConnectionError(err error) bool {
    if err == nil {
        return false
    }
    errStr := err.Error()
    return errors.Is(err, io.EOF) ||
           strings.Contains(errStr, "connection reset") ||
           strings.Contains(errStr, "broken pipe") ||
           strings.Contains(errStr, "connection lost") ||
           strings.Contains(errStr, "closed network connection")
}
```

### Strategy 3: Connection Pool (Advanced)

**Approach**: Maintain multiple connections with automatic failover

```go
type connectionPool struct {
    connections []*sftp.Client
    dialFunc    func(context.Context) (*sftp.Client, error)
    maxSize     int
    mu          sync.RWMutex
}

func (p *connectionPool) getHealthyClient(ctx context.Context) (*sftp.Client, error) {
    p.mu.RLock()
    for _, client := range p.connections {
        if isHealthy(client) {
            p.mu.RUnlock()
            return client, nil
        }
    }
    p.mu.RUnlock()

    // Create new connection if needed
    return p.createConnection(ctx)
}
```

## Recommended Implementation

### Phase 1: Error Detection and Retry (Minimum Viable Solution)

1. **Add connection parameters to fileSystem struct**
   - Store host, username, password, hostKeyCallback
   - Enable reconnection without URL credentials

2. **Implement `isConnectionError()` helper**
   - Detect connection-related errors
   - Distinguish from other error types

3. **Add `reconnect()` method**
   - Close broken connection
   - Establish new connection with stored parameters
   - Thread-safe with mutex

4. **Wrap critical operations with retry logic**
   - Apply to all methods that call `getClient()`
   - Use exponential backoff (100ms, 200ms, 400ms)
   - Maximum 3 retry attempts
   - Return original error after exhausting retries

### Phase 2: Connection Health Monitoring (Enhanced Reliability)

1. **Add background health check**
   - Periodic lightweight operations (e.g., `Getwd()`)
   - Proactive reconnection before operations fail

2. **Implement keepalive mechanism**
   - Use SSH keepalive to maintain connection
   - Detect dead connections faster

### Phase 3: Connection Pooling (High Availability)

1. **Implement connection pool**
   - Multiple concurrent connections
   - Automatic failover
   - Load balancing

## Example Implementation Points

### Critical Methods to Modify

1. **`getClient()`** (line 214-241)
   - Add retry logic
   - Store connection parameters on first successful connection
   - Implement reconnection on failure

2. **`dial()`** (line 174-194)
   - Add connection validation
   - Store SSH client reference for keepalive

3. **All operation methods**
   - `MakeDir()`, `Stat()`, `ListDirInfo()`
   - `openFile()` and file operations
   - `Move()`, `Remove()`, `Truncate()`

### Configuration Options

Add configuration struct:

```go
type ReconnectConfig struct {
    MaxRetries     int           // Default: 3
    InitialBackoff time.Duration // Default: 100ms
    MaxBackoff     time.Duration // Default: 5s
    HealthCheckInterval time.Duration // Default: 30s (0 = disabled)
}
```

## Testing Considerations

### Scenarios to Test

1. **Network interruption during file operation**
   - Drop connection mid-transfer
   - Verify automatic reconnection and retry

2. **Server restart**
   - Stop/start SFTP server
   - Verify graceful reconnection

3. **Timeout scenarios**
   - Long-running operations
   - Idle connection timeouts

4. **Concurrent operations**
   - Multiple goroutines using same client
   - Thread-safety of reconnection logic

### Test Infrastructure

Use Docker-based SFTP server (already present in `sftp_test.go`):
- Simulate connection drops with `docker stop/start`
- Test network partitions with `iptables` rules
- Verify retry behavior with controlled failures

## Backward Compatibility

The reconnection implementation should:
- Be **opt-in** via configuration
- Default to current behavior if not configured
- Not break existing API contracts
- Preserve error semantics for non-connection errors

## Security Considerations

1. **Credential Storage**: Store connection credentials securely in memory
2. **Timeout Limits**: Prevent indefinite retry loops
3. **Rate Limiting**: Avoid overwhelming server with reconnection attempts
4. **Logging**: Log reconnection attempts without exposing credentials

## Performance Impact

- **Retry overhead**: Additional latency on connection loss (acceptable tradeoff)
- **Health checks**: Minimal overhead (1 operation per interval)
- **Memory**: Small increase for connection parameters storage
- **CPU**: Negligible for retry logic

## Conclusion

Connection lost errors can occur in **any SFTP operation** in the current implementation. The most practical solution is:

1. **Immediate**: Implement Strategy 2 (Retry with Exponential Backoff)
   - Minimal code changes
   - Significant reliability improvement
   - Handles transient network issues

2. **Future**: Add Strategy 1 (Health Monitoring)
   - Proactive detection
   - Better user experience
   - Reduced operation failures

3. **Optional**: Strategy 3 (Connection Pool) for high-concurrency scenarios

The retry strategy provides the best balance of complexity, reliability, and backward compatibility.
