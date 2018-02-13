import json
import collections
import math

from toolz import partition_all
from hive.db.methods import query, query_all, query_col, query_one

from hive.utils.post import post_basic, post_legacy, post_payout, post_stats
from hive.utils.timer import Timer
from hive.indexer.accounts import Accounts
from hive.indexer.steem_client import SteemClient

# levels of post dirtiness, in order of decreasing priority
LEVELS = ['insert', 'payout', 'update', 'upvote']

def _keyify(items):
    return dict(map(lambda x: ("val_%d" % x[0], x[1]), enumerate(items)))

class CachedPost:

    # cursor signifying upper bound of cached post span
    _last_id = -1

    # cached id map
    _ids = {}

    # urls which are missing from id map
    _noids = set()

    # dirty posts; {key: dirty_level}
    _queue = collections.OrderedDict()

    # Mark a post as dirty.
    @classmethod
    def _dirty(cls, level, author, permlink, pid=None):
        assert level in LEVELS, "invalid level {}".format(level)
        mode = LEVELS.index(level)
        url = author + '/' + permlink

        # add to appropriate queue.
        if url not in cls._queue:
            cls._queue[url] = mode
        # upgrade priority if needed
        elif cls._queue[url] > mode:
            cls._queue[url] = mode

        # add to id map, or register missing
        if pid and url in cls._ids:
            assert pid == cls._ids[url], "pid map conflict #78"
        elif pid:
            cls._ids[url] = pid
        else:
            cls._noids.add(url)

    @classmethod
    def _get_id(cls, url):
        if url in cls._ids:
            return cls._ids[url]
        raise Exception("requested id for %s not in map" % url)

    # Called when a post is voted on.
    @classmethod
    def vote(cls, author, permlink):
        cls._dirty('upvote', author, permlink)

    # Called when a post record is created.
    @classmethod
    def insert(cls, author, permlink, pid):
        cls._dirty('insert', author, permlink, pid)

    # Called when a post's content is edited.
    @classmethod
    def update(cls, author, permlink, pid):
        cls._dirty('update', author, permlink, pid)

    # In steemd, posts can be 'deleted' or unallocated in certain conditions.
    # This requires foregoing some convenient assumptions, such as:
    #   - author/permlink is unique and always references the same post
    #   - you can always get_content on any author/permlink you see in an op
    @classmethod
    def delete(cls, post_id, author, permlink):
        query("DELETE FROM hive_posts_cache WHERE post_id = :id", id=post_id)

        # if it was queued for a write, remove it
        url = author+'/'+permlink
        if url in cls._queue:
            del cls._queue[url]

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
        # do not force-write unless cache spans this id.
        if post_id > cls.last_id():
            cls.insert(author, permlink, post_id)
            return

        # force-create dummy row to ensure cache is aware. only needed when
        # cache already spans this id, in case in-mem buffer is lost. default
        # value for payout_at ensures that it will get picked up for update.
        cls._write({
            'post_id': post_id,
            'author': author,
            'permlink': permlink},
                   mode='insert')
        cls.update(author, permlink, post_id)

    # Process all posts which have been marked as dirty.
    @classmethod
    def flush(cls, trx=False, period=1):
        cls._load_noids() # load missing ids
        assert period == 1, "period not tested"

        counts = {}
        tuples = []
        for level in LEVELS:
            tups = cls._get_tuples_for_level(level, period)
            counts[level] = len(tups)
            tuples.extend(tups)

        if trx or len(tuples) > 250:
            changed = filter(lambda t: t[1], counts.items())
            summary = list(map(lambda group: "%d %ss" % group[::-1], changed))
            summary = ', '.join(summary) if summary else 'none'
            print("[PREP] posts cache process: %s" % summary)

        cls._update_batch(tuples, trx)
        for url, _, _ in tuples:
            del cls._queue[url]

        # TODO: ideal place to update reps of authors whos posts were modified.
        # potentially could be triggered in vote(). remove the Accounts.dirty
        # from hive.indexer.blocks which follows CachedPost.vote.

        return counts

    # Given a specific flush level (insert, payout, update, upvote),
    # return a list of tuples to be passed to _update_batch, in the form
    # of: [(url, id, level)*]
    @classmethod
    def _get_tuples_for_level(cls, level, fraction=1):
        mode = LEVELS.index(level)
        urls = [url for url, i in cls._queue.items() if i == mode]
        if fraction > 1 and level != 'insert': # inserts must be full flush
            urls = urls[0:math.ceil(len(urls) / fraction)]
        return [(url, cls._get_id(url), level) for url in urls]

    # When posts are marked dirty, specifying the id is optional because
    # a successive call might be able to provide it "for free". Before
    # flushing changes this method should be called to fill in any gaps.
    @classmethod
    def _load_noids(cls):
        from hive.indexer.posts import Posts
        noids = cls._noids - set(cls._ids.keys())
        tuples = [(Posts.get_id(*url.split('/')), url) for url in noids]
        for pid, url in tuples:
            assert pid, "WARNING: missing id for %s" % url
            cls._ids[url] = pid
        cls._noids = set()
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
            cls._dirty('payout', author, permlink, pid)
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
    def dirty_missing(cls, limit=1_000_000):
        from hive.indexer.posts import Posts

        # cached posts inserted sequentially, so compare MAX(id)'s
        last_cached_id = cls.last_id()
        last_post_id = Posts.last_id()
        gap = last_post_id - last_cached_id

        if gap:
            missing = cls._select_missing_tuples(last_cached_id, limit)
            for pid, author, permlink in missing:
                cls._dirty('insert', author, permlink, pid)

        return gap

    @classmethod
    def recover_missing_posts(cls):
        gap = cls.dirty_missing()
        print("[INIT] {} missing post cache entries".format(gap))
        while cls.flush(trx=True)['insert']:
            cls.dirty_missing()

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
    def _update_batch(cls, tuples, trx=True):
        steemd = SteemClient.instance()
        timer = Timer(total=len(tuples), entity='post', laps=['rps', 'wps'])
        tuples = sorted(tuples, key=lambda x: x[1]) # enforce ASC id's

        for tups in partition_all(1000, tuples):
            timer.batch_start()
            buffer = []

            post_args = [tup[0].split('/') for tup in tups]
            posts = steemd.get_content_batch(post_args)
            post_ids = [tup[1] for tup in tups]
            post_levels = [tup[2] for tup in tups]
            for pid, post, level in zip(post_ids, posts, post_levels):
                if post['author']:
                    buffer.append(cls._sql(pid, post, level=level))
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
    def _sql(cls, pid, post, level=None):
        #pylint: disable=bad-whitespace
        assert post['author'], "post {} is blank".format(pid)

        # last-minute sanity check to ensure `pid` is correct #78
        pid2 = cls._get_id(post['author']+'/'+post['permlink'])
        assert pid == pid2, "hpc id %d maps to %d" % (pid, pid2)

        # inserts always sequential. if pid > last_id, this operation
        # *must* be an insert; so `level` must not be ay form of update.
        if pid > cls.last_id() and level != 'insert':
            raise Exception("WARNING: new pid, but level=%s. #%d vs %d, %s"
                            % (level, pid, cls.last_id(), repr(post)))

        # start building the queries
        tag_sqls = []
        values = [('post_id', pid)]

        # immutable; write only once
        if level == 'insert':
            values.extend([
                ('author',   post['author']),
                ('permlink', post['permlink']),
                ('category', post['category']),
                ('depth',    post['depth'])])

        # always write, unless simple payout update
        if level in ['insert', 'payout', 'update']:
            basic = post_basic(post)
            values.extend([
                ('created_at',    post['created']),    # immutable*
                ('updated_at',    post['last_update']),
                ('title',         post['title']),
                ('payout_at',     basic['payout_at']), # immutable*
                ('preview',       basic['preview']),
                ('body',          basic['body']),
                ('img_url',       basic['image']),
                ('is_nsfw',       basic['is_nsfw']),
                ('is_declined',   basic['is_payout_declined']),
                ('is_full_power', basic['is_full_power']),
                ('is_paidout',    basic['is_paidout']),
                ('json',          json.dumps(basic['json_metadata'])),
                ('raw_json',      json.dumps(post_legacy(post))),
                ('children',      min(post['children'], 32767))])

            # update tags if root post
            if not post['depth']:
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
        mode = 'insert' if level == 'insert' else 'update'
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
            params = _keyify(to_add)
            vals = ["(:id, :%s)" % key for key in params.keys()]
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
