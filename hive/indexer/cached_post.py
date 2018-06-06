"""Manages cached post data."""

import json
import collections
import math

from toolz import partition_all
from hive.db.adapter import Db
from hive.steem.steem_client import SteemClient

from hive.utils.post import post_basic, post_legacy, post_payout, post_stats
from hive.utils.timer import Timer
from hive.indexer.accounts import Accounts

DB = Db.instance()

# levels of post dirtiness, in order of decreasing priority
LEVELS = ['insert', 'payout', 'update', 'upvote']

def _keyify(items):
    return dict(map(lambda x: ("val_%d" % x[0], x[1]), enumerate(items)))

class CachedPost:
    """Maintain update queue and writing to `hive_posts_cache`."""

    # cursor signifying upper bound of cached post span
    _last_id = -1

    # cached id map
    _ids = {}

    # urls which are missing from id map
    _noids = set()

    # dirty posts; {key: dirty_level}
    _queue = collections.OrderedDict()

    # new promoted values, pending write
    _pending_promoted = {}

    @classmethod
    def update_promoted_amount(cls, post_id, amount):
        """Set a new pending amount for a post for its next update."""
        cls._pending_promoted[post_id] = amount

    @classmethod
    def _dirty(cls, level, author, permlink, pid=None):
        """Mark a post as dirty."""
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
        """Given a post url, get its id."""
        if url in cls._ids:
            return cls._ids[url]
        raise Exception("requested id for %s not in map" % url)

    @classmethod
    def vote(cls, author, permlink, pid=None):
        """Handle a post dirtied by a `vote` op."""
        cls._dirty('upvote', author, permlink, pid)

    @classmethod
    def insert(cls, author, permlink, pid):
        """Handle a post created by a `comment` op."""
        cls._dirty('insert', author, permlink, pid)

    @classmethod
    def update(cls, author, permlink, pid):
        """Handle a post updated by a `comment` op."""
        cls._dirty('update', author, permlink, pid)

    @classmethod
    def delete(cls, post_id, author, permlink):
        """Handle a post deleted by a `delete_comment` op.

        With steemd, posts can be 'deleted' or unallocated in certain
        conditions. It requires foregoing convenient assumptions, e.g.:

         - author/permlink is unique and always references the same post
         - you can always get_content on any author/permlink you see in an op
        """
        DB.query("DELETE FROM hive_posts_cache WHERE post_id = :id", id=post_id)

        # if it was queued for a write, remove it
        url = author+'/'+permlink
        if url in cls._queue:
            del cls._queue[url]

    @classmethod
    def undelete(cls, post_id, author, permlink):
        """Handle a post 'undeleted' by a `comment` op.

        'Undeletion' occurs when hive detects that a previously deleted
        author/permlink combination has been reused on a new post. Hive
        does not delete hive_posts entries because they are currently
        irreplaceable in case of a fork. Instead, we reuse the slot.
        It's important to immediately insert a placeholder in the cache
        table since hive only scans forward. This row's properties push
        it to the front of update-immediately queue.

        Alternate ways of handling undeletes:

         - delete row from hive_posts so that it can be re-indexed (re-id'd)
            - comes at a risk of losing expensive entry on fork (and no undo)
         - create undo table for hive_posts, hive_follows, etc, & link to block
         - rely on steemd's post.id instead of database autoincrement
           - requires way to query steemd post objects by id to be useful
             - batch get_content_by_ids in steemd would be /huge/ speedup
         - create a consistent cache queue table or dirty flag col
        """
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

    @classmethod
    def flush(cls, trx=False, spread=1, full_total=None):
        """Process all posts which have been marked as dirty."""
        cls._load_noids() # load missing ids
        assert spread == 1, "not fully tested, use with caution"

        counts = {}
        tuples = []
        for level in LEVELS:
            tups = cls._get_tuples_for_level(level, spread)
            counts[level] = len(tups)
            tuples.extend(tups)

        if trx or len(tuples) > 250:
            changed = filter(lambda t: t[1], counts.items())
            summary = list(map(lambda group: "%d %ss" % group[::-1], changed))
            summary = ', '.join(summary) if summary else 'none'
            print("[PREP] posts cache process: %s" % summary)

        cls._update_batch(tuples, trx, full_total=full_total)
        for url, _, _ in tuples:
            del cls._queue[url]

        # TODO: ideal place to update reps of authors whos posts were modified.
        # potentially could be triggered in vote(). remove the Accounts.dirty
        # from hive.indexer.blocks which follows CachedPost.vote.

        return counts

    @classmethod
    def _get_tuples_for_level(cls, level, fraction=1):
        """Query tuples to be updated.

        Given a specific flush level (insert, payout, update, upvote),
        returns a list of tuples to be passed to _update_batch, in the
        form of: `[(url, id, level)*]`
        """
        mode = LEVELS.index(level)
        urls = [url for url, i in cls._queue.items() if i == mode]
        if fraction > 1 and level != 'insert': # inserts must be full flush
            urls = urls[0:math.ceil(len(urls) / fraction)]
        return [(url, cls._get_id(url), level) for url in urls]

    @classmethod
    def _load_noids(cls):
        """Load ids for posts we don't know the ids of.

        When posts are marked dirty, specifying the id is optional
        because a successive call might be able to provide it "for
        free". Before flushing changes this method should be called
        to fill in any gaps.
        """
        from hive.indexer.posts import Posts
        noids = cls._noids - set(cls._ids.keys())
        tuples = [(Posts.get_id(*url.split('/')), url) for url in noids]
        for pid, url in tuples:
            assert pid, "WARNING: missing id for %s" % url
            cls._ids[url] = pid
        cls._noids = set()
        return len(tuples)

    @classmethod
    def _select_paidout_tuples(cls, date):
        """Query hive_posts_cache for payout sweep.

        Select all posts which should have been paid out before `date`
        yet do not have the `is_paidout` flag set. We perform this
        sweep to ensure that we always have accurate final payout
        state. Since payout values vary even between votes, we'd have
        stale data if we didn't sweep, and only waited for incoming
        votes before an update.
        """
        from hive.indexer.posts import Posts

        sql = """SELECT post_id FROM hive_posts_cache
                  WHERE is_paidout = '0' AND payout_at <= :date"""
        ids = DB.query_col(sql, date=date)
        if not ids:
            return []

        sql = """SELECT id, author, permlink
                 FROM hive_posts WHERE id IN :ids"""
        results = DB.query_all(sql, ids=tuple(ids))
        return Posts.save_ids_from_tuples(results)

    @classmethod
    def dirty_paidouts(cls, date):
        """Mark dirty all paidout posts not yet updated in db."""
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
    def _select_missing_tuples(cls, last_cached_id, limit=1000000):
        """Fetch posts inserted into main posts table but not cache."""
        from hive.indexer.posts import Posts
        sql = """SELECT id, author, permlink, promoted FROM hive_posts
                  WHERE is_deleted = '0' AND id > :id
               ORDER BY id LIMIT :limit"""
        results = DB.query_all(sql, id=last_cached_id, limit=limit)
        return Posts.save_ids_from_tuples(results)

    @classmethod
    def dirty_missing(cls, limit=250000):
        """Mark dirty all hive_posts records not yet written to cache."""
        from hive.indexer.posts import Posts

        # cached posts inserted sequentially, so compare MAX(id)'s
        last_cached_id = cls.last_id()
        last_post_id = Posts.last_id()
        gap = last_post_id - last_cached_id

        if gap:
            missing = cls._select_missing_tuples(last_cached_id, limit)
            for pid, author, permlink, promoted in missing:
                if promoted > 0: # ensure we don't miss promote amount
                    cls.update_promoted_amount(pid, promoted)
                cls._dirty('insert', author, permlink, pid)

        return gap

    @classmethod
    def recover_missing_posts(cls):
        """Startup routine that cycles through missing posts.

        This is used for (1) initial sync, and (2) recovering missing
        cache records upon launch if hive fast-sync was interrupted.
        """
        gap = cls.dirty_missing()
        print("[INIT] {} missing post cache entries".format(gap))
        while cls.flush(trx=True, full_total=gap)['insert']:
            gap = cls.dirty_missing()

    @classmethod
    def _update_batch(cls, tuples, trx=True, full_total=None):
        """Fetch, process, and write a batch of posts.

        Given a set of posts, fetch from steemd and write them to the
        db. The `tuples` arg is the form of `[(url, id, level)*]`
        representing posts which are to be fetched from steemd and
        updated in cache.

        Regarding _bump_last_id: there's a rare edge case when the last
        hive_post entry has been deleted "in the future" (ie, we haven't
        seen the delete op yet). So even when the post is not found
        (i.e. `not post['author']`), it's important to advance _last_id,
        because this cursor is used to deduce any missing cache entries.
        """

        steemd = SteemClient.instance()
        timer = Timer(total=len(tuples), entity='post',
                      laps=['rps', 'wps'], full_total=full_total)
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
        """Retrieve the latest post_id that was cached."""
        if cls._last_id == -1:
            # after initial query, we maintain last_id w/ _bump_last_id()
            sql = "SELECT COALESCE(MAX(post_id), 0) FROM hive_posts_cache"
            cls._last_id = DB.query_one(sql)
        return cls._last_id

    @classmethod
    def _bump_last_id(cls, next_id):
        """Update our last_id based on a recent insert."""
        last_id = cls.last_id()
        if next_id <= last_id:
            return

        if next_id - last_id > 2:
            cls._ensure_safe_gap(last_id, next_id)
            if next_id - last_id > 4:
                # gap of 2 is common due to deletions. report on larger gaps.
                print("[WARN] skip post ids: %d -> %d" % (last_id, next_id))

        cls._last_id = next_id

    @classmethod
    def _ensure_safe_gap(cls, last_id, next_id):
        """Paranoid check of important operating assumption."""
        sql = """
            SELECT COUNT(*) FROM hive_posts
            WHERE id BETWEEN :x1 AND :x2 AND is_deleted = '0'
        """
        missing_posts = DB.query_one(sql, x1=(last_id + 1), x2=(next_id - 1))
        if not missing_posts:
            return
        raise Exception("found large cache gap: %d --> %d (%d)"
                        % (last_id, next_id, missing_posts))

    @classmethod
    def _batch_queries(cls, batches, trx):
        """Process batches of prepared SQL tuples."""
        if trx:
            DB.query("START TRANSACTION")
        for queries in batches:
            for (sql, params) in queries:
                DB.query(sql, **params)
        if trx:
            DB.query("COMMIT")

    @classmethod
    def _sql(cls, pid, post, level=None):
        """Given a post and "update level", generate SQL edit statement.

        Valid levels are:
         - `insert`: post does not yet exist in cache
         - `update`: post was modified
         - `payout`: post was paidout
         - `upvote`: post payout/votes changed
        """

        #pylint: disable=bad-whitespace
        assert post['author'], "post {} is blank".format(pid)

        # last-minute sanity check to ensure `pid` is correct #78
        pid2 = cls._get_id(post['author']+'/'+post['permlink'])
        assert pid == pid2, "hpc id %d maps to %d" % (pid, pid2)

        # inserts always sequential. if pid > last_id, this operation
        # *must* be an insert; so `level` must not be any form of update.
        if pid > cls.last_id() and level != 'insert':
            raise Exception("WARNING: new pid, but level=%s. #%d vs %d, %s"
                            % (level, pid, cls.last_id(), repr(post)))

        # start building the queries
        tag_sqls = []
        values = [('post_id', pid)]

        # immutable; write only once (*edge case: undeleted posts)
        if level == 'insert':
            values.extend([
                ('author',   post['author']),
                ('permlink', post['permlink']),
                ('category', post['category']),
                ('depth',    post['depth'])])

        # always write, unless simple vote update
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

        # update tags if action is insert/update and is root post
        if level in ['insert', 'update'] and not post['depth']:
            diff = level != 'insert' # do not attempt tag diff on insert
            tag_sqls.extend(cls._tag_sqls(pid, basic['tags'], diff=diff))

        # if there's a pending promoted value to write, pull it out
        if pid in cls._pending_promoted:
            bal = cls._pending_promoted[pid]
            values.append(('promoted', bal))
            del cls._pending_promoted[pid]

        # update unconditionally
        payout = post_payout(post)
        stats = post_stats(post)
        values.extend([
            ('payout',      "%f" % payout['payout']),
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
    def _tag_sqls(cls, pid, tags, diff=True):
        """Generate SQL "deltas" for a post_id's associated tags."""
        curr_tags = set()
        if diff:
            sql = "SELECT tag FROM hive_post_tags WHERE post_id = :id"
            curr_tags = set(DB.query_col(sql, id=pid))

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
        """Given row `values`, write to our table."""
        tup = cls._write_sql(values, mode)
        return DB.query(tup[0], **tup[1])

    @classmethod
    def _write_sql(cls, values, mode='insert'):
        """SQL builder for writing to hive_posts_cache table."""
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
