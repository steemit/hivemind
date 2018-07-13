# coding=utf-8
"""Simple HTTP client for communicating with jussi/steem."""

from concurrent.futures import ThreadPoolExecutor, as_completed
import logging
import socket
from functools import partial
from itertools import cycle
from time import sleep, perf_counter as perf
import ujson as json

import certifi
import urllib3

from urllib3.util import Retry
from urllib3.connection import HTTPConnection
from urllib3.exceptions import HTTPError

from hive.steem.exceptions import RPCError, RPCErrorFatal

logging.getLogger('urllib3.connectionpool').setLevel(logging.WARNING)
log = logging.getLogger(__name__)

def validated_json_payload(response):
    """Asserts that the HTTP response was successful and valid JSON."""
    if response.status != 200:
        raise HTTPError(response.status, "non-200 response")

    try:
        data = response.data.decode('utf-8')
        payload = json.loads(data)
    except Exception as e:
        raise Exception("JSON error %s: %s" % (str(e), data[0:1024]))

    return payload

def validated_result(payload, body):
    """Asserts that the JSON-RPC payload is valid/sane."""
    assert payload, "response entirely blank"
    if 'error' in payload:
        raise RPCError.build(payload['error'], body)
    if isinstance(body, list):
        return _validated_batch_result(payload, body)

    assert isinstance(payload, dict), "response was not a dict"
    assert body['id'] == payload['id'], "response id mismatch"
    assert 'result' in payload, "response with no result key"
    return payload['result']

def _validated_batch_result(payload, body):
    """Asserts that the batch payload, and each item, is valid/sane."""
    assert isinstance(payload, list), "batch result must be list"
    assert len(body) == len(payload), "batch result len mismatch"
    for req, res in zip(body, payload):
        assert req['id'] == res['id'], "id mismatch: %s -> %s" % (req, res)
    for idx, item in enumerate(payload):
        if 'error' in item:
            raise RPCError.build(item['error'], body, idx)
        assert 'result' in item, "batch[%d] resp empty" % idx
    return [item['result'] for item in payload]

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
        if kwargs.get('tcp_keepalive', True):
            socket_options = HTTPConnection.default_socket_options + \
                             [(socket.SOL_SOCKET, socket.SO_KEEPALIVE, 1), ]
        else:
            socket_options = HTTPConnection.default_socket_options

        self.http = urllib3.poolmanager.PoolManager(
            num_pools=kwargs.get('num_pools', 10),
            maxsize=kwargs.get('maxsize', 64),
            timeout=kwargs.get('timeout', 30),
            socket_options=socket_options,
            block=False,
            retries=Retry(total=False),
            headers={
                'Content-Type': 'application/json',
                'accept-encoding': 'gzip'},
            cert_reqs='CERT_REQUIRED',
            ca_certs=certifi.where())

        self.nodes = cycle(nodes)
        self.url = ''
        self.request = None
        self.next_node()

    def next_node(self):
        """Switch to the next available node."""
        self.set_node(next(self.nodes))

    def set_node(self, node_url):
        """Change current node to provided node URL."""
        if not self.url == node_url:
            log.info("using node: %s", node_url)
            self.url = node_url
            self.request = partial(self.http.urlopen, 'POST', self.url)

    def rpc_body(self, method, args, is_batch=False):
        """Build JSON request body for steemd RPC requests."""
        fqm = self.METHOD_API[method] + '.' + method

        if not is_batch:
            body = _rpc_body(fqm, args, -1)
        else:
            body = [_rpc_body(fqm, arg, i+1) for i, arg in enumerate(args)]

        return body

    def exec(self, method, args, is_batch=False):
        """Execute a steemd RPC method, retrying on failure."""
        what = "%s[%d]" % (method, len(args) if is_batch else 1)
        body = self.rpc_body(method, args, is_batch)
        body_data = json.dumps(body, ensure_ascii=False).encode('utf8')

        tries = 0
        while tries < 100:
            tries += 1
            secs = -1
            info = None
            try:
                start = perf()
                response = self.request(body=body_data)
                secs = perf() - start

                info = {'jussi-id': response.headers.get('x-jussi-request-id'),
                        'secs': round(secs, 3),
                        'try': tries}

                # strict validation/asserts, error check
                payload = validated_json_payload(response)
                result = validated_result(payload, body)

                if secs > 5:
                    log.warning('%s took %.1fs %s', what, secs, info)
                if tries > 2:
                    log.warning('%s took %d tries %s', what, tries, info)

                return result

            except (AssertionError, RPCErrorFatal) as e:
                raise e

            except (Exception, socket.timeout) as e:
                if secs < 0: # request failed
                    secs = perf() - start
                    info = {'secs': round(secs, 3), 'try': tries}
                log.error('%s failed in %.1fs. try %d. %s - %s',
                          what, secs, tries, info, repr(e))

            if tries % 2 == 0:
                self.next_node()
            sleep(tries / 10)

        raise Exception("abort %s after %d tries" % (method, tries))

    def exec_multi(self, name, params, max_workers, batch_size):
        """Process a batch as parallel requests."""
        chunks = [[name, args, True] for args in chunkify(params, batch_size)]
        with ThreadPoolExecutor(max_workers=max_workers) as executor:
            for items in executor.map(lambda tup: self.exec(*tup), chunks):
                yield list(items) # (use of `map` preserves request order)

    def exec_multi_as_completed(self, name, params, max_workers, batch_size):
        """Process a batch as parallel requests; yields unordered."""
        chunks = [[name, args, True] for args in chunkify(params, batch_size)]
        with ThreadPoolExecutor(max_workers=max_workers) as executor:
            futures = (executor.submit(self.exec, *tup) for tup in chunks)
            for future in as_completed(futures):
                yield future.result()
