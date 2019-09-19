"""Main custom_json op handler."""
import logging

from funcy.seqs import first, second
from hive.db.adapter import Db
from hive.db.db_state import DbState

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.feed_cache import FeedCache
from hive.indexer.follow import Follow
from hive.indexer.notify import Notify

from hive.indexer.community import process_json_community_op
from hive.utils.normalize import load_json_key

DB = Db.instance()

log = logging.getLogger(__name__)

def _get_auth(op):
    """get account name submitting a custom_json op.

    Hive custom_json op processing requires `required_posting_auths`
    is always used and length 1. It may be that some ops will require
    `required_active_auths` in the future. For now, these are ignored.
    """
    if op['required_auths']:
        log.warning("unexpected active auths: %s", op)
        return None
    if len(op['required_posting_auths']) != 1:
        log.warning("unexpected auths: %s", op)
        return None
    return op['required_posting_auths'][0]

class CustomOp:
    """Processes custom ops and dispatches updates."""

    @classmethod
    def process_ops(cls, ops, block_num, block_date):
        """Given a list of operation in block, filter and process them."""
        for op in ops:
            if op['id'] not in ['follow', 'community']:
                continue

            account = _get_auth(op)
            if not account:
                continue

            op_json = load_json_key(op, 'json')
            if op['id'] == 'follow':
                if block_num < 6000000 and not isinstance(op_json, list):
                    op_json = ['follow', op_json]  # legacy compat
                cls._process_legacy(account, op_json, block_date)
            elif op['id'] == 'community':
                if block_num > 30e6:
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
        if ('account' not in op_json
                or 'author' not in op_json
                or 'permlink' not in op_json):
            return
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

        author_id = Accounts.get_id(author)
        blogger_id = Accounts.get_id(blogger)

        if 'delete' in op_json and op_json['delete'] == 'delete':
            DB.query("DELETE FROM hive_reblogs WHERE account = :a AND "
                     "post_id = :pid LIMIT 1", a=blogger, pid=post_id)
            if not DbState.is_initial_sync():
                FeedCache.delete(post_id, blogger_id)

        else:
            sql = ("INSERT INTO hive_reblogs (account, post_id, created_at) "
                   "VALUES (:a, :pid, :date) ON CONFLICT (account, post_id) DO NOTHING")
            DB.query(sql, a=blogger, pid=post_id, date=block_date)
            if not DbState.is_initial_sync():
                FeedCache.insert(post_id, blogger_id, block_date)
                Notify('reblog', src_id=blogger_id, dst_id=author_id,
                       post_id=post_id, when=block_date,
                       score=Accounts.default_score(blogger)).write()
