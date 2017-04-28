Modules
~~~~~~~

**hive** is comprised of the following modules:
 - ``hive.indexer``
 - ``hive.db``
 - ``hive.server``
 - ``hive.extras``



Indexer
=======

hive.indexer.core
-----------------

.. automodule:: hive.indexer.core
   :members:

--------

Server
======

.. automodule:: hive.server
   :members:

Health Check
------------
To perform a health check, perform a `GET` on ``/health``.
Returns 200 if everything is OK, and 500 /w json error message otherwise.

JSON-RPC Example
----------------

.. code-block:: python

    from jsonrpcclient.http_client import HTTPClient
    HTTPClient('http://localhost:1234').request('hive.status')

Outputs:

::

    --> {"jsonrpc": "2.0", "method": "hive.status", "id": 8}
    <-- {"result": {"diff": 6396850, "hive": 5042966, "steemd": 11439816}, "id": 8, "jsonrpc": "2.0"} (200 OK)
    Result: {'diff': 6396850, 'hive': 5042966, 'steemd': 11439816}

--------

Community
=========

Community
---------

.. autoclass:: hive.community.community.Community
   :members:

Roles
-----

.. automodule:: hive.community.roles
   :members:

--------


Database
========

hive.db.methods
---------------

.. automodule:: hive.db.methods
   :members:

--------