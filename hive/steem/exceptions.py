"""Defines exceptions which can be thrown by HttpClient."""

def _str_trunc(value, max_length):
    value = str(value)
    if len(value) > max_length:
        value = value[0:max_length] + '...'
    return value

class RPCError(Exception):
    """Raised when an error is returned from upstream (jussi/steem)."""

    @staticmethod
    def build(error, body, index=None):
        """Given an RPC error, builds exception w/ appropriate severity."""
        assert 'message' in error, "missing error msg key: {}".format(error)
        assert 'code' in error, "missing error code key: {}".format(error)

        if isinstance(body, list):
            item = body[index] if index else body[0]
            method = item['method'] + ('[%s]' % index if index else '')
            params = '[%s, (%d more)]' % (item['params'], len(body) - 1)
        else:
            method = body['method']
            params = _str_trunc(body['params'], 1024)

        message = RPCError.humanize(error)
        message += ' in %s(%s)' % (method, params)

        if not RPCError.is_recoverable(error):
            return RPCErrorFatal(message)
        return RPCError(message)

    @staticmethod
    def is_recoverable(error):
        """Check if this error is transient/retriable.

        For now, retry all errors. See #126 for details. A blacklist
        would be more appropriate but since hive uses only 'prepared
        queries', fatal errors can only be due to dev error.
        """
        #pylint: disable=unused-argument
        return True

    @staticmethod
    def humanize(error):
        """Get friendly error string from steemd RPC response."""
        message = error['message'] if 'message' in error else str(error)
        code = error['code'] if 'code' in error else -1

        info = ''
        if 'data' not in error: # eg db_lock_error
            name = 'error'
        elif 'name' in error['data']: # steemd errs
            name = error['data']['name']
        elif 'error_id' in error['data']: # jussi errs
            if 'exception' in error['data']:
                name = error['data']['exception']
            else:
                name = 'unspecified exception'
            info = '[jussi:%s]' % error['data']['error_id']
        else:
            name = 'unspecified error'
            info = str(error)

        return "%s[%s]: `%s` %s" % (name, code, message, info)

class RPCErrorFatal(RPCError):
    """Represents a steemd error which is not recoverable."""
    pass
