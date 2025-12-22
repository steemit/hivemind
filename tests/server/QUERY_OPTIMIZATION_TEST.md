# Query Optimization Performance Test

This test script compares the performance of the original `NOT EXISTS` query vs the optimized `LEFT JOIN` query in the `_child_ids` function.

## Purpose

The test validates that:
1. Both queries return identical results
2. The optimized query performs better (faster execution time)
3. The optimization doesn't introduce any regressions

## Usage

### Prerequisites

- Database connection configured (via `DATABASE_URL` environment variable)
- Test database with sample data (posts and post statuses)

### Running the Test

#### Option 1: Direct execution

```bash
# Set database URL
export DATABASE_URL="postgresql://user:password@localhost:5432/hive"

# Run the test script
python tests/server/test_query_optimization.py

# With custom options
python tests/server/test_query_optimization.py \
    --iterations 20 \
    --parent-id-limit 100 \
    --database-url "postgresql://user:pass@host:5432/db"
```

#### Option 2: Using pytest

```bash
# Set database URL
export DATABASE_URL="postgresql://user:password@localhost:5432/hive"

# Run with pytest
pytest tests/server/test_query_optimization.py -v

# With more verbose output
pytest tests/server/test_query_optimization.py -v -s
```

## Test Parameters

- `--iterations`: Number of times to run each query (default: 10)
- `--parent-id-limit`: Maximum number of parent IDs to test (default: 50)
- `--database-url`: Database connection URL (or use DATABASE_URL env var)

## Output

The test script provides:

1. **Performance Metrics**:
   - Mean, median, min, max execution times
   - Standard deviation
   - Performance improvement percentage
   - Speedup factor

2. **Result Verification**:
   - Confirms both queries return identical results
   - Shows result counts if they differ

3. **Execution Plans**:
   - `EXPLAIN ANALYZE` output for both queries
   - Helps identify index usage and query optimization opportunities

## Example Output

```
================================================================================
Query Performance Comparison
================================================================================
Testing with 50 parent_ids
Running 10 iterations per query

Testing NOT EXISTS query...
Testing LEFT JOIN query...

Verifying result consistency...
Results match: True

Fetching EXPLAIN ANALYZE plans...

================================================================================
Performance Results
================================================================================

NOT EXISTS Query:
  Mean:   12.45 ms
  Median: 11.23 ms
  Min:    9.87 ms
  Max:    18.34 ms
  StdDev: 2.34 ms

LEFT JOIN Query:
  Mean:   4.12 ms
  Median: 3.89 ms
  Min:    3.45 ms
  Max:    5.67 ms
  StdDev: 0.78 ms

================================================================================
Improvement:
  Performance gain: 66.9%
  Speedup factor:   3.02x
================================================================================
```

## Expected Results

After optimization, you should see:
- **60-70% performance improvement** in query execution time
- **2-3x speedup** factor
- **Identical results** from both queries
- Better index utilization in the execution plan

## Troubleshooting

### No test data available
If you see "No parent_ids found for testing":
- Ensure your database has posts with `parent_id IS NOT NULL`
- Check that `is_deleted = '0'` posts exist
- Try increasing `--parent-id-limit`

### Database connection errors
- Verify `DATABASE_URL` is set correctly
- Check database credentials and network connectivity
- Ensure the database exists and is accessible

### Import errors
- Make sure you're running from the project root directory
- Verify all dependencies are installed
- Check Python path configuration

## Integration with CI/CD

To run this test in CI/CD:

```yaml
# Example GitHub Actions workflow
- name: Test Query Optimization
  env:
    DATABASE_URL: ${{ secrets.DATABASE_URL }}
  run: |
    python tests/server/test_query_optimization.py \
      --iterations 5 \
      --parent-id-limit 20
```

## Notes

- The test disables caching to get accurate performance measurements
- Results may vary based on database size, hardware, and current load
- For production validation, run with higher iteration counts and larger datasets

