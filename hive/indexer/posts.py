import collections

from hive.db.methods import query, query_one, query_row
from hive.db.db_state import DbState

from hive.indexer.normalize import load_json_key
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache

from hive.community.roles import is_community_post_valid

class Posts:

    # LRU cache for (author-permlink -> id) lookup
    _ids = collections.OrderedDict()
    _hits = 0
    _miss = 0

    @classmethod
    def last_id(cls):
        sql = "SELECT MAX(id) FROM hive_posts WHERE is_deleted = '0'"
        return query_one(sql) or 0

    @classmethod
    def get_id(cls, author, permlink):
        url = author+'/'+permlink
        if url in cls._ids:
            cls._hits += 1
            _id = cls._ids.pop(url)
            cls._ids[url] = _id
        else:
            cls._miss += 1
            sql = """SELECT id FROM hive_posts WHERE
                     author = :a AND permlink = :p"""
            _id = query_one(sql, a=author, p=permlink)
            if _id:
                cls._set_id(url, _id)

        # cache stats
        total = cls._hits + cls._miss
        if total % 1000 == 0:
            print("post.id lookups: %d, hits: %d (%.1f%%), entries: %d"
                  % (total, cls._hits, 100.0*cls._hits/total, len(cls._ids)))

        return _id

    @classmethod
    def _set_id(cls, url, pid):
        if len(cls._ids) > 1000000:
            cls._ids.popitem(last=False)
        cls._ids[url] = pid

    @classmethod
    def save_ids_from_tuples(cls, tuples):
        for pid, author, permlink in tuples:
            url = author+'/'+permlink
            if not url in cls._ids:
                cls._set_id(url, pid)
        return tuples

    @classmethod
    def get_id_and_depth(cls, author, permlink):
        _id = cls.get_id(author, permlink)
        if not _id:
            return (None, -1)
        depth = query_one("SELECT depth FROM hive_posts WHERE id = :id", id=_id)
        return (_id, depth)

    @classmethod
    def is_pid_deleted(cls, pid):
        sql = "SELECT is_deleted FROM hive_posts WHERE id = :id"
        return query_one(sql, id=pid)

    # marks posts as deleted and removes them from feed cache
    @classmethod
    def delete_ops(cls, ops):
        for op in ops:
            cls.delete(op)

    # registers new posts (ignores edits), inserts into feed cache
    @classmethod
    def comment_ops(cls, ops, block_date):
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

    # inserts new post records
    @classmethod
    def insert(cls, op, date):
        sql = """INSERT INTO hive_posts (is_valid, parent_id, author, permlink,
                                        category, community, depth, created_at)
                      VALUES (:is_valid, :parent_id, :author, :permlink,
                              :category, :community, :depth, :date)"""
        post = cls._build_post(op, date)
        query(sql, **post)

        if not DbState.is_initial_sync():
            cls._insert_feed_cache(post)

    # re-allocates an existing record flagged as deleted
    @classmethod
    def undelete(cls, op, date, pid):
        sql = """UPDATE hive_posts SET is_valid = :is_valid, is_deleted = '0',
                   parent_id = :parent_id, category = :category,
                   community = :community, depth = :depth
                 WHERE id = :id"""
        post = cls._build_post(op, date, pid)
        query(sql, **post)

        if not DbState.is_initial_sync():
            CachedPost.undelete(pid, post['author'], post['permlink'])
            cls._insert_feed_cache(post)

    # marks a post record as being deleted
    @classmethod
    def delete(cls, op):
        pid, depth = cls.get_id_and_depth(op['author'], op['permlink'])
        query("UPDATE hive_posts SET is_deleted = '1' WHERE id = :id", id=pid)

        if not DbState.is_initial_sync():
            CachedPost.delete(pid, op['author'], op['permlink'])
            if depth == 0:
                FeedCache.delete(pid)

    @classmethod
    def update(cls, op, date, pid):
        # here you could trigger post_cache.dirty or build content diffs...
        pass

    @classmethod
    def _insert_feed_cache(cls, post):
        if post['depth'] == 0:
            post_id = post['id'] or cls.get_id(post['author'], post['permlink'])
            account_id = Accounts.get_id(post['author'])
            FeedCache.insert(post_id, account_id, post['date'])

    @classmethod
    def _build_post(cls, op, date, pid=None):
        # either a top-level post or comment (with inherited props)
        if not op['parent_author']:
            parent_id = None
            depth = 0
            category = op['parent_permlink']
            community = cls._get_op_community(op) or op['author']
        else:
            parent_id = cls.get_id(op['parent_author'], op['parent_permlink'])
            sql = "SELECT depth,category,community FROM hive_posts WHERE id=:id"
            parent_depth, category, community = query_row(sql, id=parent_id)
            depth = parent_depth + 1

        # check post validity in specified context
        is_valid = is_community_post_valid(community, op)
        if not is_valid:
            url = "@{}/{}".format(op['author'], op['permlink'])
            print("Invalid post {} in @{}".format(url, community))

        return dict(author=op['author'], permlink=op['permlink'], id=pid,
                    is_valid=is_valid, parent_id=parent_id, depth=depth,
                    category=category, community=community, date=date)

    # given a comment op, safely read 'community' field from json
    @classmethod
    def _get_op_community(cls, comment):
        md = load_json_key(comment, 'json_metadata')
        if not md or not isinstance(md, dict) or 'community' not in md:
            return None
        community = md['community']
        if not isinstance(community, str):
            return None
        if not Accounts.exists(community):
            return None
        return community
