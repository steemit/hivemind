#!/usr/bin/env python3
"""
API 比较器 - 用于对比两个版本的 Hivemind API 响应

用法:
    python api_comparator.py --config test_cases.yaml
"""

import json
import sys
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Optional
from urllib.parse import urljoin

import requests
import yaml


@dataclass
class TestResult:
    """测试结果"""
    api_method: str
    test_name: str
    passed: bool
    python_response: Optional[Any] = None
    go_response: Optional[Any] = None
    difference: Optional[str] = None
    error: Optional[str] = None
    duration_ms: int = 0

    def to_dict(self) -> dict:
        return {
            "api_method": self.api_method,
            "test_name": self.test_name,
            "passed": self.passed,
            "python_response": self.python_response,
            "go_response": self.go_response,
            "difference": self.difference,
            "error": self.error,
            "duration_ms": self.duration_ms
        }


@dataclass
class TestConfig:
    """测试配置"""
    python_api_url: str = "http://localhost:8081"
    go_api_url: str = "http://localhost:8082"
    timeout: int = 30
    ignore_fields: list = field(default_factory=list)
    tolerance: dict = field(default_factory=lambda: {
        "float_epsilon": 0.001,
        "timestamp_seconds": 1
    })


class ResponseComparator:
    """响应比较器"""

    def __init__(self, config: TestConfig):
        self.config = config
        self.ignore_fields = set(config.ignore_fields)
        self.tolerance = config.tolerance

    def compare(self, python_result: Any, go_result: Any, path: str = "") -> tuple[bool, Optional[str]]:
        """
        比较两个响应

        返回: (是否匹配, 差异描述)
        """
        # 两个都是 None
        if python_result is None and go_result is None:
            return True, None

        # 一个是 None
        if python_result is None or go_result is None:
            return False, f"类型不匹配: {path} - Python: {type(python_result)}, Go: {type(go_result)}"

        # 类型检查
        if type(python_result) != type(go_result):
            # 允许 int 和 float 互换
            if not (isinstance(python_result, (int, float)) and isinstance(go_result, (int, float))):
                return False, f"类型不匹配: {path} - Python: {type(python_result)}, Go: {type(go_result)}"

        # 基本类型比较
        if isinstance(python_result, (int, float)):
            return self._compare_numbers(python_result, go_result, path)
        elif isinstance(python_result, str):
            return python_result == go_result, None
        elif isinstance(python_result, bool):
            return python_result == go_result, None
        elif isinstance(python_result, list):
            return self._compare_lists(python_result, go_result, path)
        elif isinstance(python_result, dict):
            return self._compare_dicts(python_result, go_result, path)
        else:
            return False, f"不支持的类型: {type(python_result)}"

    def _compare_numbers(self, a: float, b: float, path: str) -> tuple[bool, Optional[str]]:
        """比较数字"""
        epsilon = self.tolerance.get("float_epsilon", 0.001)
        if abs(a - b) <= epsilon:
            return True, None
        return False, f"数值不匹配: {path} - Python: {a}, Go: {b}, 差值: {abs(a - b)}"

    def _compare_lists(self, a: list, b: list, path: str) -> tuple[bool, Optional[str]]:
        """比较列表"""
        if len(a) != len(b):
            return False, f"列表长度不匹配: {path} - Python: {len(a)}, Go: {len(b)}"

        for i, (item_a, item_b) in enumerate(zip(a, b)):
            matched, diff = self.compare(item_a, item_b, f"{path}[{i}]")
            if not matched:
                return False, diff

        return True, None

    def _compare_dicts(self, a: dict, b: dict, path: str) -> tuple[bool, Optional[str]]:
        """比较字典"""
        keys_a = set(a.keys()) - self.ignore_fields
        keys_b = set(b.keys()) - self.ignore_fields

        if keys_a != keys_b:
            only_a = keys_a - keys_b
            only_b = keys_b - keys_a
            return False, f"键不匹配: {path} - 仅Python: {only_a}, 仅Go: {only_b}"

        for key in keys_a:
            matched, diff = self.compare(a[key], b[key], f"{path}.{key}" if path else key)
            if not matched:
                return False, diff

        return True, None


class APIClient:
    """API 客户端"""

    def __init__(self, base_url: str, timeout: int = 30):
        self.base_url = base_url
        self.timeout = timeout
        self.session = requests.Session()
        self.session.headers.update({"Content-Type": "application/json"})

    def call(self, method: str, params: Any = None) -> dict:
        """
        调用 JSON-RPC API

        Args:
            method: API 方法名
            params: 参数

        Returns:
            响应结果
        """
        payload = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": method,
            "params": params if params is not None else []
        }

        try:
            response = self.session.post(
                self.base_url,
                json=payload,
                timeout=self.timeout
            )
            response.raise_for_status()

            data = response.json()

            if "error" in data:
                raise Exception(f"API 错误: {data['error']}")

            return data.get("result", data)

        except requests.RequestException as e:
            raise Exception(f"请求失败: {e}")


class APITestRunner:
    """API 测试运行器"""

    def __init__(self, config: TestConfig):
        self.config = config
        self.python_client = APIClient(config.python_api_url, config.timeout)
        self.go_client = APIClient(config.go_api_url, config.timeout)
        self.comparator = ResponseComparator(config)
        self.results: list[TestResult] = []

    def run_test(self, api_method: str, test_name: str, params: Any,
                 ignore_fields: list = None, tolerance: dict = None) -> TestResult:
        """
        运行单个测试用例

        Args:
            api_method: API 方法名
            test_name: 测试名称
            params: 请求参数
            ignore_fields: 要忽略的字段
            tolerance: 容差配置

        Returns:
            测试结果
        """
        # 更新比较器配置
        if ignore_fields:
            self.comparator.ignore_fields = set(ignore_fields)
        else:
            self.comparator.ignore_fields = set(self.config.ignore_fields)

        if tolerance:
            self.comparator.tolerance = {**self.config.tolerance, **tolerance}

        start_time = datetime.now()

        try:
            # 调用 Python API
            python_response = self.python_client.call(api_method, params)

            # 调用 Go API
            go_response = self.go_client.call(api_method, params)

            # 比较响应
            matched, difference = self.comparator.compare(python_response, go_response)

            duration_ms = int((datetime.now() - start_time).total_seconds() * 1000)

            result = TestResult(
                api_method=api_method,
                test_name=test_name,
                passed=matched,
                python_response=python_response,
                go_response=go_response,
                difference=difference,
                duration_ms=duration_ms
            )

        except Exception as e:
            duration_ms = int((datetime.now() - start_time).total_seconds() * 1000)
            result = TestResult(
                api_method=api_method,
                test_name=test_name,
                passed=False,
                error=str(e),
                duration_ms=duration_ms
            )

        self.results.append(result)
        return result

    def run_from_config(self, config_file: str) -> list[TestResult]:
        """
        从配置文件运行测试

        Args:
            config_file: 配置文件路径

        Returns:
            所有测试结果
        """
        with open(config_file, "r") as f:
            config = yaml.safe_load(f)

        test_cases = config.get("test_cases", [])

        for test_case in test_cases:
            self.run_test(
                api_method=test_case["api"],
                test_name=test_case["name"],
                params=test_case.get("params"),
                ignore_fields=test_case.get("ignore_fields"),
                tolerance=test_case.get("tolerance")
            )

        return self.results

    def get_summary(self) -> dict:
        """获取测试摘要"""
        total = len(self.results)
        passed = sum(1 for r in self.results if r.passed)
        failed = total - passed

        return {
            "total": total,
            "passed": passed,
            "failed": failed,
            "pass_rate": f"{(passed / total * 100):.1f}%" if total > 0 else "0%",
            "total_duration_ms": sum(r.duration_ms for r in self.results)
        }


class ReportGenerator:
    """报告生成器"""

    def __init__(self, results: list[TestResult], summary: dict):
        self.results = results
        self.summary = summary

    def generate_console_report(self) -> str:
        """生成控制台报告"""
        lines = [
            "=" * 60,
            "API 一致性测试报告",
            "=" * 60,
            "",
            "测试摘要:",
            f"  总计: {self.summary['total']}",
            f"  通过: {self.summary['passed']}",
            f"  失败: {self.summary['failed']}",
            f"  通过率: {self.summary['pass_rate']}",
            f"  总耗时: {self.summary['total_duration_ms']} ms",
            ""
        ]

        if self.summary["failed"] > 0:
            lines.extend([
                "失败用例:",
                "-" * 60
            ])

            for result in self.results:
                if not result.passed:
                    lines.append(f"  - {result.test_name} ({result.api_method})")
                    if result.error:
                        lines.append(f"    错误: {result.error}")
                    if result.difference:
                        lines.append(f"    差异: {result.difference}")

        return "\n".join(lines)

    def generate_json_report(self, output_file: str):
        """生成 JSON 报告"""
        report = {
            "timestamp": datetime.now().isoformat(),
            "summary": self.summary,
            "results": [r.to_dict() for r in self.results]
        }

        with open(output_file, "w") as f:
            json.dump(report, f, indent=2, ensure_ascii=False)

    def generate_html_report(self, output_file: str):
        """生成 HTML 报告"""
        html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>API 一致性测试报告</title>
    <style>
        body {{ font-family: Arial, sans-serif; margin: 20px; }}
        .summary {{ background: #f0f0f0; padding: 15px; border-radius: 5px; margin-bottom: 20px; }}
        .passed {{ color: green; }}
        .failed {{ color: red; }}
        table {{ border-collapse: collapse; width: 100%; }}
        th, td {{ border: 1px solid #ddd; padding: 8px; text-align: left; }}
        th {{ background-color: #4CAF50; color: white; }}
        tr:nth-child(even) {{ background-color: #f2f2f2; }}
        .result-passed {{ background-color: #d4edda; }}
        .result-failed {{ background-color: #f8d7da; }}
        .diff {{ white-space: pre-wrap; font-family: monospace; font-size: 12px; }}
    </style>
</head>
<body>
    <h1>API 一致性测试报告</h1>
    <p>生成时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</p>

    <div class="summary">
        <h2>测试摘要</h2>
        <p>总计: {self.summary['total']}</p>
        <p>通过: <span class="passed">{self.summary['passed']}</span></p>
        <p>失败: <span class="failed">{self.summary['failed']}</span></p>
        <p>通过率: {self.summary['pass_rate']}</p>
        <p>总耗时: {self.summary['total_duration_ms']} ms</p>
    </div>

    <h2>测试结果</h2>
    <table>
        <tr>
            <th>API 方法</th>
            <th>测试名称</th>
            <th>状态</th>
            <th>耗时</th>
            <th>差异/错误</th>
        </tr>
"""

        for result in self.results:
            status_class = "result-passed" if result.passed else "result-failed"
            status_text = "通过" if result.passed else "失败"
            diff_text = result.difference or result.error or "-"

            html += f"""
        <tr class="{status_class}">
            <td>{result.api_method}</td>
            <td>{result.test_name}</td>
            <td>{status_text}</td>
            <td>{result.duration_ms} ms</td>
            <td class="diff">{diff_text}</td>
        </tr>
"""

        html += """
    </table>
</body>
</html>
"""

        with open(output_file, "w") as f:
            f.write(html)


def main():
    """主函数"""
    import argparse

    parser = argparse.ArgumentParser(description="Hivemind API 一致性测试工具")
    parser.add_argument("--config", default="test_cases.yaml", help="测试配置文件")
    parser.add_argument("--python-url", default="http://localhost:8081", help="Python API URL")
    parser.add_argument("--go-url", default="http://localhost:8082", help="Go API URL")
    parser.add_argument("--timeout", type=int, default=30, help="请求超时时间(秒)")
    parser.add_argument("--output", default="results", help="结果输出目录")
    parser.add_argument("--no-html", action="store_true", help="不生成 HTML 报告")

    args = parser.parse_args()

    # 创建配置
    config = TestConfig(
        python_api_url=args.python_url,
        go_api_url=args.go_url,
        timeout=args.timeout
    )

    # 运行测试
    runner = APITestRunner(config)
    runner.run_from_config(args.config)

    # 生成报告
    summary = runner.get_summary()
    generator = ReportGenerator(runner.results, summary)

    # 控制台输出
    print(generator.generate_console_report())

    # JSON 报告
    json_file = f"{args.output}/report-{datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
    generator.generate_json_report(json_file)
    print(f"\nJSON 报告已保存: {json_file}")

    # HTML 报告
    if not args.no_html:
        html_file = f"{args.output}/report-{datetime.now().strftime('%Y%m%d-%H%M%S')}.html"
        generator.generate_html_report(html_file)
        print(f"HTML 报告已保存: {html_file}")

    # 返回退出码
    sys.exit(0 if summary["failed"] == 0 else 1)


if __name__ == "__main__":
    main()
