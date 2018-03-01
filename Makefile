SHELL := /bin/bash
ROOT_DIR := $(shell pwd)

PROJECT_NAME := hive
PROJECT_DOCKER_TAG := steemit/$(PROJECT_NAME)
PROJECT_DOCKER_RUN_ARGS := --link db:db

default: build

.PHONY: test run test-without-lint test-pylint fmt test-without-build build docs

docs:
	pdoc --html hive --html-dir docs --overwrite

build:
	docker build -t $(PROJECT_DOCKER_TAG) .

run:
	docker run -it $(PROJECT_DOCKER_RUN_ARGS) $(PROJECT_DOCKER_TAG) /bin/bash

compose:
	docker-compose up -d

db:
	docker run -d --name hive_db -p 5432:5432 -e POSTGRES_PASSWORD=root_password -e POSTGRES_DATABASE=hivepy postgres

mysql:
	docker run --env DATABASE_URL=mysql://root:root_password@mysql:3306/testdb -p 4000:8080 hive

serve-local:
	pipenv run python hive/server/serve.py --port 8080 --database_url='mysql://root:root_password@127.0.0.1:3306/testdb'

.PHONY: db-head-state
db-head-state:
	curl -H 'Content-Type: application/json' -d '{"id":1,"jsonrpc":"2.0","method":"db_head_state"}' http://localhost:8080

ipython:
	docker run -it $(PROJECT_DOCKER_RUN_ARGS) $(PROJECT_DOCKER_TAG) ipython

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
	pip3 install -e .
