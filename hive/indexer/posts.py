from hive.db.methods import query, query_one, query_row
from hive.db.db_state import DbState

from hive.indexer.normalize import load_json_key
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache

from hive.community.roles import is_community_post_valid

class Posts:

    @classmethod
    def last_id(cls):
        sql = "SELECT MAX(id) FROM hive_posts WHERE is_deleted = '0'"
        return query_one(sql) or 0

    @classmethod
    def get_id(cls, author, permlink):
        sql = """SELECT id FROM hive_posts WHERE
                 author = :a AND permlink = :p"""
        return query_one(sql, a=author, p=permlink)

    @classmethod
    def get_id_and_depth(cls, author, permlink):
        res = query_row("SELECT id, depth FROM hive_posts WHERE "
                        "author = :a AND permlink = :p",
                        a=author, p=permlink)
        return res or (None, -1)

    @classmethod
    def urls_to_tuples(cls, urls):
        tuples = []
        sql = """SELECT id, is_deleted FROM hive_posts
                  WHERE author = :a AND permlink = :p"""
        for author, permlink in urls:
            pid, is_deleted = query_row(sql, a=author, p=permlink)
            assert pid, "no pid for {}/{}".format(author, permlink)
            if not is_deleted:
                tuples.append([pid, author, permlink])
            else:
                print("Deleting cached post %d" % pid)
                query("DELETE FROM hive_posts_cache WHERE post_id = %d" % pid)

        # sort the results.. must insert cache records sequentially
        return sorted(tuples, key=lambda tup: tup[0])

    # marks posts as deleted and removes them from feed cache
    @classmethod
    def delete_ops(cls, ops):
        for op in ops:
            cls.delete(op)

    # registers new posts (ignores edits), inserts into feed cache
    @classmethod
    def comment_ops(cls, ops, block_date):
        for op in ops:
            sql = ("SELECT id, is_deleted FROM hive_posts "
                   "WHERE author = :a AND permlink = :p")
            ret = query_row(sql, a=op['author'], p=op['permlink'])
            if not ret:
                # post does not exist, go ahead and process it.
                cls.insert(op, block_date)
            elif not ret[1]:
                # post exists, not deleted, thus an edit. ignore.
                cls.update(op, block_date, ret[1])
            else:
                # post exists but was deleted. time to reinstate.
                cls.undelete(op, block_date, ret[0])

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
            CachedPost.delete(pid)
            if depth == 0:
                FeedCache.delete(pid)

    @classmethod
    def update(cls, op, date, pid):
        # here you could trigger post_cache.dirty or build content diffs...
        pass

    @classmethod
    def _insert_feed_cache(cls, post):
        if post['depth'] == 0:
            if not post['id']:
                post['id'] = cls.get_id(post['author'], post['permlink'])
            account_id = Accounts.get_id(post['author'])
            FeedCache.insert(post['id'], account_id, post['date'])

    @classmethod
    def _build_post(cls, op, date, pid=None):
        # either a top-level post or comment (with inherited props)
        if not op['parent_author']:
            parent_id = None
            depth = 0
            category = op['parent_permlink']
            community = cls._get_op_community(op) or op['author']
        else:
            sql = """SELECT id, depth, category, community FROM hive_posts
                     WHERE author = :author AND permlink = :permlink"""
            parent_data = query_row(sql,
                                    author=op['parent_author'],
                                    permlink=op['parent_permlink'])
            parent_id, parent_depth, category, community = parent_data
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
