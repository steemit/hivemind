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
from urllib3.exceptions import MaxRetryError, ReadTimeoutError, ProtocolError

logger = logging.getLogger(__name__)
logger.setLevel(logging.WARNING)

class RPCError(Exception):
    """Represents a structured error returned from Steem/Jussi"""

    @staticmethod
    def humanize(result):
        """Get friendly error string from steemd RPC response."""
        error = result['error']
        detail = error['message'] if 'message' in error else str(error)

        if 'data' not in error:
            name = 'error' # eg db_lock_error
        elif 'name' not in error['data']:
            name = 'error2' # eg jussi error
        else:
            name = error['data']['name']

        return "%s: `%s`" % (name, detail)

    @staticmethod
    def is_recoverable(result):
        """Check if error appears recoverable (e.g. network condition)"""
        error = result['error']
        assert 'message' in error, "missing error msg key: {}".format(error)
        assert 'code' in error, "missing error code key: {}".format(error)
        message = error['message']
        code = error['code']

        # common steemd error
        # {"code"=>-32003, "message"=>"Unable to acquire database lock"}
        if message == 'Unable to acquire database lock':
            return True

        # rare steemd error
        # {"code"=>-32000, "message"=>"Unknown exception",
        #  "data"=>"0 exception: unspecified\nUnknown Exception\n[...]"}
        if message == 'Unknown exception':
            return True

        # generic jussi error
        # {'code': -32603, 'message': 'Internal Error', 'data': {
        #    'error_id': 'c7a15140-f306-4727-acbd-b5e8f3717e9b',
        #    'request': {'amzn_trace_id': 'Root=1-5ad4cb9f-...',
        #      'jussi_request_id': None}}}
        if message == 'Internal Error' and code == -32603:
            return True

        return False

class RPCErrorFatal(RPCError):
    """Represents a structured steemd error which is not recoverable."""
    pass

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


class HttpClient(object):
    """ Simple Steem JSON-HTTP-RPC API """

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
        """ Switch to the next available node.

        This method will change base URL of our requests.
        Use it when the current node goes down to change to a fallback node. """
        self.set_node(next(self.nodes))

    def set_node(self, node_url):
        """ Change current node to provided node URL. """
        if self.url == node_url:
            return
        logger.info("HttpClient using node: %s", node_url)
        self.url = node_url
        self.request = partial(self.http.urlopen, 'POST', self.url)

    def rpc_body(self, method, args, is_batch=False, _id=0):
        """ Build request body for steemd RPC requests."""
        if is_batch:
            return [self.rpc_body(method, _arg, _id=i+1)
                    for i, _arg in enumerate(args)]

        return dict(jsonrpc="2.0", id=_id, method=method, params=args)

    def exec(self, method, args, is_batch=False):
        """ Execute a method against steemd RPC.

            Warning: Auto-retry on failure, including broadcasting a tx.
        """

        fqm = self.METHOD_API[method] + '.' + method
        body = self.rpc_body(fqm, args, is_batch)
        body_json = json.dumps(body, ensure_ascii=False).encode('utf8')

        tries = 0
        while True:
            try:
                response = self.request(body=body_json)
                if response.status != 200:
                    raise RPCError("non-200 response: %s" % response.status)

                response_data = response.data.decode('utf-8')
                result = json.loads(response_data)
                assert result, "result entirely blank"

                if 'error' in result:
                    if RPCError.is_recoverable(result):
                        raise RPCError(RPCError.humanize(result))
                    raise RPCErrorFatal(RPCError.humanize(result))

                if not is_batch:
                    assert isinstance(result, dict), "result was not a dict"
                    return result['result']

                # sanity-checking of batch results
                assert isinstance(result, list), "batch result must be list"
                assert len(args) == len(result), "batch result len mismatch"
                for i, item in enumerate(result):
                    if 'error' in item:
                        raise RPCError("batch[%d] error: %s in %s(%s)" % (
                            i, RPCError.humanize(item), method, args[i]))
                    assert 'result' in item, "batch[%d] response empty" % i
                return [item['result'] for item in result]

            except (AssertionError, RPCErrorFatal) as e:
                raise e

            except (MaxRetryError, ConnectionResetError, ReadTimeoutError,
                    RemoteDisconnected, ProtocolError, RPCError) as e:
                self.next_node()
                logging.error("%s failed, try %d. %s", method, tries, repr(e))

            except json.decoder.JSONDecodeError as e:
                logging.error("invalid JSON response: %s", response_data)

            except Exception as e:
                logging.error('Unexpected %s: %s', e.__class__.__name__, e)

            if tries >= 50:
                raise Exception("giving up after %d tries" % tries)
            tries += 1
            time.sleep(tries / 10)

    def exec_multi_with_futures(self, name, params, max_workers=None):
        """Process a batch as parallel signular requests."""
        with concurrent.futures.ThreadPoolExecutor(
            max_workers=max_workers) as executor:
            futures = (executor.submit(self.exec, name, args)
                       for args in params)
            for future in concurrent.futures.as_completed(futures):
                yield future.result()

    def exec_batch(self, name, params, batch_size):
        """Chunkify batch requests and return them in order"""
        for batch_params in chunkify(params, batch_size):
            for item in self.exec(name, batch_params, is_batch=True):
                yield item
