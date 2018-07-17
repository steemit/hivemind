"""Main custom_json op handler."""
import logging

from funcy.seqs import first, second
from hive.db.adapter import Db
from hive.db.db_state import DbState

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.feed_cache import FeedCache
from hive.indexer.follow import Follow

from hive.indexer.community import process_json_community_op
from hive.utils.normalize import load_json_key

DB = Db.instance()

log = logging.getLogger(__name__)

class CustomOp:
    """Processes custom ops and dispatches updates."""

    @classmethod
    def process_ops(cls, ops, block_num, block_date):
        """Given a list of operation in block, filter and process them."""
        for op in ops:
            if op['id'] not in ['follow', 'com.steemit.community']:
                continue

            # we assume `required_posting_auths` is always used and length 1.
            # it may be that some ops require `required_active_auths` instead.
            # (e.g. if we use that route for admin action of acct creation)
            # if op['required_active_auths']:
            #    log.warning("unexpected active auths: %s" % op)
            if len(op['required_posting_auths']) != 1:
                log.warning("unexpected auths: %s", op)
                continue

            account = op['required_posting_auths'][0]
            op_json = load_json_key(op, 'json')

            if op['id'] == 'follow':
                if block_num < 6000000 and not isinstance(op_json, list):
                    op_json = ['follow', op_json]  # legacy compat
                cls._process_legacy(account, op_json, block_date)
            elif op['id'] == 'com.steemit.community':
                if block_num > 23e6:
                    process_json_community_op(account, op_json, block_date)

    @classmethod
    def _process_legacy(cls, account, op_json, block_date):
        """Handle legacy 'follow' plugin ops (follow/mute/clear, reblog)"""
        if not isinstance(op_json, list):
            return
        if len(op_json) != 2:
            return
        if first(op_json) not in ['follow', 'reblog']:
            return
        if not isinstance(second(op_json), dict):
            return

        cmd, op_json = op_json  # ['follow', {data...}]
        if cmd == 'follow':
            Follow.follow_op(account, op_json, block_date)
        elif cmd == 'reblog':
            cls.reblog(account, op_json, block_date)

    @classmethod
    def reblog(cls, account, op_json, block_date):
        """Handle legacy 'reblog' op"""
        blogger = op_json['account']
        author = op_json['author']
        permlink = op_json['permlink']

        if blogger != account:
            return  # impersonation
        if not all(map(Accounts.exists, [author, blogger])):
            return

        post_id, depth = Posts.get_id_and_depth(author, permlink)

        if depth > 0:
            return  # prevent comment reblogs

        if not post_id:
            log.debug("reblog: post not found: %s/%s", author, permlink)
            return

        if 'delete' in op_json and op_json['delete'] == 'delete':
            DB.query("DELETE FROM hive_reblogs WHERE account = :a AND "
                     "post_id = :pid LIMIT 1", a=blogger, pid=post_id)
            if not DbState.is_initial_sync():
                FeedCache.delete(post_id, Accounts.get_id(blogger))

        else:
            sql = ("INSERT INTO hive_reblogs (account, post_id, created_at) "
                   "VALUES (:a, :pid, :date) ON CONFLICT (account, post_id) DO NOTHING")
            DB.query(sql, a=blogger, pid=post_id, date=block_date)
            if not DbState.is_initial_sync():
                FeedCache.insert(post_id, Accounts.get_id(blogger), block_date)
