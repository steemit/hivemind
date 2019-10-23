"""List of muted accounts for server process."""

import logging
from urllib.request import urlopen
import ujson as json

log = logging.getLogger(__name__)

def _read_url(url):
    return urlopen(url).read()

class Mutes:
    """Singleton tracking muted accounts."""

    _instance = None
    accounts = set()
    blist = set()
    blist_map = dict()

    @classmethod
    def instance(cls):
        """Get the shared instance."""
        assert cls._instance, 'set_shared_instance was never called'
        return cls._instance

    @classmethod
    def set_shared_instance(cls, instance):
        """Set the global/shared instance."""
        cls._instance = instance

    def __init__(self, url):
        """Initialize a muted account list by loading from URL"""
        if url:
            self.accounts = set(_read_url(url).decode('utf8').split())
            jsn = _read_url('http://blacklist.usesteem.com/blacklists')
            self.blist = set(json.loads(jsn))
            log.warning("%d muted, %d blacklisted", len(self.accounts), len(self.blist))


    @classmethod
    def all(cls):
        """Return the set of all muted accounts from singleton instance."""
        return cls.instance().accounts

    @classmethod
    def listed(cls):
        """Return blacklisted accounts."""
        return cls.instance().blist

    @classmethod
    def lists(cls, name):
        """Return blacklists the account belongs to."""
        assert name
        inst = cls.instance().blist_map
        if name not in inst:
            url = 'http://blacklist.usesteem.com/user/' + name
            lists = json.loads(_read_url(url))
            inst[name] = lists['blacklisted']
        return inst[name]
