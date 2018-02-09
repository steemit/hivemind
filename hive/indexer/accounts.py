import math
import json
import re

from collections import deque
from datetime import datetime
from toolz import partition_all

from hive.db.methods import query_col, query, query_all
from hive.indexer.steem_client import get_adapter
from hive.utils.normalize import rep_log10, amount, trunc
from hive.utils.timer import Timer

class Accounts:
    _ids = {}
    _dirty = deque()

    # account core methods
    # --------------------

    @classmethod
    def load_ids(cls):
        assert not cls._ids, "id map only needs to be loaded once"
        cls._ids = dict(query_all("SELECT name, id FROM hive_accounts"))

    @classmethod
    def get_id(cls, name):
        assert name in cls._ids, "account does not exist or was not registered"
        return cls._ids[name]

    @classmethod
    def exists(cls, name):
        return name in cls._ids

    @classmethod
    def register(cls, names, block_date):
        new_names = list(filter(lambda n: not cls.exists(n), set(names)))
        if not new_names:
            return

        # insert new names and add the new ids to our mem map
        for name in new_names:
            query("INSERT INTO hive_accounts (name, created_at) "
                  "VALUES (:name, :date)", name=name, date=block_date)

        sql = "SELECT name, id FROM hive_accounts WHERE name IN :names"
        cls._ids = {**dict(query_all(sql, names=tuple(new_names))), **cls._ids}


    # account cache methods
    # ---------------------

    @classmethod
    def dirty(cls, accounts):
        if not accounts:
            return 0
        if isinstance(accounts, str):
            accounts = [accounts]
        accounts = set(accounts) - set(cls._dirty)
        cls._dirty.extend(accounts)
        return len(accounts)

    @classmethod
    def dirty_all(cls):
        cls.dirty(query_col("SELECT name FROM hive_accounts"))

    @classmethod
    def dirty_oldest(cls, limit=50000):
        print("[HIVE] flagging %d oldest accounts for update" % limit)
        sql = "SELECT name FROM hive_accounts ORDER BY cached_at LIMIT :limit"
        return cls.dirty(query_col(sql, limit=limit))

    @classmethod
    def flush(cls, trx=False, period=1):
        assert period >= 1
        if not cls._dirty:
            return 0
        count = len(cls._dirty)
        if period > 1:
            count = math.ceil(count / period)
        accounts = [cls._dirty.popleft() for _ in range(count)]
        if trx:
            print("[SYNC] update %d accounts" % count)
        cls._cache_accounts(accounts, trx=trx)
        return count

    @classmethod
    def update_ranks(cls):
        sql = """
        UPDATE hive_accounts
           SET rank = r.rnk
          FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY vote_weight DESC) as rnk FROM hive_accounts) r
         WHERE hive_accounts.id = r.id AND rank != r.rnk;
        """
        query(sql)

    @classmethod
    def _cache_accounts(cls, accounts, trx=True):
        timer = Timer(len(accounts), 'account', ['rps', 'wps'])
        for batch in partition_all(1000, accounts):

            timer.batch_start()
            sqls = cls._generate_cache_sqls(batch)
            timer.batch_lap()
            cls._batch_update(sqls, trx)

            timer.batch_finish(len(batch))
            if trx or len(accounts) > 1000:
                print(timer.batch_status())

    @classmethod
    def _batch_update(cls, sqls, trx):
        if trx:
            query("START TRANSACTION")
        for (sql, params) in sqls:
            query(sql, **params)
        if trx:
            query("COMMIT")

    @classmethod
    def _generate_cache_sqls(cls, accounts):
        cached_at = datetime.now().strftime('%Y-%m-%dT%H:%M:%S')
        sqls = []
        for account in get_adapter().get_accounts(accounts):
            vote_weight = (amount(account['vesting_shares'])
                           + amount(account['received_vesting_shares'])
                           - amount(account['delegated_vesting_shares']))

            # remove empty keys
            useless = ['transfer_history', 'market_history', 'post_history',
                       'vote_history', 'other_history', 'tags_usage',
                       'guest_bloggers']
            for key in useless:
                del account[key]

            # pull out valid profile md and delete the key
            profile = cls._safe_account_metadata(account)
            del account['json_metadata']

            values = {
                'name': account['name'],
                'proxy': account['proxy'],
                'post_count': account['post_count'],
                'reputation': rep_log10(account['reputation']),
                'proxy_weight': amount(account['vesting_shares']),
                'vote_weight': vote_weight,
                'kb_used': int(account['lifetime_bandwidth']) / 1e6 / 1024,
                'active_at': account['last_bandwidth_update'],
                'cached_at': cached_at,
                'display_name': profile['name'],
                'about': profile['about'],
                'location': profile['location'],
                'website': profile['website'],
                'profile_image': profile['profile_image'],
                'cover_image': profile['cover_image'],
                'raw_json': json.dumps(account)
            }

            update = ', '.join([k+" = :"+k for k in list(values.keys())][1:])
            sql = "UPDATE hive_accounts SET %s WHERE name = :name" % (update)
            sqls.append((sql, values))
        return sqls

    @classmethod
    def _safe_account_metadata(cls, account):
        prof = {}
        try:
            prof = json.loads(account['json_metadata'])['profile']
            if not isinstance(prof, dict):
                prof = {}
        except Exception:
            pass

        name = str(prof['name']) if 'name' in prof else None
        about = str(prof['about']) if 'about' in prof else None
        location = str(prof['location']) if 'location' in prof else None
        website = str(prof['website']) if 'website' in prof else None
        profile_image = str(prof['profile_image']) if 'profile_image' in prof else None
        cover_image = str(prof['cover_image']) if 'cover_image' in prof else None

        name = cls._char_police(name)
        about = cls._char_police(about)
        location = cls._char_police(location)

        name = trunc(name, 20)
        about = trunc(about, 160)
        location = trunc(location, 30)

        if name and name[0:1] == '@':
            name = None
        if website and len(website) > 100:
            website = None
        if website and not re.match('^https?://', website):
            website = 'http://' + website

        if profile_image and not re.match('^https?://', profile_image):
            profile_image = None
        if cover_image and not re.match('^https?://', cover_image):
            cover_image = None
        if profile_image and len(profile_image) > 1024:
            profile_image = None
        if cover_image and len(cover_image) > 1024:
            cover_image = None

        return dict(
            name=name or '',
            about=about or '',
            location=location or '',
            website=website or '',
            profile_image=profile_image or '',
            cover_image=cover_image or '',
        )

    @classmethod
    def _char_police(cls, string):
        if not string:
            return None
        if string.find('\x00') > -1:
            print("bad string: {}".format(string))
            return None
        return string


if __name__ == '__main__':
    Accounts.update_ranks()
    #Accounts.dirty_all()
    #Accounts.flush()
