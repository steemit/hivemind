# Fork Handling Strategy Evaluation

## Overview

This document evaluates the fork handling strategies for the Hivemind indexer.

## Strategy A: Follow Latest Block + Limited Rollback

### Implementation Details

Based on the legacy Python implementation:

1. **Block Queue**: Maintains a buffer of recent blocks (size = TRAIL_BLOCKS, default: 2)
2. **Fork Detection**: Validates block hash chain on each block
3. **Recovery**: Pops blocks back to last valid block (max depth: 25 blocks)
4. **Data Cleanup**: Removes affected records from all tables

### Pros

- Low latency: Follows chain closely (2-6 seconds behind head)
- Better user experience: Quick feedback for votes and discussions
- Handles microforks automatically

### Cons

- **Complexity**: Requires sophisticated fork detection and rollback logic
- **Data Consistency Risk**: Rollback may not perfectly restore all state
  - Follow counts may get out of sync (requires manual recount)
  - Some edge cases may leave inconsistent data
- **Performance Impact**: Rollback operations are expensive
- **Recovery Limitations**: 
  - Maximum fork depth: 25 blocks
  - Some data may require manual intervention
  - Fork beyond irreversible block requires waiting

### Implementation Requirements

1. Block queue with hash validation
2. Fork detection logic (MicroFork vs Fork)
3. Rollback mechanism that deletes:
   - Blocks
   - Posts (core, tags, cache)
   - Follows
   - Reblogs
   - Feed cache
   - Payments
   - Communities data
   - Notifications
   - Subscriptions
   - Roles
4. Transaction management for atomic rollback
5. Recovery verification on startup

### Risk Assessment

**Stability**: Medium
- Works well for microforks (within buffer)
- Complex for deeper forks
- Edge cases may cause issues

**Data Consistency**: Medium-High Risk
- Rollback logic must be comprehensive
- Some derived data (counts) may be inconsistent
- Requires periodic reconciliation

**Maintenance**: High Complexity
- Complex code to maintain
- Difficult to test all edge cases
- Requires ongoing monitoring

## Strategy B: Follow Only Irreversible Blocks

### Implementation Details

1. **Sync Target**: Only sync up to last irreversible block
2. **No Fork Handling**: No need for rollback logic
3. **Simple Logic**: Just check irreversible block and sync to it

### Pros

- **Simplicity**: Much simpler implementation
- **Stability**: No fork handling complexity
- **Data Consistency**: Guaranteed consistency (no rollback needed)
- **Reliability**: Fewer edge cases and failure modes
- **Easier Testing**: Simpler to test and verify

### Cons

- **Latency**: ~21 seconds delay (Steem irreversible block confirmation time)
- **User Experience**: Slower feedback for new content
- **Real-time Features**: Not suitable for real-time features

### Implementation Requirements

1. Query last irreversible block from steemd
2. Sync only up to that block
3. Simple loop: wait for new irreversible blocks

### Risk Assessment

**Stability**: High
- Very simple and reliable
- No complex edge cases

**Data Consistency**: High
- Guaranteed consistency
- No rollback needed

**Maintenance**: Low Complexity
- Simple code
- Easy to understand and maintain

## Recommendation

### For Initial Implementation: Strategy B

**Rationale:**
1. **Faster Development**: Can get a working system much faster
2. **Lower Risk**: Less chance of bugs and data corruption
3. **Easier Migration**: Simpler to verify correctness
4. **21s Delay Acceptable**: For most use cases, 21 seconds is acceptable
5. **Can Upgrade Later**: Can implement Strategy A later if needed

### Future Enhancement: Strategy A (Optional)

If low latency becomes critical:
1. Implement Strategy A as an optional mode
2. Make it configurable via `TRAIL_BLOCKS` setting
3. Add comprehensive monitoring and alerting
4. Implement periodic reconciliation jobs

## Implementation Plan

### Phase 1: Strategy B (Recommended)

1. Implement simple irreversible block following
2. No fork handling needed
3. Focus on core indexing functionality
4. Get system working and stable

### Phase 2: Strategy A (If Needed)

1. Add block queue
2. Implement fork detection
3. Implement rollback logic
4. Add comprehensive tests
5. Make it optional/configurable

## Configuration

```go
type ForkStrategy string

const (
	ForkStrategyIrreversible ForkStrategy = "irreversible" // Strategy B
	ForkStrategyLatest       ForkStrategy = "latest"        // Strategy A
)

type IndexerConfig struct {
	ForkStrategy ForkStrategy // "irreversible" or "latest"
	TrailBlocks  int          // Only used if strategy is "latest"
	MaxForkDepth int          // Only used if strategy is "latest" (default: 25)
}
```

## Conclusion

**Recommended Approach**: Start with Strategy B (irreversible blocks only) for initial implementation. This provides:
- Faster development
- Lower risk
- Guaranteed data consistency
- Simpler maintenance

Strategy A can be added later as an optional enhancement if low latency becomes critical.

