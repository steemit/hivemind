FROM python:3.11-slim

WORKDIR /app

# 安装依赖
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 复制测试文件
COPY api_comparator.py .
COPY test_cases.yaml .

# 创建结果目录
RUN mkdir -p results

# 设置入口点
ENTRYPOINT ["python", "api_comparator.py"]
