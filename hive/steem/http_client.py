# coding=utf-8
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
    pass

def chunkify(iterable, chunksize=3000):
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
    """ Simple Steem JSON-HTTP-RPC API

    This class serves as an abstraction layer for easy use of the Steem API.

    Args:
      nodes (list): A list of Steem HTTP RPC nodes to connect to.

    .. code-block:: python

       rpc = HttpClient(['https://steemd-node1.com', 'https://steemd-node2.com'])

    any call available to that port can be issued using the instance
    via the syntax ``rpc.exec('command', *parameters)``.

    Example:

    .. code-block:: python

       rpc.exec(
           'get_followers',
           'furion', 'abit', 'blog', 10,
           api='follow_api'
       )

    """

    def __init__(self, nodes, **kwargs):
        self.max_workers = kwargs.get('max_workers', None)
        self.use_appbase = kwargs.get('use_appbase', True)

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

    def rpc_body(self, method, args, api=None, jsonrpc_id=0):
        """ Build request body for steemd RPC requests."""
        assert isinstance(args, (list, tuple, set)), "args must be list"

        if self.use_appbase:
            method = "condenser_api."+method

        if api: # TODO: does this xform need to happen before condenser_api?
            args = [api, method, args]
            method = "call"

        return {"jsonrpc": "2.0",
                "id": jsonrpc_id,
                "method": method,
                "params": args}

    def _exec(self, body, _ret_cnt=0):
        """ Execute a method against steemd RPC.

            Warning: Auto-retry on failure, including broadcasting a tx.
        """

        assert isinstance(body, (dict, list)), "body must be dict or list"
        is_batch = isinstance(body, list)

        try:
            encoded_body = json.dumps(body, ensure_ascii=False).encode('utf8')
            response = self.request(body=encoded_body)

            # check response status
            if response.status not in tuple(
                    [*response.REDIRECT_STATUSES, 200]):
                raise RPCError("non-200 response:%s" % response.status)

            # check response format/success
            result = json.loads(response.data.decode('utf-8'))
            if not result:
                raise Exception("result entirely blank")
            if 'error' in result:
                error = result['error']
                if error['code'] == -32002 and 'api.method' in error['message']:
                    raise RPCError("missing appbase flag? {}".format(result))
                raise RPCError("result['error'] -- {}".format(result))

            # pylint: disable=no-else-return
            # final sanity checks and trimming
            if is_batch:
                assert isinstance(result, list), "batch result must be list"
                assert len(body) == len(result), "batch result len mismatch"
                for item in result:
                    assert 'result' in item, "batch response empty item: {}".format(result)
                    assert 'error' not in item, "batch response error item: {}".format(result)
                return [item['result'] for item in result]
            else:
                assert isinstance(result, dict), "non-batch result must be dict"
                return result['result']

        except (MaxRetryError,
                ConnectionResetError,
                ReadTimeoutError,
                RemoteDisconnected,
                ProtocolError) as e:

            if _ret_cnt > 10:
                raise e
            elif _ret_cnt > 2:
                time.sleep(_ret_cnt)

            self.next_node()
            logging.error("call failed, retry %d. %s", _ret_cnt, repr(e))
            return self._exec(body, _ret_cnt=_ret_cnt + 1)

        except Exception as e:
            raise e

    def exec(self, name, *args, api=None):
        body = self.rpc_body(name, args, api=api)
        return self._exec(body)

    def exec_multi_with_futures(self, name, params, api=None, max_workers=None):
        with concurrent.futures.ThreadPoolExecutor(
            max_workers=max_workers) as executor:
            futures = (executor.submit(self.exec, name, *args, api=api)
                       for args in params)
            for future in concurrent.futures.as_completed(futures):
                yield future.result()

    def exec_batch(self, name, params, batch_size):
        for batch in chunkify(params, batch_size):
            calls = [self.rpc_body(name, args) for args in batch]
            response = self._exec(body=calls)
            for item in response:
                yield item

def run():
    import argparse
    parser = argparse.ArgumentParser('jussi client')
    parser.add_argument('--url', type=str, default='https://api.steemitdev.com')
    parser.add_argument('--start_block', type=int, default=1)
    parser.add_argument('--end_block', type=int, default=15000000)
    parser.add_argument('--batch_request_size', type=int, default=20)
    parser.add_argument('--log_level', type=str, default='DEBUG')
    args = parser.parse_args()

    client = HttpClient(nodes=[args.url], batch_size=args.batch_request_size)
    block_nums = range(args.start_block, args.end_block)
    for response in client.exec_batch('get_block', block_nums, 50):
        print(json.dumps(response))

if __name__ == '__main__':
    run()
