# -*- coding: utf-8 -*-
import click
import logging

from hive.server import lazy_load_dev_server

logger = logging.getLogger(__name__)


@click.group()
def server():
    """HTTP server for answering DB queries"""
    pass


# Development server
@server.command(name='dev-server')
@click.option(
    '--port',
    type=click.INT,
    default=8080,
    help='localhost TCP port for server')
@click.option('--no_debug', is_flag=True)
def dev_server_command(port, no_debug):
    """development server"""
    debug = not no_debug
    dev_server = lazy_load_dev_server()
    dev_server(port, debug)
