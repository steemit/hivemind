"""Defines exceptions which can be thrown by HttpClient."""

class RPCError(Exception):
    """Represents a structured error returned from Steem/Jussi"""

    @staticmethod
    def build(error, method, args, index=None):
        """Given an RPC error, builds exception w/ appropriate severity."""
        assert 'message' in error, "missing error msg key: {}".format(error)
        assert 'code' in error, "missing error code key: {}".format(error)

        index = '[%d]' % index if index else ''
        message = RPCError.humanize(error)
        message += ' in %s%s(%s)' % (method, index, str(args)[0:1024])

        #if not RPCError.is_recoverable(error):
        #    return RPCErrorFatal(message)
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

        if 'data' not in error: # eg db_lock_error
            name = 'error'
        elif 'name' in error['data']: # steemd errs
            name = error['data']['name']
        elif 'error_id' in error['data']: # jussi errs
            if 'exception' in error['data']:
                etype = error['data']['exception']
            else:
                etype = 'unspecified exception'
            name = '%s [jussi:%s]' % (etype, error['data']['error_id'])
        else:
            name = 'error [unspecified:%s]' % str(error)

        return "%s[%s]: `%s`" % (name, code, message)

class RPCErrorFatal(RPCError):
    """Represents a structured steemd error which is not recoverable."""
    pass
