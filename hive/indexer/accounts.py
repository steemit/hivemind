"""Accounts indexer."""

import logging

from datetime import datetime
from toolz import partition_all

import ujson as json

from hive.db.adapter import Db
from hive.utils.normalize import rep_log10, vests_amount
from hive.utils.timer import Timer
from hive.utils.account import safe_profile_metadata
from hive.utils.unique_fifo import UniqueFIFO
from hive.indexer.community import Community

log = logging.getLogger(__name__)

DB = Db.instance()

class Accounts:
    """Manages account id map, dirty queue, and `hive_accounts` table."""

    # name->id map
    _ids = {}

    # fifo queue
    _dirty = UniqueFIFO()

    # in-mem id->rank map
    _ranks = {}

    # account core methods
    # --------------------

    @classmethod
    def load_ids(cls):
        """Load a full (name: id) dict into memory."""
        assert not cls._ids, "id map already loaded"
        cls._ids = dict(DB.query_all("SELECT name, id FROM hive_accounts"))

    @classmethod
    def clear_ids(cls):
        """Wipe id map. Only used for db migration #5."""
        cls._ids = None

    @classmethod
    def get_id(cls, name):
        """Get account id by name. Throw if not found."""
        assert name in cls._ids, "account does not exist or was not registered"
        return cls._ids[name]

    @classmethod
    def exists(cls, name):
        """Check if an account name exists."""
        return name in cls._ids

    @classmethod
    def register(cls, names, block_date):
        """Block processing: register "candidate" names.

        There are four ops which can result in account creation:
        *account_create*, *account_create_with_delegation*, *pow*,
        and *pow2*. *pow* ops result in account creation only when
        the account they name does not already exist!
        """

        # filter out names which already registered
        new_names = list(filter(lambda n: not cls.exists(n), set(names)))
        if not new_names:
            return

        for name in new_names:
            DB.query("INSERT INTO hive_accounts (name, created_at) "
                     "VALUES (:name, :date)", name=name, date=block_date)

        # pull newly-inserted ids and merge into our map
        sql = "SELECT name, id FROM hive_accounts WHERE name IN :names"
        for name, _id in DB.query_all(sql, names=tuple(new_names)):
            cls._ids[name] = _id

        # post-insert: pass to communities to check for new registrations
        Community.register(new_names, block_date)

    # account cache methods
    # ---------------------

    @classmethod
    def dirty(cls, accounts):
        """Marks given accounts as needing an update."""
        return cls._dirty.extend(accounts)

    @classmethod
    def dirty_all(cls):
        """Marks all accounts as dirty. Use to rebuild entire table."""
        cls.dirty(set(DB.query_col("SELECT name FROM hive_accounts")))

    @classmethod
    def dirty_oldest(cls, limit=50000):
        """Flag `limit` least-recently updated accounts for update."""
        sql = "SELECT name FROM hive_accounts ORDER BY cached_at LIMIT :limit"
        return cls.dirty(set(DB.query_col(sql, limit=limit)))

    @classmethod
    def flush(cls, steem, trx=False, spread=1):
        """Process all accounts flagged for update.

         - trx: bool - wrap the update in a transaction
         - spread: int - spread writes over a period of `n` calls
        """
        accounts = cls._dirty.shift_portion(spread)

        count = len(accounts)
        if not count:
            return 0

        if trx:
            log.info("[SYNC] update %d accounts", count)

        cls._cache_accounts(accounts, steem, trx=trx)
        return count

    @classmethod
    def fetch_ranks(cls):
        """Rebuild account ranks and store in memory for next update."""
        sql = "SELECT id FROM hive_accounts ORDER BY vote_weight DESC"
        for rank, _id in enumerate(DB.query_col(sql)):
            cls._ranks[_id] = rank + 1

    @classmethod
    def _cache_accounts(cls, accounts, steem, trx=True):
        """Fetch all `accounts` and write to db."""
        timer = Timer(len(accounts), 'account', ['rps', 'wps'])
        for name_batch in partition_all(1000, accounts):
            cached_at = datetime.now().strftime('%Y-%m-%dT%H:%M:%S')

            timer.batch_start()
            batch = steem.get_accounts(name_batch)

            timer.batch_lap()
            sqls = [cls._sql(acct, cached_at) for acct in batch]
            DB.batch_queries(sqls, trx)

            timer.batch_finish(len(batch))
            if trx or len(accounts) > 1000:
                log.info(timer.batch_status())

    @classmethod
    def _sql(cls, account, cached_at):
        """Prepare a SQL query from a steemd account."""
        vote_weight = (vests_amount(account['vesting_shares'])
                       + vests_amount(account['received_vesting_shares'])
                       - vests_amount(account['delegated_vesting_shares']))

        # remove empty keys
        useless = ['transfer_history', 'market_history', 'post_history',
                   'vote_history', 'other_history', 'tags_usage',
                   'guest_bloggers']
        for key in useless:
            del account[key]

        # pull out valid profile md and delete the key
        profile = safe_profile_metadata(account)
        del account['json_metadata']

        active_at = max(account['created'],
                        account['last_post'],
                        account['last_vote_time'])

        values = {
            'name':         account['name'],
            'created_at':   account['created'],
            'proxy':        account['proxy'],
            'post_count':   account['post_count'],
            'reputation':   rep_log10(account['reputation']),
            'proxy_weight': vests_amount(account['vesting_shares']),
            'vote_weight':  vote_weight,
            'active_at':    active_at,
            'cached_at':    cached_at,

            'display_name':  profile['name'],
            'about':         profile['about'],
            'location':      profile['location'],
            'website':       profile['website'],
            'profile_image': profile['profile_image'],
            'cover_image':   profile['cover_image'],

            'raw_json': json.dumps(account)}

        # update rank field, if present
        _id = cls.get_id(account['name'])
        if _id in cls._ranks:
            values['rank'] = cls._ranks[_id]

        bind = ', '.join([k+" = :"+k for k in list(values.keys())][1:])
        return ("UPDATE hive_accounts SET %s WHERE name = :name" % bind, values)
