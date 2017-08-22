# -*- coding: utf-8 -*-
import click
from click import echo
from hive.indexer.core import sync_from_file, sync_from_steemd, head_state
from hive.db.schema import setup
from prettytable import PrettyTable


@click.group()
def indexer():
    """Parse the blockchain and index the MySQL Database.
    
    Source of blocks can be a .json.lst file, steemd, or both.
    """
    pass


#@indexer.command(name='from-file')
#@click.argument('filename', type=click.Path(exists=True))
#def index_from_file(filename):
#    """import blocks from steemd"""
#    echo('Loading blocks from %s...' % filename)
#    sync_from_file(filename)


@indexer.command(name='from-steemd')
def index_from_steemd():
    """import blocks from .json.lst file"""
    echo('Loading blocks from steemd...')
    setup()
    sync_from_steemd(True)


@indexer.command(name='show-status')
def show_status():
    """print head block info"""
    t = PrettyTable(['steemd', 'hive', 'Difference'])
    t.align = "l"
    s = head_state()
    t.add_row([s['steemd'], s['hive'], s['diff']])
    echo(t)
