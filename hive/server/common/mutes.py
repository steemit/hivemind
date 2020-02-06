"""List of muted accounts for server process."""

import logging
from time import perf_counter as perf
from urllib.request import urlopen
import ujson as json

log = logging.getLogger(__name__)

def _read_url(url):
    return urlopen(url).read()

class Mutes:
    """Singleton tracking muted accounts."""

    _instance = None
    url = None
    accounts = set() # list/irredeemables
    blist = set() # list/any-blacklist
    blist_map = dict() # cached account-list map
    fetched = None

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
        self.url = url
        if url:
            self.load()

    def load(self):
        """Reload all accounts from irredeemables endpoint and global lists."""
        self.accounts = set(_read_url(self.url).decode('utf8').split())
        jsn = _read_url('http://blacklist.usesteem.com/blacklists')
        self.blist = set(json.loads(jsn))
        self.blist_map = dict()
        log.warning("%d muted, %d blacklisted", len(self.accounts), len(self.blist))
        self.fetched = perf()

    @classmethod
    def all(cls):
        """Return the set of all muted accounts from singleton instance."""
        return cls.instance().accounts

    @classmethod
    def lists(cls, name, rep):
        """Return blacklists the account belongs to."""
        assert name
        inst = cls.instance()

        # update hourly
        if perf() - inst.fetched > 3600:
            inst.load()

        if name not in inst.blist_map:
            out = []
            if name in inst.blist:
                url = 'http://blacklist.usesteem.com/user/' + name
                lists = json.loads(_read_url(url))
                out.extend(lists['blacklisted'])

            if name in inst.accounts:
                if 'irredeemables' not in out:
                    out.append('irredeemables')

            if int(rep) < 1:
                out.append('reputation-0')
            elif int(rep) == 1:
                out.append('reputation-1')

            inst.blist_map[name] = out

        return inst.blist_map[name]
