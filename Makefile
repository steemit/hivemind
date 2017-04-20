SHELL := /bin/bash
ROOT_DIR := $(shell pwd)

PROJECT_NAME := hive
PROJECT_DOCKER_TAG := steemit/$(PROJECT_NAME)
PROJECT_DOCKER_RUN_ARGS := -p8080:8080

default: build

.PHONY: test run test-without-lint test-pylint fmt test-without-build build

build:
	docker build -t $(PROJECT_DOCKER_TAG) .

run:
	docker run $(PROJECT_DOCKER_RUN_ARGS) $(PROJECT_DOCKER_TAG)

ipython:
    docker run -it $(PROJECT_DOCKER_TAG) ipython

test: test-without-build build

test-without-build: test-without-lint test-pylint


test-without-lint:
	py.test tests

test-pylint:
	py.test --pylint -m pylint $(PROJECT_NAME)

fmt:
	yapf --recursive --in-place --style pep8 .
	autopep8 --recursive --in-place .

requirements.txt: serve.py
	pip freeze > $@

clean: clean-build clean-pyc

clean-build:
	rm -fr build/ dist/ *.egg-info .eggs/ .tox/ __pycache__/ .cache/ .coverage htmlcov src

clean-pyc:
	find . -name '*.pyc' -exec rm -f {} +
	find . -name '*.pyo' -exec rm -f {} +
	find . -name '*~' -exec rm -f {} +

install: clean
	pip install -e .