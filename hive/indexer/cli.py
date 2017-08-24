# -*- coding: utf-8 -*-
import click
from click import echo
from hive.indexer.core import run, head_state
from hive.db.schema import setup
from prettytable import PrettyTable


@click.group()
def indexer():
    """Parse the blockchain and index the MySQL Database.
    
    Source of blocks can be a .json.lst file, steemd, or both.
    """
    pass


@indexer.command(name='run')
def index_from_steemd():
    """sync up to head block, then listen"""
    echo('Starting hivemind...')
    run()


@indexer.command(name='show-status')
def show_status():
    """print head block info"""
    t = PrettyTable(['steemd', 'hive', 'Difference'])
    t.align = "l"
    s = head_state()
    t.add_row([s['steemd'], s['hive'], s['diff']])
    echo(t)
