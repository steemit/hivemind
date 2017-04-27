# -*- coding: utf-8 -*-
import logging

import click
import click_spinner
from click import echo
from hive.indexer.core import sync_from_file, sync_from_steemd

logger = logging.getLogger(__name__)


@click.group()
def indexer():
    """Parse the blockchain and index the MySQL Database.
    
    Source of blocks can be a .json.lst file, steemd, or both.
    """
    pass


@indexer.command(name='from-file')
@click.argument('filename', type=click.Path(exists=True))
def index_from_file(filename):
    """import blocks from .json.lst file"""
    echo('Loading blocks from %s...' % filename)
    with click_spinner.spinner():
        sync_from_file(filename)


@indexer.command(name='from-steemd')
def index_from_steemd():
    """import blocks from .json.lst file"""
    echo('Loading blocks from steemd...')
    with click_spinner.spinner():
        sync_from_steemd()
