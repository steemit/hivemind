import time
import json
import re

from hive.db.methods import query_one, query_col, query, query_row, query_all
from hive.indexer.steem_client import get_adapter
from hive.indexer.normalize import rep_log10, amount, trunc

class Accounts:
    _ids = {}
    _dirty = set()
    _dirty_follows = set()

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
        return (name in cls._ids)

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
    def dirty(cls, account):
        cls._dirty.add(account)

    @classmethod
    def dirty_follows(cls, account):
        cls._dirty_follows.add(account)

    @classmethod
    def cache_all(cls):
        cls.cache_accounts(query_col("SELECT name FROM hive_accounts"))

    @classmethod
    def cache_old(cls):
        print("Caching old accounts...")
        cls.cache_accounts(query_col("SELECT name FROM hive_accounts WHERE cached_at < NOW() - INTERVAL 12 HOUR"))

    @classmethod
    def cache_dirty(cls):
        cls.cache_accounts(list(cls._dirty))
        cls._dirty = set()

    @classmethod
    def cache_dirty_follows(cls):
        if not cls._dirty_follows:
            return
        cls.update_follows(list(cls._dirty_follows))
        cls._dirty_follows = set()

    @classmethod
    def cache_accounts(cls, accounts):
        from hive.indexer.cache import batch_queries

        processed = 0
        total = len(accounts)

        for i in range(0, total, 1000):
            batch = accounts[i:i+1000]

            lap_0 = time.perf_counter()
            sqls = cls._generate_cache_sqls(batch)
            lap_1 = time.perf_counter()
            batch_queries(sqls)
            lap_2 = time.perf_counter()

            if len(batch) < 1000:
                continue

            processed += len(batch)
            rem = total - processed
            rate = len(batch) / (lap_2 - lap_0)
            pct_db = int(100 * (lap_2 - lap_1) / (lap_2 - lap_0))
            print(" -- account {} of {} ({}/s, {}% db) -- {}m remaining".format(
                processed, total, round(rate, 1), pct_db, round(rem / rate / 60, 2)))

    @classmethod
    def update_ranks(cls):
        sql = """
        UPDATE hive_accounts JOIN (
            SELECT id, @rownum:=@rownum+1 rank
            FROM hive_accounts, (SELECT @rownum:=0) r
            ORDER BY vote_weight DESC
        ) ranks USING(id) SET hive_accounts.rank = ranks.rank
        """
        query(sql)
        return

        # the following method is 10-20x slower
        id_weight = query_all("SELECT id, vote_weight FROM hive_accounts")
        id_weight = sorted(id_weight, key=lambda el: el[1], reverse=True)

        print("Updating account ranks...")
        lap_0 = time.perf_counter()
        query("START TRANSACTION")
        for (i, (_id, _)) in enumerate(id_weight):
            query("UPDATE hive_accounts SET rank=%d WHERE id=%d" % (i+1, _id))
        query("COMMIT")
        lap_1 = time.perf_counter()
        print("Updated %d ranks in %ds" % (len(id_weight), lap_1 - lap_0))

    @classmethod
    def update_follows(cls, accounts):
        ids = map(cls.get_id, accounts)
        sql = """
            UPDATE hive_accounts
               SET followers = (SELECT COUNT(*) FROM hive_follows WHERE state = 1 AND following = hive_accounts.id),
                   following = (SELECT COUNT(*) FROM hive_follows WHERE state = 1 AND follower  = hive_accounts.id)
             WHERE id IN :ids
        """
        query(sql, ids=tuple(ids))

    @classmethod
    def _generate_cache_sqls(cls, accounts, block_date=None):
        if not block_date:
            block_date = get_adapter().head_time()

        sqls = []
        for account in get_adapter().get_accounts(accounts):
            values = {
                'name': account['name'],
                'proxy': account['proxy'],
                'post_count': account['post_count'],
                'reputation': rep_log10(account['reputation']),
                'proxy_weight': amount(account['vesting_shares']),
                'vote_weight': amount(account['vesting_shares']) + amount(account['received_vesting_shares']) - amount(account['delegated_vesting_shares']),
                'kb_used': int(account['lifetime_bandwidth']) / 1e6 / 1024,
                'active_at': account['last_bandwidth_update'],
                'cached_at': block_date,
                **cls._safe_account_metadata(account)
            }

            update = ', '.join([k+" = :"+k for k in values.keys()][1:])
            sql = "UPDATE hive_accounts SET %s WHERE name = :name" % (update)
            sqls.append([(sql, values)])
        return sqls

    @classmethod
    def _safe_account_metadata(cls, account):
        prof = {}
        try:
            prof = json.loads(account['json_metadata'])['profile']
            if not isinstance(prof, dict):
                prof = {}
        except:
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
        if website and website[0:4] != 'http':
            website = 'http://' + website
        # TODO: regex validate `website`

        if profile_image and not re.match('^https?://', profile_image):
            profile_image = None
        if cover_image and not re.match('^https?://', cover_image):
            cover_image = None
        if profile_image and len(profile_image) > 1024:
            profile_image = None
        if cover_image and len(cover_image) > 1024:
            cover_image = None

        return dict(
            display_name=name or '',
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
    print(Accounts._generate_cache_sqls(['roadscape', 'ned', 'sneak', 'test-safari']))
    #Accounts.cache_all()
