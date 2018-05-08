"""Core posts manager."""

import collections

from hive.db.adapter import Db
from hive.db.db_state import DbState

from hive.utils.normalize import load_json_key
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache

from hive.community.roles import is_community_post_valid

DB = Db.instance()

class Posts:
    """Handles critical/core post ops and data."""

    # LRU cache for (author-permlink -> id) lookup
    _ids = collections.OrderedDict()
    _hits = 0
    _miss = 0

    @classmethod
    def last_id(cls):
        """Get the last indexed post id."""
        sql = "SELECT MAX(id) FROM hive_posts WHERE is_deleted = '0'"
        return DB.query_one(sql) or 0

    @classmethod
    def get_id(cls, author, permlink):
        """Look up id by author/permlink, making use of LRU cache."""
        url = author+'/'+permlink
        if url in cls._ids:
            cls._hits += 1
            _id = cls._ids.pop(url)
            cls._ids[url] = _id
        else:
            cls._miss += 1
            sql = """SELECT id FROM hive_posts WHERE
                     author = :a AND permlink = :p"""
            _id = DB.query_one(sql, a=author, p=permlink)
            if _id:
                cls._set_id(url, _id)

        # cache stats (under 10M every 10K else every 100K)
        total = cls._hits + cls._miss
        if total % 100000 == 0:
            print("[STATS] posts.id lookups: %d, hits: %d (%.1f%%), entries: %d"
                  % (total, cls._hits, 100.0*cls._hits/total, len(cls._ids)))

        return _id

    @classmethod
    def _set_id(cls, url, pid):
        """Add an entry to the LRU, maintaining max size."""
        assert pid, "no pid provided for %s" % url
        if len(cls._ids) > 1000000:
            cls._ids.popitem(last=False)
        cls._ids[url] = pid

    @classmethod
    def save_ids_from_tuples(cls, tuples):
        """Skim & cache `author/permlink -> id` from external queries."""
        for tup in tuples:
            pid, author, permlink = (tup[0], tup[1], tup[2])
            url = author+'/'+permlink
            if not url in cls._ids:
                cls._set_id(url, pid)
        return tuples

    @classmethod
    def get_id_and_depth(cls, author, permlink):
        """Get the id and depth of @author/permlink post."""
        _id = cls.get_id(author, permlink)
        if not _id:
            return (None, -1)
        depth = DB.query_one("SELECT depth FROM hive_posts WHERE id = :id", id=_id)
        return (_id, depth)

    @classmethod
    def is_pid_deleted(cls, pid):
        """Check if the state of post is deleted."""
        sql = "SELECT is_deleted FROM hive_posts WHERE id = :id"
        return DB.query_one(sql, id=pid)

    @classmethod
    def delete_ops(cls, ops):
        """Given a list of delete ops, mark the posts as deleted.

        (And remove them from the feed cache.)
        """
        for op in ops:
            cls.delete(op)

    @classmethod
    def comment_ops(cls, ops, block_date):
        """Register new/edited/undeleted posts; insert into feed cache."""
        for op in ops:
            pid = cls.get_id(op['author'], op['permlink'])
            if not pid:
                # post does not exist, go ahead and process it.
                cls.insert(op, block_date)
            elif not cls.is_pid_deleted(pid):
                # post exists, not deleted, thus an edit. ignore.
                cls.update(op, block_date, pid)
            else:
                # post exists but was deleted. time to reinstate.
                cls.undelete(op, block_date, pid)

    @classmethod
    def insert(cls, op, date):
        """Inserts new post records."""
        sql = """INSERT INTO hive_posts (is_valid, parent_id, author, permlink,
                                        category, community, depth, created_at)
                      VALUES (:is_valid, :parent_id, :author, :permlink,
                              :category, :community, :depth, :date)"""
        sql += ";SELECT currval(pg_get_serial_sequence('hive_posts','id'))"
        post = cls._build_post(op, date)
        result = DB.query(sql, **post)
        post['id'] = int(list(result)[0][0])
        cls._set_id(op['author']+'/'+op['permlink'], post['id'])

        if not DbState.is_initial_sync():
            CachedPost.insert(op['author'], op['permlink'], post['id'])
            cls._insert_feed_cache(post)

    @classmethod
    def undelete(cls, op, date, pid):
        """Re-allocates an existing record flagged as deleted."""
        sql = """UPDATE hive_posts SET is_valid = :is_valid, is_deleted = '0',
                   parent_id = :parent_id, category = :category,
                   community = :community, depth = :depth
                 WHERE id = :id"""
        post = cls._build_post(op, date, pid)
        DB.query(sql, **post)

        if not DbState.is_initial_sync():
            CachedPost.undelete(pid, post['author'], post['permlink'])
            cls._insert_feed_cache(post)

    @classmethod
    def delete(cls, op):
        """Marks a post record as being deleted."""
        pid, depth = cls.get_id_and_depth(op['author'], op['permlink'])
        DB.query("UPDATE hive_posts SET is_deleted = '1' WHERE id = :id", id=pid)

        if not DbState.is_initial_sync():
            CachedPost.delete(pid, op['author'], op['permlink'])
            if depth == 0:
                FeedCache.delete(pid)

    @classmethod
    def update(cls, op, date, pid):
        """Handle post updates.

        Here we could also build content diffs, but for now just used
        a signal to update cache record.
        """
        # pylint: disable=unused-argument
        if not DbState.is_initial_sync():
            CachedPost.update(op['author'], op['permlink'], pid)

    @classmethod
    def _insert_feed_cache(cls, post):
        """Insert the new post into feed cache if it's not a comment."""
        if not post['depth']:
            account_id = Accounts.get_id(post['author'])
            FeedCache.insert(post['id'], account_id, post['date'])

    @classmethod
    def _build_post(cls, op, date, pid=None):
        """Validate and normalize a post operation."""
        # either a top-level post or comment (with inherited props)
        if not op['parent_author']:
            parent_id = None
            depth = 0
            category = op['parent_permlink']
            community = cls._get_op_community(op) or op['author']
        else:
            parent_id = cls.get_id(op['parent_author'], op['parent_permlink'])
            sql = "SELECT depth,category,community FROM hive_posts WHERE id=:id"
            parent_depth, category, community = DB.query_row(sql, id=parent_id)
            depth = parent_depth + 1

        # check post validity in specified context
        is_valid = is_community_post_valid(community, op)
        if not is_valid:
            url = "@{}/{}".format(op['author'], op['permlink'])
            print("Invalid post {} in @{}".format(url, community))

        return dict(author=op['author'], permlink=op['permlink'], id=pid,
                    is_valid=is_valid, parent_id=parent_id, depth=depth,
                    category=category, community=community, date=date)

    @classmethod
    def _get_op_community(cls, comment):
        """Given a comment op, safely read 'community' field from json."""
        md = load_json_key(comment, 'json_metadata')
        if not md or not isinstance(md, dict) or 'community' not in md:
            return None
        community = md['community']
        if not isinstance(community, str):
            return None
        if not Accounts.exists(community):
            return None
        return community
