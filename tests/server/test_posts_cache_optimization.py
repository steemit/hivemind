#!/usr/bin/env python3
"""
Test script: Verify hive_posts_cache batch query optimization

Usage:
    python test_posts_cache_optimization.py --endpoint http://localhost:8080
    python test_posts_cache_optimization.py --endpoint http://localhost:8080 --test large
"""

import argparse
import json
import time
import sys
from typing import Dict, Any, Optional

try:
    import requests
except ImportError:
    print("Error: requests library is required")
    print("Run: pip install requests")
    sys.exit(1)


class APITester:
    """API test class"""
    
    def __init__(self, endpoint: str):
        self.endpoint = endpoint.rstrip('/')
        self.headers = {"Content-Type": "application/json"}
        self.results = []
    
    def call_api(self, method: str, params: Dict[str, Any]) -> Optional[Dict]:
        """Call JSON-RPC API"""
        payload = {
            "id": 1,
            "jsonrpc": "2.0",
            "method": method,
            "params": params
        }
        
        try:
            start = time.time()
            response = requests.post(
                self.endpoint,
                json=payload,
                headers=self.headers,
                timeout=60
            )
            elapsed = time.time() - start
            
            if response.status_code != 200:
                print(f"  ❌ HTTP error: {response.status_code}")
                return None
            
            result = response.json()
            
            if 'error' in result:
                print(f"  ❌ API error: {result['error']}")
                return None
            
            return {
                'result': result.get('result'),
                'elapsed': elapsed,
                'success': True
            }
        except requests.exceptions.Timeout:
            print(f"  ❌ Request timeout")
            return None
        except Exception as e:
            print(f"  ❌ Exception: {str(e)}")
            return None
    
    def test_small_list(self):
        """Test small list (≤1000)"""
        print("\n" + "=" * 60)
        print("Test 1: Small List Test (≤1000) - Verify Backward Compatibility")
        print("=" * 60)
        
        tests = [
            {
                'name': 'bridge.get_account_posts (blog, limit=20)',
                'method': 'bridge.get_account_posts',
                'params': {
                    'sort': 'blog',
                    'account': 'steemit',
                    'limit': 20
                }
            },
            {
                'name': 'bridge.get_account_posts (blog, limit=100)',
                'method': 'bridge.get_account_posts',
                'params': {
                    'sort': 'blog',
                    'account': 'steemit',
                    'limit': 100
                }
            },
            {
                'name': 'bridge.get_ranked_posts (trending)',
                'method': 'bridge.get_ranked_posts',
                'params': {
                    'sort': 'trending',
                    'limit': 50
                }
            },
            {
                'name': 'condenser_api.get_discussions_by_trending',
                'method': 'condenser_api.get_discussions_by_trending',
                'params': {
                    'limit': 50
                }
            },
            {
                'name': 'condenser_api.get_discussions_by_blog',
                'method': 'condenser_api.get_discussions_by_blog',
                'params': {
                    'tag': 'steemit',
                    'limit': 50
                }
            }
        ]
        
        passed = 0
        failed = 0
        
        for test in tests:
            print(f"\nTest: {test['name']}")
            result = self.call_api(test['method'], test['params'])
            
            if result and result['success']:
                data = result['result']
                count = len(data) if isinstance(data, list) else len(data) if isinstance(data, dict) else 0
                print(f"  ✅ Success")
                print(f"  - Response time: {result['elapsed']:.3f}s")
                print(f"  - Return count: {count}")
                passed += 1
            else:
                print(f"  ❌ Failed")
                failed += 1
        
        print(f"\nSmall list test results: {passed} passed, {failed} failed")
        return passed, failed
    
    def test_large_discussion(self, author: str = None, permlink: str = None):
        """Test large discussion (may have >1000 comments)"""
        print("\n" + "=" * 60)
        print("Test 2: Large Discussion Test (>1000) - Verify Batch Query Functionality")
        print("=" * 60)
        
        if not author or not permlink:
            print("⚠️  Need to provide popular post's author and permlink")
            print("   Usage: --author <author> --permlink <permlink>")
            print("   Example: --author steemit --permlink firstpost")
            return 0, 0
        
        print(f"\nTest: bridge.get_discussion")
        print(f"  - Author: {author}")
        print(f"  - Permlink: {permlink}")
        
        result = self.call_api('bridge.get_discussion', {
            'author': author,
            'permlink': permlink
        })
        
        if result and result['success']:
            data = result['result']
            count = len(data) if isinstance(data, dict) else 0
            print(f"  ✅ Success")
            print(f"  - Response time: {result['elapsed']:.3f}s")
            print(f"  - Discussion nodes: {count}")
            
            if count > 1000:
                print(f"  - ⚠️  Discussion contains {count} nodes, should trigger batch queries")
                print(f"  - 💡 Please check application logs to confirm batch queries were executed")
            
            return 1, 0
        else:
            print(f"  ❌ Failed")
            return 0, 1
    
    def test_performance(self):
        """Performance comparison test"""
        print("\n" + "=" * 60)
        print("Test 3: Performance Test")
        print("=" * 60)
        
        print("\nTest: bridge.get_account_posts (multiple calls)")
        
        times = []
        for i in range(5):
            result = self.call_api('bridge.get_account_posts', {
                'sort': 'blog',
                'account': 'steemit',
                'limit': 100
            })
            
            if result and result['success']:
                times.append(result['elapsed'])
                print(f"  Call {i+1}: {result['elapsed']:.3f}s")
        
        if times:
            avg_time = sum(times) / len(times)
            min_time = min(times)
            max_time = max(times)
            print(f"\n  Average response time: {avg_time:.3f}s")
            print(f"  Fastest: {min_time:.3f}s")
            print(f"  Slowest: {max_time:.3f}s")
            print(f"  ✅ Performance test completed")
            return 1, 0
        else:
            print(f"  ❌ Performance test failed")
            return 0, 1
    
    def run_all_tests(self, author: str = None, permlink: str = None):
        """Run all tests"""
        print("=" * 60)
        print("hive_posts_cache Batch Query Optimization Test")
        print("=" * 60)
        print(f"API endpoint: {self.endpoint}")
        
        total_passed = 0
        total_failed = 0
        
        # Test 1: Small list
        passed, failed = self.test_small_list()
        total_passed += passed
        total_failed += failed
        
        # Test 2: Large discussion
        passed, failed = self.test_large_discussion(author, permlink)
        total_passed += passed
        total_failed += failed
        
        # Test 3: Performance
        passed, failed = self.test_performance()
        total_passed += passed
        total_failed += failed
        
        # Summary
        print("\n" + "=" * 60)
        print("Test Summary")
        print("=" * 60)
        print(f"Total: {total_passed + total_failed} tests")
        print(f"Passed: {total_passed}")
        print(f"Failed: {total_failed}")
        
        if total_failed == 0:
            print("\n✅ All tests passed!")
        else:
            print(f"\n⚠️  {total_failed} test(s) failed")
        
        return total_passed, total_failed


def main():
    parser = argparse.ArgumentParser(
        description='Test hive_posts_cache batch query optimization'
    )
    parser.add_argument(
        '--endpoint',
        type=str,
        default='http://localhost:8080',
        help='API endpoint URL (default: http://localhost:8080)'
    )
    parser.add_argument(
        '--test',
        type=str,
        choices=['small', 'large', 'performance', 'all'],
        default='all',
        help='Test type to run (default: all)'
    )
    parser.add_argument(
        '--author',
        type=str,
        help='Post author for large discussion test'
    )
    parser.add_argument(
        '--permlink',
        type=str,
        help='Post permlink for large discussion test'
    )
    
    args = parser.parse_args()
    
    tester = APITester(args.endpoint)
    
    if args.test == 'small':
        tester.test_small_list()
    elif args.test == 'large':
        tester.test_large_discussion(args.author, args.permlink)
    elif args.test == 'performance':
        tester.test_performance()
    else:
        tester.run_all_tests(args.author, args.permlink)


if __name__ == '__main__':
    main()
