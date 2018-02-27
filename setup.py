# coding=utf-8
import sys

from setuptools import find_packages
from setuptools import setup

assert sys.version_info[0] == 3 and sys.version_info[1] >= 5, "hive requires Python 3.5 or newer"

# yapf: disable
setup(
    name='hivemind',
    version='0.0.1',
    description='Community consensus layer for the Steem blockchain',
    long_description=open('README.md').read(),
    packages=find_packages(exclude=['scripts']),
    setup_requires=['pytest-runner'],
    tests_require=['pytest',
                   'pep8',
                   'pytest-pylint',
                   'yapf',
                   'pytest-console-scripts'],

    install_requires=[
        'aiopg',
        'jsonrpcserver',
        'aiohttp',
        'certifi',
        'sqlalchemy',
        'funcy',
        'toolz',
        'maya',
        'ujson',
        'urllib3',
        'psycopg2',
        'aiocache',
        'configargparse',
    ],
    entry_points={
        'console_scripts': [
            'hive=hive.cli:run',
        ]
    })
