"""Handles bookmark operations."""

import logging

from hive.db.adapter import Db
from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts

log = logging.getLogger(__name__)

DB = Db.instance()


class Bookmark:
    """Handles processing of adding and removing bookmarks and flushing to db."""

    @classmethod
    def bookmark_op(cls, account, op_json, date):
        """Process an incoming bookmark op."""
        op = cls._validated_op(account, op_json, date)
        if not op:
            return
        
        # perform add bookmark
        if op['action'] == 'add':
            sql = """INSERT INTO hive_bookmarks (account_id, post_id, bookmarked_at)
                     VALUES (:account_id, :post_id, :at)"""
            DB.query(sql, **op)
            # TODO notify author of bookmark added
            # TODO update bookmarks count on post

        # perform remove bookmark
        elif op['action'] == 'remove':
            sql = """DELETE FROM hive_bookmarks
                     WHERE account_id = :account_id AND post_id = :post_id"""
            DB.query(sql, **op)
            # TODO update bookmarks count on post

    @classmethod
    def _validated_op(cls, account, op, date):
        """Validate and normalize the operation."""

        min_params = ['account', 'author', 'permlink', 'action', 'category']
        if any(param not in op for param in min_params):
            # invalid op
            return None
        
        if account != op['account']:
            # impersonation
            return None
        
        if op['action'] not in ['add', 'remove']:
            # invalid action
            return None

        account_id = Accounts.get_id(account)
        if not account_id:
            # invalid account
            return None
        
        post_id = Posts.get_id(op['author'], op['permlink'])
        if not post_id:
            # invalid post
            return None

        exist_bookmark = cls._get_bookmark(account_id, post_id)
        if ((exist_bookmark and op['action'] == 'add')              # already bookmarked
           or (not exist_bookmark and op['action'] == 'remove')):   # not bookmarked
            # invalid action
            return None
        
        return dict(account_id=account_id,
                    post_id=post_id,
                    action=op['action'],
                    at=date)

    @classmethod
    def _get_bookmark(cls, account_id, post_id):
        """Return bookmark if it exists."""
        sql = """SELECT 1 FROM hive_bookmarks
                 WHERE account_id = :account_id AND post_id = :post_id"""
        return DB.query_one(sql, account_id=account_id, post_id=post_id)