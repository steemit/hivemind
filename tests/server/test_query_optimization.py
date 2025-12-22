#!/usr/bin/env python3
"""
Performance test script for query optimization.

This script compares the performance of the original NOT EXISTS query
vs the optimized LEFT JOIN query in the _child_ids function.

Usage:
    python -m pytest tests/server/test_query_optimization.py -v
    # Or run directly:
    python tests/server/test_query_optimization.py
"""

import asyncio
import time
import statistics
import os
import sys
from typing import List, Tuple, Dict, Any

# Add parent directory to path for imports
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '../..'))

try:
    from hive.server.db import Db
except ImportError:
    print("Warning: Could not import hive.server.db. Make sure you're in the right environment.")
    sys.exit(1)


class QueryPerformanceTester:
    """Test and compare query performance."""

    def __init__(self, db: Db):
        self.db = db
        self.results = {
            'not_exists': [],
            'left_join': [],
            'explain_plans': {}
        }

    async def _child_ids_not_exists(self, parent_ids: Tuple[int]) -> List[Tuple[int, List[int]]]:
        """Original implementation using NOT EXISTS."""
        sql = """
            SELECT p.parent_id as parent_id, array_agg(p.id) as child_ids
            FROM hive_posts p
            WHERE p.parent_id IN :ids
            AND p.is_deleted = '0'
            AND NOT EXISTS (
                SELECT 1
                FROM hive_posts_status s
                WHERE s.list_type = '3'
                AND s.author = p.author
            )
            GROUP BY p.parent_id
        """
        rows = await self.db.query_all(sql, ids=parent_ids)
        return [[row['parent_id'], row['child_ids']] for row in rows]

    async def _child_ids_left_join(self, parent_ids: Tuple[int]) -> List[Tuple[int, List[int]]]:
        """Optimized implementation using LEFT JOIN."""
        sql = """
            SELECT p.parent_id as parent_id, array_agg(p.id) as child_ids
            FROM hive_posts p
            LEFT JOIN hive_posts_status s ON s.list_type = '3' AND s.author = p.author
            WHERE p.parent_id IN :ids
            AND p.is_deleted = '0'
            AND s.id IS NULL
            GROUP BY p.parent_id
        """
        rows = await self.db.query_all(sql, ids=parent_ids)
        return [[row['parent_id'], row['child_ids']] for row in rows]

    async def get_explain_plan(self, sql: str, parent_ids: Tuple[int]) -> str:
        """Get EXPLAIN ANALYZE plan for a query."""
        try:
            # For EXPLAIN ANALYZE, we need to use parameterized query
            # PostgreSQL requires actual values in the query
            explain_sql = f"EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) {sql}"
            # Execute with actual parameters
            rows = await self.db.query_all(explain_sql, ids=parent_ids)
            # Extract plan text from result
            plan_text = []
            for row in rows:
                if isinstance(row, dict):
                    # If row is a dict, try to get the plan text
                    plan_text.append(str(row.get('QUERY PLAN', row)))
                else:
                    plan_text.append(str(row))
            return '\n'.join(plan_text) if plan_text else "No plan available"
        except Exception as e:
            return f"Error getting explain plan: {e}"

    async def measure_query_time(self, query_func, parent_ids: Tuple[int], 
                                 iterations: int = 10) -> List[float]:
        """Measure query execution time over multiple iterations."""
        times = []
        for i in range(iterations):
            start = time.perf_counter()
            await query_func(parent_ids)
            end = time.perf_counter()
            times.append((end - start) * 1000)  # Convert to milliseconds
        return times

    async def get_test_parent_ids(self, limit: int = 50) -> Tuple[int]:
        """Get a sample of parent_ids from the database for testing."""
        sql = """
            SELECT DISTINCT parent_id 
            FROM hive_posts 
            WHERE parent_id IS NOT NULL 
            AND is_deleted = '0'
            LIMIT :limit
        """
        rows = await self.db.query_col(sql, limit=limit)
        return tuple(rows) if rows else tuple()

    async def compare_queries(self, parent_ids: Tuple[int], iterations: int = 10):
        """Compare both query implementations."""
        if not parent_ids:
            print("No parent_ids found for testing. Skipping comparison.")
            return

        print(f"\n{'='*80}")
        print(f"Query Performance Comparison")
        print(f"{'='*80}")
        print(f"Testing with {len(parent_ids)} parent_ids")
        print(f"Running {iterations} iterations per query\n")

        # Test NOT EXISTS query
        print("Testing NOT EXISTS query...")
        not_exists_times = await self.measure_query_time(
            self._child_ids_not_exists, parent_ids, iterations)
        self.results['not_exists'] = not_exists_times

        # Test LEFT JOIN query
        print("Testing LEFT JOIN query...")
        left_join_times = await self.measure_query_time(
            self._child_ids_left_join, parent_ids, iterations)
        self.results['left_join'] = left_join_times

        # Verify results are identical
        print("\nVerifying result consistency...")
        not_exists_result = await self._child_ids_not_exists(parent_ids)
        left_join_result = await self._child_ids_left_join(parent_ids)
        
        # Sort results for comparison
        not_exists_sorted = sorted(not_exists_result, key=lambda x: x[0])
        left_join_sorted = sorted(left_join_result, key=lambda x: x[0])
        
        results_match = not_exists_sorted == left_join_sorted
        print(f"Results match: {results_match}")
        if not results_match:
            print(f"NOT EXISTS returned {len(not_exists_result)} results")
            print(f"LEFT JOIN returned {len(left_join_result)} results")
            print("\nSample NOT EXISTS result:", not_exists_sorted[:3])
            print("Sample LEFT JOIN result:", left_join_sorted[:3])

        # Get explain plans
        print("\nFetching EXPLAIN ANALYZE plans...")
        not_exists_sql = """
            SELECT p.parent_id as parent_id, array_agg(p.id) as child_ids
            FROM hive_posts p
            WHERE p.parent_id IN :ids
            AND p.is_deleted = '0'
            AND NOT EXISTS (
                SELECT 1
                FROM hive_posts_status s
                WHERE s.list_type = '3'
                AND s.author = p.author
            )
            GROUP BY p.parent_id
        """
        left_join_sql = """
            SELECT p.parent_id as parent_id, array_agg(p.id) as child_ids
            FROM hive_posts p
            LEFT JOIN hive_posts_status s ON s.list_type = '3' AND s.author = p.author
            WHERE p.parent_id IN :ids
            AND p.is_deleted = '0'
            AND s.id IS NULL
            GROUP BY p.parent_id
        """
        
        # Note: EXPLAIN ANALYZE requires actual values, so we'll use a small sample
        sample_ids = parent_ids[:10] if len(parent_ids) > 10 else parent_ids
        self.results['explain_plans']['not_exists'] = await self.get_explain_plan(
            not_exists_sql, sample_ids)
        self.results['explain_plans']['left_join'] = await self.get_explain_plan(
            left_join_sql, sample_ids)

    def print_results(self):
        """Print performance comparison results."""
        print(f"\n{'='*80}")
        print(f"Performance Results")
        print(f"{'='*80}\n")

        not_exists_times = self.results['not_exists']
        left_join_times = self.results['left_join']

        if not not_exists_times or not left_join_times:
            print("No results to display.")
            return

        # Calculate statistics
        not_exists_stats = {
            'mean': statistics.mean(not_exists_times),
            'median': statistics.median(not_exists_times),
            'min': min(not_exists_times),
            'max': max(not_exists_times),
            'stdev': statistics.stdev(not_exists_times) if len(not_exists_times) > 1 else 0
        }

        left_join_stats = {
            'mean': statistics.mean(left_join_times),
            'median': statistics.median(left_join_times),
            'min': min(left_join_times),
            'max': max(left_join_times),
            'stdev': statistics.stdev(left_join_times) if len(left_join_times) > 1 else 0
        }

        # Print NOT EXISTS stats
        print("NOT EXISTS Query:")
        print(f"  Mean:   {not_exists_stats['mean']:.2f} ms")
        print(f"  Median: {not_exists_stats['median']:.2f} ms")
        print(f"  Min:    {not_exists_stats['min']:.2f} ms")
        print(f"  Max:    {not_exists_stats['max']:.2f} ms")
        print(f"  StdDev: {not_exists_stats['stdev']:.2f} ms")

        # Print LEFT JOIN stats
        print("\nLEFT JOIN Query:")
        print(f"  Mean:   {left_join_stats['mean']:.2f} ms")
        print(f"  Median: {left_join_stats['median']:.2f} ms")
        print(f"  Min:    {left_join_stats['min']:.2f} ms")
        print(f"  Max:    {left_join_stats['max']:.2f} ms")
        print(f"  StdDev: {left_join_stats['stdev']:.2f} ms")

        # Calculate improvement
        improvement = ((not_exists_stats['mean'] - left_join_stats['mean']) / 
                      not_exists_stats['mean']) * 100
        speedup = not_exists_stats['mean'] / left_join_stats['mean'] if left_join_stats['mean'] > 0 else 0

        print(f"\n{'='*80}")
        print(f"Improvement:")
        print(f"  Performance gain: {improvement:.1f}%")
        print(f"  Speedup factor:   {speedup:.2f}x")
        print(f"{'='*80}\n")

        # Print explain plans
        if self.results['explain_plans']:
            print("EXPLAIN ANALYZE Plans:")
            print(f"\n{'='*80}")
            print("NOT EXISTS Plan:")
            print(f"{'='*80}")
            print(self.results['explain_plans']['not_exists'])
            print(f"\n{'='*80}")
            print("LEFT JOIN Plan:")
            print(f"{'='*80}")
            print(self.results['explain_plans']['left_join'])
            print(f"{'='*80}\n")


async def run_test(database_url: str = None, iterations: int = 10, 
                   parent_id_limit: int = 50):
    """Run the performance test."""
    # Get database URL from environment or parameter
    if not database_url:
        database_url = os.environ.get('DATABASE_URL')
        if not database_url:
            print("Error: DATABASE_URL environment variable not set.")
            print("Usage: DATABASE_URL=postgresql://user:pass@host:5432/db python test_query_optimization.py")
            return

    print(f"Connecting to database: {database_url.split('@')[-1] if '@' in database_url else database_url}")
    
    # Initialize database connection
    db = await Db.create(database_url)
    
    try:
        tester = QueryPerformanceTester(db)
        
        # Get test data
        print("Fetching test data...")
        parent_ids = await tester.get_test_parent_ids(parent_id_limit)
        
        if not parent_ids:
            print("No test data available. Make sure the database has posts.")
            return
        
        # Run comparison
        await tester.compare_queries(parent_ids, iterations)
        
        # Print results
        tester.print_results()
        
    finally:
        db.close()
        await db.wait_closed()


def main():
    """Main entry point."""
    import argparse
    
    parser = argparse.ArgumentParser(
        description='Test query optimization performance')
    parser.add_argument(
        '--database-url',
        type=str,
        help='Database URL (or set DATABASE_URL env var)')
    parser.add_argument(
        '--iterations',
        type=int,
        default=10,
        help='Number of iterations per query (default: 10)')
    parser.add_argument(
        '--parent-id-limit',
        type=int,
        default=50,
        help='Maximum number of parent IDs to test (default: 50)')
    
    args = parser.parse_args()
    
    asyncio.run(run_test(
        database_url=args.database_url,
        iterations=args.iterations,
        parent_id_limit=args.parent_id_limit
    ))


if __name__ == '__main__':
    main()

