# coding=utf-8
"""Simple HTTP client for communicating with jussi/steem."""

import concurrent.futures
import json
import logging
import socket
import time
from functools import partial
from http.client import RemoteDisconnected
from itertools import cycle

import certifi
import urllib3

from urllib3.connection import HTTPConnection
from urllib3.exceptions import MaxRetryError, ReadTimeoutError, ProtocolError, HTTPError

from hive.steem.exceptions import RPCError, RPCErrorFatal

logger = logging.getLogger(__name__)
logger.setLevel(logging.WARNING)

def chunkify(iterable, chunksize=3000):
    """Yields chunks of an iterator."""
    i = 0
    chunk = []
    for item in iterable:
        chunk.append(item)
        i += 1
        if i == chunksize:
            yield chunk
            i = 0
            chunk = []
    if chunk:
        yield chunk

def _rpc_body(method, args, _id=0):
    if args is None:
        args = [] if 'condenser_api' in method else {}
    return dict(jsonrpc="2.0", id=_id, method=method, params=args)

class HttpClient(object):
    """Simple Steem JSON-HTTP-RPC API"""

    METHOD_API = dict(
        get_block='block_api',
        get_content='condenser_api',
        get_accounts='condenser_api',
        get_order_book='condenser_api',
        get_feed_history='condenser_api',
        get_dynamic_global_properties='database_api',
    )

    def __init__(self, nodes, **kwargs):
        self.max_workers = kwargs.get('max_workers', None)

        num_pools = kwargs.get('num_pools', 10)
        maxsize = kwargs.get('maxsize', 10)
        timeout = kwargs.get('timeout', 60)
        retries = kwargs.get('retries', 20)
        pool_block = kwargs.get('pool_block', False)
        tcp_keepalive = kwargs.get('tcp_keepalive', True)

        if tcp_keepalive:
            socket_options = HTTPConnection.default_socket_options + \
                             [(socket.SOL_SOCKET, socket.SO_KEEPALIVE, 1), ]
        else:
            socket_options = HTTPConnection.default_socket_options

        self.http = urllib3.poolmanager.PoolManager(
            num_pools=num_pools,
            maxsize=maxsize,
            block=pool_block,
            timeout=timeout,
            retries=retries,
            socket_options=socket_options,
            headers={
                'Content-Type': 'application/json',
                'accept-encoding': 'gzip'

            },
            cert_reqs='CERT_REQUIRED',
            ca_certs=certifi.where())
        '''
            urlopen(method, url, body=None, headers=None, retries=None,
            redirect=True, assert_same_host=True, timeout=<object object>,
            pool_timeout=None, release_conn=None, chunked=False, body_pos=None,
            **response_kw)
        '''

        self.nodes = cycle(nodes)
        self.url = ''
        self.request = None
        self.next_node()

        log_level = kwargs.get('log_level', logging.WARNING)
        logger.setLevel(log_level)

    def next_node(self):
        """Switch to the next available node."""
        self.set_node(next(self.nodes))

    def set_node(self, node_url):
        """Change current node to provided node URL."""
        if not self.url == node_url:
            logger.info("HttpClient using node: %s", node_url)
            self.url = node_url
            self.request = partial(self.http.urlopen, 'POST', self.url)

    def rpc_body(self, method, args, is_batch=False):
        """Build JSON request body for steemd RPC requests."""
        fqm = self.METHOD_API[method] + '.' + method

        if not is_batch:
            body = _rpc_body(fqm, args, -1)
        else:
            body = [_rpc_body(fqm, arg, i+1) for i, arg in enumerate(args)]

        return json.dumps(body, ensure_ascii=False).encode('utf8')

    def submit(self, body, method):
        """Submit an RPC request"""
        start = time.perf_counter()
        response = self.request(body=body)
        secs = time.perf_counter() - start
        if secs > 5:
            extra = {'jussi-id': response.headers.get('x-jussi-request-id')}
            logger.warning('%s took %.1fs %s', method, secs, extra)
        return response

    def exec(self, method, args, is_batch=False):
        """Execute a steemd RPC method, retrying on failure."""
        body = self.rpc_body(method, args, is_batch)

        tries = 0
        while tries < 100:
            tries += 1
            try:
                response = self.submit(body, method)
                if response.status != 200:
                    raise HTTPError(response.status, "non-200 response")

                response_data = response.data.decode('utf-8')
                result = json.loads(response_data)
                assert result, "result entirely blank"

                if 'error' in result:
                    raise RPCError.build(result['error'], method, args)

                if not is_batch:
                    assert isinstance(result, dict), "result was not a dict"
                    assert 'result' in result, "response with no result key"
                    if tries > 2:
                        logging.warning("%s took %d tries", method, tries)
                    return result['result']

                # sanity-checking of batch results
                assert isinstance(result, list), "batch result must be list"
                assert len(args) == len(result), "batch result len mismatch"
                for i, item in enumerate(result):
                    id1, id2 = [i+1, item['id']]
                    assert id1 == id2, "got id %s, expected %s" % (id2, id1)
                    if 'error' in item:
                        raise RPCError.build(item['error'], method, args[i], i)
                    assert 'result' in item, "batch[%d] result empty" % i
                if tries > 2:
                    logging.warning("%s took %d tries", method, tries)
                return [item['result'] for item in result]

            except (AssertionError, RPCErrorFatal) as e:
                raise e

            except (RemoteDisconnected, ConnectionResetError, ReadTimeoutError,
                    MaxRetryError, ProtocolError, RPCError, HTTPError) as e:
                logging.error("%s failed, try %d. %s", method, tries, repr(e))

            except json.decoder.JSONDecodeError as e:
                logging.error("invalid JSON returned: %s", response_data)

            except Exception as e:
                logging.error('Unexpected %s: %s', e.__class__.__name__, e)

            if tries % 2 == 0:
                self.next_node()
            time.sleep(tries / 10)

        raise Exception("abort %s after %d tries" % (method, tries))

    def exec_multi(self, name, params, max_workers, batch_size):
        """Process a batch as parallel requests."""
        chunks = [[name, args, True] for args in chunkify(params, batch_size)]
        with concurrent.futures.ThreadPoolExecutor(
            max_workers=max_workers) as executor:
            for items in executor.map(lambda tup: self.exec(*tup), chunks):
                yield list(items) # (use of `map` preserves request order)
