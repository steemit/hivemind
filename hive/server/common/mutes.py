"""List of muted accounts for server process."""

import logging
from urllib.request import urlopen

log = logging.getLogger(__name__)

class Mutes:
    """Singleton tracking muted accounts."""

    _instance = None
    accounts = set()

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
            self.accounts = set(urlopen(url).read().decode('utf8').split())

    @classmethod
    def all(cls):
        """Return the set of all muted accounts from singleton instance."""
        return cls.instance().accounts
