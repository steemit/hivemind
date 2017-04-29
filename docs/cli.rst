hive-cli
~~~~~~~~
`hive` is a convenient CLI utility that enables you to manage the indexer, or spin up the `JSON-RPC` server.

Usage
-----

::

    ~/G/s/hive % hive -h
    Usage: hive [OPTIONS] COMMAND [ARGS]...

      The *hive* CLI manages database, indexer and the server.

      For more detailed information on a command and its flags, run:
          hive COMMAND --help

    Options:
      -h, --help  Show this message and exit.

    Commands:
      db       Database Level Operations.
      indexer  Parse the blockchain and index the MySQL...
      server   HTTP server for answering DB queries


Database Commands
-----------------
Create initial MySQL tables.

::

    hive db ensure-schema

Server Commands
---------------
Spin up a JSON-RPC server.

::

    hive server dev-server --port 1234


Indexer Commands
----------------

Syncing with Blockchain:

::

    hive indexer from-file /path/to/blocks.json.lst

    hive indexer from-steem



Head Block Status:

::

    % hive indexer show-status
    +----------+---------+------------+
    | steemd   | hive    | Difference |
    +----------+---------+------------+
    | 11482113 | 5723313 | 5758800    |
    +----------+---------+------------+
