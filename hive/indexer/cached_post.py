import json
import collections

from toolz import partition_all
from hive.db.methods import query, query_all, query_col, query_one
from hive.db.db_state import DbState

from hive.utils.post import post_basic, post_legacy, post_payout, post_stats
from hive.utils.timer import Timer
from hive.indexer.accounts import Accounts
from hive.indexer.steem_client import get_adapter

class CachedPost:

    # cursor signifying upper bound of cached post span
    _last_id = -1

    # post entries to update (full)
    _dirty = collections.OrderedDict()

    # post entries to update (light)
    _voted = collections.OrderedDict()

    # Called when a post is voted on.
    # TODO: only update relevant payout fields for this post. #16
    @classmethod
    def vote(cls, author, permlink):
        cls._dirty_full(author, permlink)

    # Called when a post record is created.
    @classmethod
    def insert(cls, author, permlink, pid):
        cls._dirty_full(author, permlink, pid)

    # Called when a post's content is edited.
    @classmethod
    def update(cls, author, permlink, pid):
        cls._dirty_full(author, permlink, pid)

    # In steemd, posts can be 'deleted' or unallocated in certain conditions.
    # This requires foregoing some convenient assumptions, such as:
    #   - author/permlink is unique and always references the same post
    #   - you can always get_content on any author/permlink you see in an op
    @classmethod
    def delete(cls, post_id, author, permlink):
        query("DELETE FROM hive_posts_cache WHERE post_id = :id", id=post_id)

        # if it was queued for a write, remove it
        url = author+'/'+permlink
        if url in cls._dirty:
            del cls._dirty[url]

    # 'Undeletion' event occurs when hive detects that a previously deleted
    #   author/permlink combination has been reused on a new post. Hive does
    #   not delete hive_posts entries because they are currently irreplaceable
    #   in case of a fork. Instead, we reuse the slot. It's important to
    #   immediately insert a placeholder in the cache table, because hive only
    #   scans forward. Here we create a dummy record whose properties push it
    #   to the front of update-immediately queue.
    #
    # Alternate ways of handling undeletes:
    #  - delete row from hive_posts so that it can be re-indexed (re-id'd)
    #    - comes at a risk of losing expensive entry on fork (and no undo)
    #  - create undo table for hive_posts, hive_follows, etc, & link to block
    #  - rely on steemd's post.id instead of database autoincrement
    #    - requires way to query steemd post objects by id to be useful
    #      - batch get_content_by_ids in steemd would be /huge/ speedup
    #  - create a consistent cache queue table or dirty flag col
    @classmethod
    def undelete(cls, post_id, author, permlink):
        # ignore unless cache spans this id. forward sweep will pick it up.
        if post_id > cls.last_id():
            return

        # create dummy row to ensure cache is aware
        cls._write({
            'post_id': post_id,
            'author': author,
            'permlink': permlink},
                   mode='insert')

    @classmethod
    def _dirty_full(cls, author, permlink, pid=None):
        url = author + '/' + permlink
        if url in cls._dirty:
            if pid:
                if not cls._dirty[url]:
                    cls._dirty[url] = pid
                else:
                    assert pid == cls._dirty[url], "pid map conflict" #78
        else:
            cls._dirty[url] = pid

    @classmethod
    def _dirty_vote(cls, author, permlink, pid=None):
        url = author + '/' + permlink
        if url in cls._voted:
            if pid and not cls._voted[url]:
                cls._voted[url] = pid
        else:
            cls._voted[url] = pid

    # Process all posts which have been marked as dirty.
    @classmethod
    def flush(cls, trx=False):
        cls._load_dirty_noids() # load missing ids
        tuples = cls._dirty.items()
        last_id = cls.last_id()

        inserts = [(url, pid) for url, pid in tuples if pid > last_id]
        updates = [(url, pid) for url, pid in tuples if pid <= last_id]

        if trx or len(tuples) > 1000:
            print("[PREP] cache %d posts (%d new, %d edits)"
                  % (len(tuples), len(inserts), len(updates)))

        batch = inserts + updates
        cls._update_batch(batch, trx)

        for url, _ in batch:
            del cls._dirty[url]
            if url in cls._voted:
                del cls._voted[url]

        #votes = [(url, pid) for url, pid in cls._voted.items() if pid <= cls.last_id()]
        #cls._update_batch(votes, trx, only_payout=True)
        #for url, _ in votes:
        #    del cls._voted[url]

        #return (len(inserts), len(updates), len(votes))
        return len(batch)

    # When posts are marked dirty, specifying the id is optional because
    # a successive call might be able to provide it "for free". Before
    # flushing changes this method should be called to fill in any gaps.
    @classmethod
    def _load_dirty_noids(cls):
        from hive.indexer.posts import Posts
        noids = [k for k, v in cls._dirty.items() if not v]
        tuples = [(Posts.get_id(*url.split('/')), url) for url in noids]
        for pid, url in tuples:
            if pid:
                cls._dirty[url] = pid
            else:
                print("WARNING: missing id for %s" % url)
                del cls._dirty[url] # extremely rare but important. add assert?

        return len(tuples)

    # Select all posts which should have been paid out before `date` yet do not
    # have the `is_paidout` flag set. We perform this sweep to ensure that we
    # always have accurate final payout state. Since payout values vary even
    # between votes, we'd have stale data if we didn't sweep, and only waited
    # for incoming votes before an update.
    @classmethod
    def _select_paidout_tuples(cls, date):
        from hive.indexer.posts import Posts

        sql = """SELECT post_id FROM hive_posts_cache
                  WHERE is_paidout = '0' AND payout_at <= :date"""
        ids = query_col(sql, date=date)
        if not ids:
            return []

        sql = """SELECT id, author, permlink
                 FROM hive_posts WHERE id IN :ids"""
        results = query_all(sql, ids=tuple(ids))
        return Posts.save_ids_from_tuples(results)

    @classmethod
    def dirty_paidouts(cls, date):
        paidout = cls._select_paidout_tuples(date)
        authors = set()
        for (pid, author, permlink) in paidout:
            authors.add(author)
            cls._dirty_full(author, permlink, pid)
        Accounts.dirty(authors) # force-update accounts on payout

        if len(paidout) > 200:
            print("[PREP] Found {} payouts for {} authors since {}".format(
                len(paidout), len(authors), date))
        return len(paidout)

    @classmethod
    def _select_missing_tuples(cls, last_cached_id, limit=1_000_000):
        from hive.indexer.posts import Posts
        sql = """SELECT id, author, permlink FROM hive_posts
                  WHERE is_deleted = '0' AND id > :id
               ORDER BY id LIMIT :limit"""
        results = query_all(sql, id=last_cached_id, limit=limit)
        return Posts.save_ids_from_tuples(results)

    @classmethod
    # TODO: with cached_post.insert, we may not need to call this every block anymore
    def dirty_missing(cls, limit=1_000_000):
        from hive.indexer.posts import Posts

        # cached posts inserted sequentially, so compare MAX(id)'s
        last_cached_id = cls.last_id()
        last_post_id = Posts.last_id()
        gap = last_post_id - last_cached_id

        if gap:
            missing = cls._select_missing_tuples(last_cached_id, limit)
            for pid, author, permlink in missing:
                # temporary sanity check -- if we're loading ids ON insert,
                # they should always be available at this point during listen.
                if DbState.is_listen_mode():
                    url = author+'/'+permlink
                    if url not in cls._dirty:
                        print("url not registered at all: %s" % url)
                    elif not cls._dirty[url]:
                        print("url registered but no id: %s" % url)
                cls._dirty_full(author, permlink, pid)

        return gap


    # Given a set of posts, fetch them from steemd and write them to the db.
    # The `tuples` arg is a list of (url, id) representing posts which are to be
    # fetched from steemd and updated in hive_posts_cache table.
    #
    # Regarding _bump_last_id: there's a rare edge case when the last hive_post
    # entry has been deleted "in the future" (ie, we haven't seen the delete op
    # yet). So even when the post is not found (i.e. `not post['author']`), it's
    # important to advance _last_id, because this cursor is used to deduce if
    # there's any missing cache entries.
    @classmethod
    def _update_batch(cls, tuples, trx=True, only_payout=False):
        from hive.indexer.posts import Posts
        steemd = get_adapter()
        timer = Timer(total=len(tuples), entity='post', laps=['rps', 'wps'])
        tuples = sorted(tuples, key=lambda x: x[1]) # enforce ASC id's

        for tups in partition_all(1000, tuples):
            timer.batch_start()
            buffer = []

            post_ids = [tup[1] for tup in tups]
            post_args = [tup[0].split('/') for tup in tups]
            posts = steemd.get_content_batch(post_args)
            for pid, post in zip(post_ids, posts):
                if post['author']:
                    # -- temp: paranoid enforcement for #78
                    pid2 = Posts.get_id(post['author'], post['permlink'])
                    assert pid == pid2, "hpc id %d maps to %d" % (pid, pid2)
                    # --
                    buffer.append(cls._sql(pid, post, only_payout=only_payout))
                else:
                    print("WARNING: ignoring deleted post {}".format(pid))
                cls._bump_last_id(pid)

            timer.batch_lap()
            cls._batch_queries(buffer, trx)

            timer.batch_finish(len(posts))
            if len(tuples) >= 1000:
                print(timer.batch_status())

    @classmethod
    def last_id(cls):
        if cls._last_id == -1:
            sql = "SELECT COALESCE(MAX(post_id), 0) FROM hive_posts_cache"
            cls._last_id = query_one(sql)
        return cls._last_id

    @classmethod
    def _bump_last_id(cls, next_id):
        last_id = cls.last_id()
        if next_id <= last_id:
            return

        if next_id - last_id > 2:
            cls._ensure_safe_gap(last_id, next_id)
            print("[WARN] skip post ids: %d -> %d" % (last_id, next_id))

        cls._last_id = next_id

    # paranoid check of important operating assumption
    @classmethod
    def _ensure_safe_gap(cls, last_id, next_id):
        sql = "SELECT COUNT(*) FROM hive_posts WHERE id BETWEEN :x1 AND :x2 AND is_deleted = '0'"
        missing_posts = query_one(sql, x1=(last_id + 1), x2=(next_id - 1))
        if not missing_posts:
            return
        raise Exception("found large cache gap: %d --> %d (%d)"
                        % (last_id, next_id, missing_posts))

    @classmethod
    def _batch_queries(cls, batches, trx):
        if trx:
            query("START TRANSACTION")
        for queries in batches:
            for (sql, params) in queries:
                query(sql, **params)
        if trx:
            query("COMMIT")

    @classmethod
    def _sql(cls, pid, post, only_payout=False):
        #pylint: disable=bad-whitespace
        assert post['author'], "post {} is blank".format(pid)

        # modes: (1) insert, (2) update, (3) update[only_payout]
        mode = 'insert' if pid > cls.last_id() else 'update'
        assert not (only_payout and mode == 'insert'), "invalid state"

        values = [('post_id', pid)]
        tag_sqls = []

        if not only_payout:
            basic = post_basic(post)
            if mode == 'insert':
                # immutable; write once.
                values.extend([
                    ('author',     "%s" % post['author']),
                    ('permlink',   "%s" % post['permlink']),
                    ('category',   "%s" % post['category']),
                    ('depth',      "%d" % post['depth']),
                    ('created_at', "%s" % post['created']),
                    ('payout_at',  "%s" % basic['payout_at'])])

            # editable post fields
            legacy = post_legacy(post)
            values.extend([
                ('updated_at',    "%s" % post['last_update']),
                ('title',         "%s" % post['title']),
                ('preview',       "%s" % basic['preview']),
                ('body',          "%s" % basic['body']),
                ('img_url',       "%s" % basic['image']),
                ('is_nsfw',       "%d" % basic['is_nsfw']),
                ('is_declined',   "%d" % basic['payout_declined']),
                ('is_full_power', "%d" % basic['full_power']),
                ('is_paidout',    "%d" % basic['is_paidout']),
                ('json',          "%s" % json.dumps(basic['json_metadata'])),
                ('raw_json',      "%s" % json.dumps(legacy)),
                ('children',      "%d" % min(post['children'], 32767))]) #FIXME

            # update tags for root posts
            if not post['parent_author']:
                tag_sqls.extend(cls._tag_sqls(pid, basic['tags']))

        # update unconditionally
        payout = post_payout(post)
        stats = post_stats(post)
        values.extend([
            ('payout',      "%f" % payout['payout']),
            ('promoted',    "%f" % payout['promoted']),
            ('rshares',     "%d" % payout['rshares']),
            ('votes',       "%s" % payout['csvotes']),
            ('sc_trend',    "%f" % payout['sc_trend']),
            ('sc_hot',      "%f" % payout['sc_hot']),
            ('flag_weight', "%f" % stats['flag_weight']),
            ('total_votes', "%d" % stats['total_votes']),
            ('up_votes',    "%d" % stats['up_votes']),
            ('is_hidden',   "%d" % stats['hide']),
            ('is_grayed',   "%d" % stats['gray']),
            ('author_rep',  "%f" % stats['author_rep'])])

        # build the post insert/update SQL, add tag SQLs
        return [cls._write_sql(values, mode)] + tag_sqls

    @classmethod
    def _tag_sqls(cls, pid, tags):
        sql = "SELECT tag FROM hive_post_tags WHERE post_id = :id"
        curr_tags = set(query_col(sql, id=pid))

        to_rem = (curr_tags - tags)
        if to_rem:
            sql = "DELETE FROM hive_post_tags WHERE post_id = :id AND tag IN :tags"
            yield (sql, dict(id=pid, tags=tuple(to_rem)))

        to_add = (tags - curr_tags)
        if to_add:
            params = {}
            vals = []
            for i, tag in enumerate(to_add):
                vals.append("(:id, :t%d)" % i)
                params["t%d"%i] = tag
            sql = "INSERT INTO hive_post_tags (post_id, tag) VALUES %s"
            sql += " ON CONFLICT DO NOTHING" # (conflicts due to collation)
            yield (sql % ','.join(vals), {'id': pid, **params})

    @classmethod
    def _write(cls, values, mode='insert'):
        tup = cls._write_sql(values, mode)
        return query(tup[0], **tup[1])

    # sql builder for writing to hive_posts_cache table
    @classmethod
    def _write_sql(cls, values, mode='insert'):
        _pk = ['post_id']
        _table = 'hive_posts_cache'
        assert _pk, "primary key not defined"
        assert _table, "table not defined"
        assert mode in ['insert', 'update'], "invalid mode %s" % mode

        values = collections.OrderedDict(values)
        fields = values.keys()

        if mode == 'insert':
            cols = ', '.join(fields)
            params = ', '.join([':'+k for k in fields])
            sql = "INSERT INTO %s (%s) VALUES (%s)"
            sql = sql % (_table, cols, params)
        elif mode == 'update':
            update = ', '.join([k+" = :"+k for k in fields if k not in _pk])
            where = ' AND '.join([k+" = :"+k for k in fields if k in _pk])
            sql = "UPDATE %s SET %s WHERE %s"
            sql = sql % (_table, update, where)

        return (sql, values)
