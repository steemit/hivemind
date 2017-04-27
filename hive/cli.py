import click
from hive.db.cli import db
from hive.indexer.cli import indexer
from hive.server.cli import server


@click.group(
    short_help='manages storage, retrieval, and querying of the Steem blockchain')
def cli():
    """The *hive* CLI manages storage, retrieval, and querying of the Steem
    blockchain.

    hive has several commands, each of which has additional subcommands.

    \b
    For more detailed information on a command and its flags, run:
        hive COMMAND --help
    """


cli.add_command(db)
cli.add_command(indexer)
cli.add_command(server)
