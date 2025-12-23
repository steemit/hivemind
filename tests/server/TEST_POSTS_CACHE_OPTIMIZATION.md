# hive_posts_cache Query Optimization Test Cases

This document provides API interface test cases for testing the `hive_posts_cache` batch query optimization.

## Optimization Overview

This optimization targets the `load_posts_keyed` and `load_posts` functions. When the `post_id` list exceeds 1000 items, queries are automatically split into batches to avoid performance issues caused by oversized IN clauses.

## Test Scenarios

### 1. Small List Tests (≤1000 posts) - Verify Backward Compatibility

These tests ensure that small lists are not affected and maintain the original logic.

#### 1.1 bridge.get_account_posts

**Interface**: `bridge.get_account_posts`

**Test Case 1.1.1**: Get account's blog list (small list)
```json
{
  "id": 1,
  "jsonrpc": "2.0",
  "method": "bridge.get_account_posts",
  "params": {
    "sort": "blog",
    "account": "steemit",
    "limit": 20
  }
}
```

**Test Case 1.1.2**: Get account's posts list
```json
{
  "id": 2,
  "jsonrpc": "2.0",
  "method": "bridge.get_account_posts",
  "params": {
    "sort": "posts",
    "account": "steemit",
    "limit": 100
  }
}
```

#### 1.2 bridge.get_ranked_posts

**Interface**: `bridge.get_ranked_posts`

**Test Case 1.2.1**: Get trending posts list
```json
{
  "id": 3,
  "jsonrpc": "2.0",
  "method": "bridge.get_ranked_posts",
  "params": {
    "sort": "trending",
    "limit": 50
  }
}
```

**Test Case 1.2.2**: Get hot posts list
```json
{
  "id": 4,
  "jsonrpc": "2.0",
  "method": "bridge.get_ranked_posts",
  "params": {
    "sort": "hot",
    "limit": 100
  }
}
```

#### 1.3 condenser_api.get_discussions_by_*

**Test Case 1.3.1**: Get trending discussions
```json
{
  "id": 5,
  "jsonrpc": "2.0",
  "method": "condenser_api.get_discussions_by_trending",
  "params": {
    "limit": 50
  }
}
```

**Test Case 1.3.2**: Get hot discussions
```json
{
  "id": 6,
  "jsonrpc": "2.0",
  "method": "condenser_api.get_discussions_by_hot",
  "params": {
    "limit": 50
  }
}
```

**Test Case 1.3.3**: Get blog discussions
```json
{
  "id": 7,
  "jsonrpc": "2.0",
  "method": "condenser_api.get_discussions_by_blog",
  "params": {
    "tag": "steemit",
    "limit": 50
  }
}
```

### 2. Large List Tests (>1000 posts) - Verify Batch Query Functionality

These tests verify that batch query functionality works correctly when the number of posts exceeds 1000.

#### 2.1 bridge.get_discussion (Most Likely Scenario to Trigger Large Lists)

**Interface**: `bridge.get_discussion`

**Description**: This interface loads all comments in a discussion thread. If the discussion is very popular, it may contain thousands of comments.

**Test Case 2.1.1**: Get popular discussion (may contain many comments)
```json
{
  "id": 8,
  "jsonrpc": "2.0",
  "method": "bridge.get_discussion",
  "params": {
    "author": "popular_author",
    "permlink": "popular_post_permlink"
  }
}
```

**Test Steps**:
1. Find a post with many comments (comment count > 1000)
2. Call the `bridge.get_discussion` interface
3. Verify the returned results are correct
4. Check logs to confirm if batch queries were executed

**Expected Results**:
- Returns complete discussion tree structure
- All comments are loaded correctly
- If comment count > 1000, multiple batch queries should be observed

#### 2.2 Batch Fetch Multiple Posts

**Test Case 2.2.1**: Accumulate large number of post IDs through multiple calls

**Description**: Although a single API call usually doesn't return >1000 posts, you can test by combining multiple queries.

**Test Steps**:
1. Call multiple `bridge.get_ranked_posts` requests to accumulate >1000 post IDs
2. Manually construct a test request containing >1000 IDs (requires direct internal function calls or creating a test script)

### 3. Boundary Tests (Exactly 1000 posts)

**Test Case 3.1**: Boundary condition test

**Description**: Test behavior when the number of posts is exactly 1000.

**Test Steps**:
1. Construct a test scenario containing exactly 1000 `post_id`s
2. Verify the query works correctly
3. Verify that batch query is not triggered (should query directly)

### 4. Performance Comparison Tests

#### 4.1 Small List Performance Test

**Test Case 4.1.1**: Test if performance of small lists (≤1000) remains unchanged

```bash
# Test response time using curl
time curl -X POST http://your-api-endpoint \
  -H "Content-Type: application/json" \
  -d '{
    "id": 1,
    "jsonrpc": "2.0",
    "method": "bridge.get_account_posts",
    "params": {
      "sort": "blog",
      "account": "steemit",
      "limit": 100
    }
  }'
```

**Expected Result**: Response time should be the same as before optimization or slightly improved.

#### 4.2 Large List Performance Test

**Test Case 4.2.1**: Test performance improvement for large lists (>1000)

**Test Steps**:
1. Find a discussion containing >1000 comments
2. Test performance before optimization (if old version is available)
3. Test performance after optimization
4. Compare response times and database query counts

**Expected Results**: 
- After optimization, query timeouts should be avoided
- Query time should be more stable
- Database load should be more evenly distributed

### 5. Functional Correctness Tests

#### 5.1 Result Completeness Test

**Test Case 5.1.1**: Verify batch query returns complete results

**Test Steps**:
1. Call an interface that may trigger batch queries
2. Verify the number of returned posts matches expectations
3. Verify all post data is complete (all fields exist)
4. Verify post order is correct

#### 5.2 Data Consistency Test

**Test Case 5.2.1**: Verify data consistency of batch queries

**Test Steps**:
1. Call the same interface multiple times
2. Verify returned results are consistent
3. Verify no data loss or duplication

## Python Test Script Example

```python
import asyncio
import time
import requests

API_ENDPOINT = "http://your-api-endpoint"
HEADERS = {"Content-Type": "application/json"}

def test_small_list():
    """Test small list (≤1000)"""
    payload = {
        "id": 1,
        "jsonrpc": "2.0",
        "method": "bridge.get_account_posts",
        "params": {
            "sort": "blog",
            "account": "steemit",
            "limit": 100
        }
    }
    
    start = time.time()
    response = requests.post(API_ENDPOINT, json=payload, headers=HEADERS)
    elapsed = time.time() - start
    
    result = response.json()
    post_count = len(result.get('result', []))
    
    print(f"Small list test:")
    print(f"  - Response time: {elapsed:.3f}s")
    print(f"  - Posts returned: {post_count}")
    print(f"  - Status: {'✅ Pass' if response.status_code == 200 else '❌ Fail'}")
    
    return response.status_code == 200

def test_large_discussion():
    """Test large discussion (may have >1000 comments)"""
    # Replace with actual popular post
    payload = {
        "id": 2,
        "jsonrpc": "2.0",
        "method": "bridge.get_discussion",
        "params": {
            "author": "popular_author",
            "permlink": "popular_post_permlink"
        }
    }
    
    start = time.time()
    response = requests.post(API_ENDPOINT, json=payload, headers=HEADERS)
    elapsed = time.time() - start
    
    result = response.json()
    discussion_count = len(result.get('result', {}))
    
    print(f"\nLarge discussion test:")
    print(f"  - Response time: {elapsed:.3f}s")
    print(f"  - Discussion nodes: {discussion_count}")
    print(f"  - Status: {'✅ Pass' if response.status_code == 200 else '❌ Fail'}")
    
    if discussion_count > 1000:
        print(f"  - ⚠️  Discussion contains {discussion_count} nodes, should trigger batch queries")
    
    return response.status_code == 200

def test_boundary():
    """Test boundary condition (exactly 1000)"""
    # This test requires constructing a scenario with exactly 1000 IDs
    # May need to call internal functions directly or use test database
    print("\nBoundary test:")
    print("  - Need to construct scenario with exactly 1000 post_ids")
    print("  - Recommend using unit tests or integration tests")

if __name__ == "__main__":
    print("=" * 50)
    print("hive_posts_cache Batch Query Optimization Test")
    print("=" * 50)
    
    test_small_list()
    test_large_discussion()
    test_boundary()
    
    print("\n" + "=" * 50)
    print("Test Complete")
    print("=" * 50)
```

## Database Query Verification

### Check if Batch Queries Are Executed

In PostgreSQL, you can check if batch queries were executed using the following method:

```sql
-- View slow query log
SELECT query, calls, total_time, mean_time
FROM pg_stat_statements
WHERE query LIKE '%hive_posts_cache%'
  AND query LIKE '%post_id IN%'
ORDER BY calls DESC
LIMIT 10;
```

### Monitor Query Performance

```sql
-- View query execution plan
EXPLAIN ANALYZE
SELECT post_id, community_id, author, permlink, title, body, category, depth, 
       promoted, payout, payout_at, is_paidout, children, votes,
       created_at, updated_at, rshares, raw_json, json,
       is_hidden, is_grayed, total_votes, flag_weight
FROM hive_posts_cache 
WHERE post_id IN (1, 2, 3, ..., 1000);  -- Replace with actual ID list
```

## Test Checklist

- [ ] Small list (≤1000) tests pass
- [ ] Large list (>1000) tests pass
- [ ] Boundary condition (=1000) tests pass
- [ ] Performance comparison tests completed
- [ ] Functional correctness verification passed
- [ ] Data consistency verification passed
- [ ] Log check confirms batch queries executed
- [ ] Database query plan verification

## Notes

1. **Test Environment**: Ensure testing is done in a test environment to avoid affecting production
2. **Test Data**: Use realistic test data to ensure test scenarios are close to production
3. **Performance Baseline**: Record performance data before optimization for comparison
4. **Log Monitoring**: Check application logs to confirm batch query logic executes correctly
5. **Database Monitoring**: Monitor database query performance to confirm optimization effects

## Related Interface List

### Bridge API
- `bridge.get_discussion` - ⭐ Most likely to trigger large lists
- `bridge.get_account_posts`
- `bridge.get_ranked_posts`
- `bridge.get_post`

### Condenser API
- `condenser_api.get_discussions_by_trending`
- `condenser_api.get_discussions_by_hot`
- `condenser_api.get_discussions_by_promoted`
- `condenser_api.get_discussions_by_created`
- `condenser_api.get_discussions_by_blog`
- `condenser_api.get_discussions_by_feed`
- `condenser_api.get_content`
- `condenser_api.get_content_replies`

### Get State API
- `condenser_api.get_state` - May trigger large lists
