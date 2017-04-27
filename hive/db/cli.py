# -*- coding: utf-8 -*-
import click
from hive.db.schema import setup, teardown


@click.group()
def db():
    """Database Level Operations.
    
    Manage schema or run administrative commands against hive MySQL.
    """
    pass


@db.command(name='ensure-schema')
@click.option(
    '--database_url',
    type=str,
    envvar='DATABASE_URL',
    required=True,
    help='Database connection URL in RFC-1738 format, read from "DATABASE_URL" ENV var by default'
)
def ensure_schema(database_url):
    """re-create db schema (WARN: will wipe data)"""
    teardown(database_url)
    setup(database_url)
