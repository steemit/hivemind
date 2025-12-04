"""Hive server and API tests."""
from hive.conf import Conf
from hive.db.adapter import Db

Db.set_shared_instance(Conf.init_test().db())
