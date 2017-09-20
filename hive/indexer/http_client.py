# coding=utf-8
import concurrent.futures
import json
import logging
import socket
import time
from functools import partial
from http.client import RemoteDisconnected
from itertools import cycle
from urllib.parse import urlparse
import gzip

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

       from steem.http_client import HttpClient
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
        self.return_with_args = kwargs.get('return_with_args', False)
        self.re_raise = kwargs.get('re_raise', True)
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

        self.batch_size = kwargs.get('batch_size', 50)

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
        self.url = node_url
        self.request = partial(self.http.urlopen, 'POST', self.url)

    @property
    def hostname(self):
        return urlparse(self.url).hostname

    @staticmethod
    def json_rpc_body(name, *args, api=None, as_json=True, _id=0):
        """ Build request body for steemd RPC requests.

        Args:
            name (str): Name of a method we are trying to call. (ie: `get_accounts`)
            args: A list of arguments belonging to the calling method.
            api (None, str): If api is provided (ie: `follow_api`),
             we generate a body that uses `call` method appropriately.
            as_json (bool): Should this function return json as dictionary or string.
            _id (int): This is an arbitrary number that can be used for request/response tracking in multi-threaded
             scenarios.

        Returns:
            (dict,str): If `as_json` is set to `True`, we get json formatted as a string.
            Otherwise, a Python dictionary is returned.
        """
        headers = {"jsonrpc": "2.0", "id": _id}
        if api:
            body_dict = {**headers, "method": "call", "params": [api, name, args]}
        else:
            body_dict = {**headers, "method": name, "params": args}
        if as_json:
            return json.dumps(body_dict, ensure_ascii=False).encode('utf8')
        else:
            return body_dict

    def exec(self, name, *args, api=None, return_with_args=None, _ret_cnt=0, body=None):
        """ Execute a method against steemd RPC.

        Warnings:
            This command will auto-retry in case of node failure, as well as handle
            node fail-over, unless we are broadcasting a transaction.
            In latter case, the exception is **re-raised**.
        """
        body = body or HttpClient.json_rpc_body(name, *args, api=api)
        response = None
        try:
            response = self.request(body=body)
        except (MaxRetryError,
                ConnectionResetError,
                ReadTimeoutError,
                RemoteDisconnected,
                ProtocolError) as e:
            # if we broadcasted a transaction, always raise
            # this is to prevent potential for double spend scenario
            if api == 'network_broadcast_api':
                raise e

            # try switching nodes before giving up
            if _ret_cnt > 2:
                time.sleep(_ret_cnt) # we should wait only a short period before trying the next node, but still slowly increase backoff
            elif _ret_cnt > 10:
                raise e
            self.next_node()
            logging.debug('Switched node to %s due to exception: %s' %
                          (self.hostname, e.__class__.__name__))
            return self.exec(name, *args,
                             return_with_args=return_with_args,
                             _ret_cnt=_ret_cnt + 1)
        except Exception as e:
            if self.re_raise:
                raise e
            else:
                extra = dict(err=e, request=self.request)
                logger.info('Request error', extra=extra)
                return self._return(
                    response=response,
                    args=args,
                    return_with_args=return_with_args)
        else:
            if response.status not in tuple(
                    [*response.REDIRECT_STATUSES, 200]):
                logger.info('non 200 response:%s', response.status)

            return self._return(
                response=response,
                args=args,
                return_with_args=return_with_args)

    def _return(self, response=None, args=None, return_with_args=None):
        return_with_args = return_with_args or self.return_with_args
        result = None

        if response:
            try:
                response_json = json.loads(response.data.decode('utf-8'))
                logger.debug(response_json)
            except Exception as e:
                extra = dict(response=response, request_args=args, err=e)
                logger.info('failed to load response', extra=extra)
                result = None
            else:
                if 'error' in response_json:
                    error = response_json['error']

                    if self.re_raise:
                        error_message = error.get(
                            'detail', response_json['error']['message'])
                        raise RPCError("{}: {}".format(error_message, response_json))

                    result = response_json['error']
                elif isinstance(response_json, dict):
                    result = response_json.get('result', None)
                else:
                    result = response_json
        if return_with_args:
            return result, args
        else:
            return result

    def exec_multi_with_futures(self, name, params, api=None, max_workers=None):
        with concurrent.futures.ThreadPoolExecutor(
                max_workers=max_workers) as executor:
            # Start the load operations and mark each future with its URL
            def ensure_list(parameter):
                return parameter if type(parameter) in (list, tuple, set) else [parameter]

            futures = (executor.submit(self.exec, name, *ensure_list(param), api=api)
                       for param in params)
            for future in concurrent.futures.as_completed(futures):
                yield future.result()

    def exec_batch(self, name, params, batch_size=None):
        batch_size = batch_size or self.batch_size

        batch_requests = ({
                "method": name,
                "params": i,
                "jsonrpc": "2.0",
                "id": 0
            } for i in params)


        for batch in chunkify(batch_requests, batch_size):
            body = json.dumps(batch).encode()
            batch_response = self.exec('ignore',[], body=body)
            assert batch_response, "batch_response was empty"
            assert len(batch_response) == len(params), "batch_response len did not match params"
            for response in batch_response:
                yield response['result']


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser('jussi client')

    parser.add_argument('--url', type=str, default='https://api.steemitdev.com')
    parser.add_argument('--start_block', type=int, default=1)
    parser.add_argument('--end_block', type=int, default=15000000)
    parser.add_argument('--batch_request_size', type=int, default=20)
    parser.add_argument('--log_level', type=str, default='DEBUG')

    args = parser.parse_args()

    c = HttpClient(nodes=[args.url], batch_size=args.batch_request_size, re_raise=False)

    block_nums = range(args.start_block, args.end_block)
    for response in c.exec_batch('get_block', block_nums):
        print(json.dumps(response))
