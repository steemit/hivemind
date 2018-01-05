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
        return query_one("SELECT COALESCE(MAX(id), 0) FROM hive_posts WHERE is_deleted = '0'")

    @classmethod
    def get_id_and_depth(cls, author, permlink):
        res = query_row("SELECT id, depth FROM hive_posts WHERE "
                        "author = :a AND permlink = :p", a=author, p=permlink)
        return res or (None, -1)

    @classmethod
    def urls_to_tuples(cls, urls):
        tuples = []
        sql = "SELECT id, is_deleted FROM hive_posts WHERE author = :a AND permlink = :p"
        for url in urls:
            author, permlink = url.split('/')
            pid, is_deleted = query_row(sql, a=author, p=permlink)
            assert pid, "no pid for {}".format(url)
            if not is_deleted:
                tuples.append([pid, author, permlink])

        # sort the results.. must insert cache records sequentially
        return sorted(tuples, key=lambda tup: tup[0])

    # marks posts as deleted and removes them from feed cache
    @classmethod
    def delete_ops(cls, ops):
        for op in ops:
            post_id, depth = cls.get_id_and_depth(op['author'], op['permlink'])
            query("UPDATE hive_posts SET is_deleted = '1' WHERE id = :id", id=post_id)
            CachedPost.delete(post_id)
            if depth == 0 and not DbState.is_initial_sync():
                FeedCache.delete(post_id)

    # registers new posts (ignores edits), inserts into feed cache
    @classmethod
    def comment_ops(cls, ops, block_date):
        for op in ops:
            sql = ("SELECT id, is_deleted FROM hive_posts "
                   "WHERE author = :a AND permlink = :p")
            ret = query_row(sql, a=op['author'], p=op['permlink'])
            pid = None
            if not ret:
                pass         # post does not exist, go ahead and process it.
            elif not ret[1]:
                continue     # post exists, not deleted, thus an edit. ignore.
            else:
                pid = ret[0] # post exists but was deleted. time to reinstate.

            # set parent & inherited attributes
            if not op['parent_author']:
                parent_id = None
                depth = 0
                category = op['parent_permlink']
                community = cls._get_op_community(op) or op['author']
            else:
                sql = """SELECT id, depth, category, community FROM hive_posts
                         WHERE author = :a AND permlink = :p"""
                parent_data = query_row(sql, a=op['parent_author'], p=op['parent_permlink'])
                parent_id, parent_depth, category, community = parent_data
                depth = parent_depth + 1

            # check post validity in specified context
            is_valid = is_community_post_valid(community, op)
            if not is_valid:
                url = "@{}/{}".format(op['author'], op['permlink'])
                print("Invalid post {} in @{}".format(url, community))

            # if we're undeleting a previously-deleted post, overwrite it
            if pid:
                sql = """
                  UPDATE hive_posts SET is_valid = :is_valid, is_deleted = '0',
                         parent_id = :parent_id, category = :category,
                         community = :community, depth = :depth
                   WHERE id = :id
                """
                query(sql, is_valid=is_valid, parent_id=parent_id,
                      category=category, community=community,
                      depth=depth, id=pid)

                if not DbState.is_initial_sync():
                    CachedPost.undelete(pid, op['author'], op['permlink'])
            else:
                sql = """
                INSERT INTO hive_posts (is_valid, parent_id, author, permlink,
                                        category, community, depth, created_at)
                     VALUES (:is_valid, :parent_id, :author, :permlink,
                             :category, :community, :depth, :date)
                """
                query(sql, is_valid=is_valid, parent_id=parent_id,
                      author=op['author'], permlink=op['permlink'],
                      category=category, community=community,
                      depth=depth, date=block_date)

            # add top-level posts to feed cache
            if not op['parent_author'] and not DbState.is_initial_sync():
                if not pid:
                    pid = query_one("SELECT id FROM hive_posts WHERE author = :a AND "
                                    "permlink = :p", a=op['author'], p=op['permlink'])
                FeedCache.insert(pid, Accounts.get_id(op['author']), block_date)


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
