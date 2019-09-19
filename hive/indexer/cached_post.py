"""Manages cached post data."""

import math
import collections
import logging
import ujson as json

from toolz import partition_all
from hive.db.adapter import Db

from hive.utils.post import post_basic, post_legacy, post_payout, post_stats, mentions
from hive.utils.timer import Timer
from hive.indexer.accounts import Accounts
from hive.indexer.community import Community
from hive.indexer.notify import Notify

# pylint: disable=too-many-lines

log = logging.getLogger(__name__)

DB = Db.instance()

# levels of post dirtiness, in order of decreasing priority
LEVELS = ['insert', 'payout', 'update', 'upvote', 'recount']

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

    # pending vote notifs {pid: [voters]}
    _votes = {}

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
    def recount(cls, author, permlink, pid=None):
        """Force a child re-count."""
        cls._dirty('recount', author, permlink, pid)

    @classmethod
    def vote(cls, author, permlink, pid=None, voter=None):
        """Handle a post dirtied by a `vote` op."""
        cls._dirty('upvote', author, permlink, pid)
        Accounts.dirty(set([author])) # rep changed
        if voter:
            url = author + '/' + permlink
            if url not in cls._votes:
                cls._votes[url] = []
            cls._votes[url].append(voter)

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
        DB.query("DELETE FROM hive_post_tags   WHERE post_id = :id", id=post_id)

        # if it was queued for a write, remove it
        url = author+'/'+permlink
        log.warning("deleting %s", url)
        if url in cls._queue:
            del cls._queue[url]
            log.warning("deleted %s", url)
            if url in cls._ids:
                del cls._ids[url]

    @classmethod
    def undelete(cls, post_id, author, permlink, category):
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
        DB.query(cls._insert({
            'post_id': post_id,
            'author': author,
            'permlink': permlink,
            'category': category}))
        cls.update(author, permlink, post_id)
        log.warning("undeleted %s/%s", author, permlink)

    @classmethod
    def flush(cls, steem, trx=False, spread=1, full_total=None):
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
            log.info("[PREP] posts cache process: %s", summary)

        for url, _, _ in tuples:
            del cls._queue[url]

        cls._update_batch(steem, tuples, trx, full_total=full_total)

        for url, _, _ in tuples:
            if url not in cls._queue and url in cls._ids:
                del cls._ids[url]

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
            log.info("[PREP] Found %d payouts for %d authors since %s",
                     len(paidout), len(authors), date)
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
    def recover_missing_posts(cls, steem):
        """Startup routine that cycles through missing posts.

        This is used for (1) initial sync, and (2) recovering missing
        cache records upon launch if hive fast-sync was interrupted.
        """
        gap = cls.dirty_missing()
        log.info("[INIT] %d missing post cache entries", gap)
        while cls.flush(steem, trx=True, full_total=gap)['insert']:
            last_gap = gap
            gap = cls.dirty_missing()
            if gap == last_gap:
                log.warning('ignoring %d inserts -- may be deleted')
                break

    @classmethod
    def _update_batch(cls, steem, tuples, trx=True, full_total=None):
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
        # pylint: disable=too-many-locals

        timer = Timer(total=len(tuples), entity='post',
                      laps=['rps', 'wps'], full_total=full_total)
        tuples = sorted(tuples, key=lambda x: x[1]) # enforce ASC id's

        for tups in partition_all(1000, tuples):
            timer.batch_start()
            buffer = []

            post_args = [tup[0].split('/') for tup in tups]
            posts = steem.get_content_batch(post_args)
            post_ids = [tup[1] for tup in tups]
            post_levels = [tup[2] for tup in tups]

            coremap = cls._get_core_fields(tups)
            for pid, post, level in zip(post_ids, posts, post_levels):
                if post['author']:
                    assert pid in coremap, 'pid not in coremap'
                    if pid in coremap:
                        core = coremap[pid]
                        post['category'] = core['category']
                        post['community_id'] = core['community_id']
                        post['gray'] = core['is_muted']
                        post['hide'] = not core['is_valid']
                    buffer.extend(cls._sql(pid, post, level=level))
                else:
                    # When a post has been deleted (or otherwise DNE),
                    # steemd simply returns a blank post  object w/ all
                    # fields blank. While it's best to not try to cache
                    # already-deleted posts, it can happen during missed
                    # post sweep and while using `trail_blocks` > 0.

                    # monitor: post not found which should def. exist; see #173
                    sql = """SELECT id, author, permlink, is_deleted
                               FROM hive_posts WHERE id = :id"""
                    row = DB.query_row(sql, id=pid)
                    if row['is_deleted']:
                        log.info("found deleted post for %s: %s", level, row)
                        if level == 'payout':
                            log.warning("force delete %s", row)
                            cls.delete(pid, row['author'], row['permlink'])
                    elif level == 'insert':
                        log.error("insert post not found -- DEFER %s", row)
                        cls.insert(row['author'], row['permlink'], pid)
                    else:
                        log.warning("%s post not found -- DEFER %s", level, row)
                        cls._dirty(level, row['author'], row['permlink'], pid)

                cls._bump_last_id(pid)

            timer.batch_lap()
            DB.batch_queries(buffer, trx)

            timer.batch_finish(len(posts))
            if len(tuples) >= 1000:
                log.info(timer.batch_status())

    @classmethod
    def last_id(cls):
        """Retrieve the latest post_id that was cached."""
        if cls._last_id == -1:
            # after initial query, we maintain last_id w/ _bump_last_id()
            sql = "SELECT COALESCE(MAX(post_id), 0) FROM hive_posts_cache"
            cls._last_id = DB.query_one(sql)
        return cls._last_id

    @classmethod
    def _community_id(cls, category, community):
        if category == community:
            # (heuristic may give false positives)
            return Community.get_id(community)
        return None

    @classmethod
    def _get_core_fields(cls, tups):
        """Cached posts must inherit some properties from hive_posts.

        Purpose
         - immutable `category` (returned from steemd is subject to change)
         - authoritative community_id can be determined and written
         - community muted/valid cols override legacy gray/hide logic
        """
        # get list of ids of posts which are to be inserted
        # TODO: try conditional. currently competes w/ legacy flags on vote
        #ids = [tup[1] for tup in tups if tup[2] in ('insert', 'update')]
        ids = [tup[1] for tup in tups]
        if not ids:
            return {}

        # build a map of id->fields for each of those posts
        sql = """SELECT id, category, community, is_muted, is_valid
                   FROM hive_posts WHERE id IN :ids"""
        core = {r[0]: {'category': r[1],
                       'community_id': cls._community_id(r[1], r[2]),
                       'is_muted': r[3],
                       'is_valid': r[4]}
                for r in DB.query_all(sql, ids=tuple(ids))}
        return core

    @classmethod
    def _bump_last_id(cls, next_id):
        """Update our last_id based on a recent insert."""
        last_id = cls.last_id()
        if next_id <= last_id:
            return

        gap = next_id - last_id - 1
        if gap:
            log.info("skipped %d ids %d -> %d", gap, last_id, next_id)
            cls._ensure_safe_gap(last_id, next_id)

        cls._last_id = next_id

    @classmethod
    def _ensure_safe_gap(cls, last_id, next_id):
        """Paranoid check of important operating assumption."""
        sql = """SELECT COUNT(*) FROM hive_posts
                  WHERE id BETWEEN :x1 AND :x2 AND is_deleted = '0'"""
        missing_posts = DB.query_one(sql, x1=(last_id + 1), x2=(next_id - 1))
        if missing_posts:
            raise Exception("found cache gap: %d --> %d (%d)"
                            % (last_id, next_id, missing_posts))

    @classmethod
    def _sql(cls, pid, post, level=None):
        """Given a post and "update level", generate SQL edit statement.

        Valid levels are:
         - `insert`: post does not yet exist in cache
         - `payout`: post was paidout
         - `update`: post was modified
         - `upvote`: post payout/votes changed
         - `recount`: post child count changed
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
                ('community_id',  post['community_id']), # immutable*
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
            ])

        # update tags if action is insert/update and is root post
        if level in ['insert', 'update'] and not post['depth']:
            diff = level != 'insert' # do not attempt tag diff on insert
            tag_sqls.extend(cls._tag_sqls(pid, basic['tags'], diff=diff))

        # if there's a pending promoted value to write, pull it out
        if pid in cls._pending_promoted:
            bal = cls._pending_promoted.pop(pid)
            values.append(('promoted', bal))

        # update unconditionally
        payout = post_payout(post)
        stats = post_stats(post)

        # //--
        # if community - override fields.
        # TODO: make conditional (date-based?)
        assert 'community_id' in post, 'comm_id not loaded'
        if post['community_id']:
            stats['hide'] = post['hide']
            stats['gray'] = post['gray']
        # //--

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
            ('author_rep',  "%f" % stats['author_rep']),
            ('children',    "%d" % min(post['children'], 32767)),
        ])

        # if recounting, update the parent next pass.
        if level == 'recount' and post['depth']:
            cls.recount(post['parent_author'], post['parent_permlink'])

        # trigger any notifications
        cls._notifs(post, pid, level, payout['payout'])

        # build the post insert/update SQL, add tag SQLs
        if level == 'insert':
            sql = cls._insert(values)
        else:
            sql = cls._update(values)
        return [sql] + tag_sqls

    @classmethod
    def _notifs(cls, post, pid, level, payout):
        # pylint: disable=too-many-locals
        author = post['author']
        author_id = Accounts.get_id(author)
        parent_author = post['parent_author']
        date = post['last_update']

        # reply notif
        if level == 'insert' and parent_author and parent_author != author:
            parent_author_id = Accounts.get_id(parent_author)
            if not cls._muted(parent_author_id, author_id):
                Notify('reply', src_id=author_id, dst_id=parent_author_id,
                       score=Accounts.default_score(author), post_id=pid,
                       when=date).write()

        # mentions notif
        if level in ('insert', 'update'):
            accounts = set(filter(Accounts.exists, mentions(post['body'])))
            accounts -= {author, parent_author}
            if len(accounts) <= 10:
                for mention in accounts:
                    mention_id = Accounts.get_id(mention)
                    if (not cls._mentioned(pid, mention_id)
                            and not cls._muted(mention_id, author_id)):
                        score = Accounts.default_score(author)
                        penalty = min([score, 5 * (len(accounts) - 1)])
                        Notify('mention', src_id=author_id,
                               dst_id=mention_id, post_id=pid, when=date,
                               score=(score - penalty)).write()
            else:
                url = '@%s/%s' % (author, post['permlink'])
                log.warning("%s - %d mentions", url, len(accounts))

        # votes notif
        url = post['author'] + '/' + post['permlink']
        if url in cls._votes:
            voters = cls._votes[url]
            del cls._votes[url]
            net = float(post['net_rshares'])
            ratio = float(payout) / net if net else 0
            for vote in post['active_votes']:
                rshares = int(vote['rshares'])
                if vote['voter'] not in voters or rshares < 10e9: continue
                contrib = int(1000 * ratio * rshares)
                if contrib < 1: continue # < $0.001

                voter_id = Accounts.get_id(vote['voter'])
                if not cls._voted(pid, author_id, voter_id):
                    score = min(100, len(str(contrib)) * 20)
                    payload = "$%.3f" % (contrib / 1000)
                    log.warning("%s -- %d/100 -- %d rshares", payload, score, rshares)
                    Notify('vote', src_id=voter_id, dst_id=author_id, when=date,
                           post_id=pid, score=score, payload=payload).write()


    @classmethod
    def _muted(cls, account, target):
        # TODO: optimize (mem cache?)
        sql = """SELECT 1 FROM hive_follows
                  WHERE follower = :account
                    AND following = :target
                    AND state = 2"""
        return DB.query_col(sql, account=account, target=target)

    @classmethod
    def _voted(cls, post_id, account_id, voter_id):
        # TODO: optimize (add idx, mem cache?)
        sql = """SELECT 1
                   FROM hive_notifs
                  WHERE dst_id = :dst_id
                    AND src_id = :src_id
                    AND post_id = :post_id
                    AND type_id = 17"""
        return bool(DB.query_one(sql, dst_id=account_id,
                                 post_id=post_id, src_id=voter_id))

    @classmethod
    def _mentioned(cls, post_id, account_id):
        # TODO: optimize (add idx, mem cache?)
        sql = """SELECT 1
                   FROM hive_notifs
                  WHERE dst_id = :dst_id
                    AND post_id = :post_id
                    AND type_id = 16"""
        return bool(DB.query_one(sql, dst_id=account_id, post_id=post_id))

    @classmethod
    def _tag_sqls(cls, pid, tags, diff=True):
        """Generate SQL "deltas" for a post_id's associated tags."""
        next_tags = set(tags)
        curr_tags = set()
        if diff:
            sql = "SELECT tag FROM hive_post_tags WHERE post_id = :id"
            curr_tags = set(DB.query_col(sql, id=pid))

        to_rem = (curr_tags - next_tags)
        if to_rem:
            sql = "DELETE FROM hive_post_tags WHERE post_id = :id AND tag IN :tags"
            yield (sql, dict(id=pid, tags=tuple(to_rem)))

        to_add = (next_tags - curr_tags)
        if to_add:
            params = _keyify(to_add)
            vals = ["(:id, :%s)" % key for key in params.keys()]
            sql = "INSERT INTO hive_post_tags (post_id, tag) VALUES %s"
            sql += " ON CONFLICT DO NOTHING" # (conflicts due to collation)
            yield (sql % ','.join(vals), {'id': pid, **params})

    @classmethod
    def _insert(cls, values):
        return DB.build_insert('hive_posts_cache', values, pk='post_id')

    @classmethod
    def _update(cls, values):
        return DB.build_update('hive_posts_cache', values, pk='post_id')
