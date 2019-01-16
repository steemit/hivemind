# coding=utf-8
import sys

from setuptools import find_packages
from setuptools import setup

assert sys.version_info[0] == 3 and sys.version_info[1] >= 6, "hive requires Python 3.6 or newer"

tests_require = [
    'pytest',
    'pytest-cov',
    'pytest-pylint',
    'pytest-asyncio',
    'pytest-console-scripts',
    'git-pylint-commit-hook',
    'pep8',
    'yapf',
]

# yapf: disable
setup(
    name='hivemind',
    version='0.0.1',
    description='Developer-friendly microservice powering social networks on the Steem blockchain.',
    long_description=open('README.md').read(),
    packages=find_packages(exclude=['scripts']),
    setup_requires=['pytest-runner'],
    tests_require=tests_require,
    install_requires=[
        'aiopg @ https://github.com/aio-libs/aiopg/tarball/862fff97e4ae465333451a4af2a838bfaa3dd0bc',
        'jsonrpcserver==4.0.1',
        'aiohttp',
        'certifi',
        'sqlalchemy',
        'funcy',
        'toolz',
        'maya',
        'ujson',
        'urllib3',
        'psycopg2-binary',
        'aiocache',
        'configargparse',
        'pdoc',
    ],
    extras_require={'test': tests_require},
    entry_points={
        'console_scripts': [
            'hive=hive.cli:run',
        ]
    })
