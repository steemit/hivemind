import logging

from funcy.seqs import first, second
from hive.db.methods import query

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.feed_cache import FeedCache

from hive.indexer.community import process_json_community_op
from hive.indexer.normalize import load_json_key

log = logging.getLogger(__name__)

class CustomOp:

    @classmethod
    def process_ops(cls, ops, block_num, block_date):
        for op in ops:
            if op['id'] not in ['follow', 'com.steemit.community']:
                continue

            # we are assuming `required_posting_auths` is always used and length 1.
            # it may be that some ops will require `required_active_auths` instead
            # (e.g. if we use that route for admin action of acct creation)
            # if op['required_active_auths']:
            #    log.warning("unexpected active auths: %s" % op)
            if len(op['required_posting_auths']) != 1:
                log.warning("unexpected auths: %s" % op)
                continue

            account = op['required_posting_auths'][0]
            op_json = load_json_key(op, 'json')

            if op['id'] == 'follow':
                if block_num < 6000000 and not isinstance(op_json, list):
                    op_json = ['follow', op_json]  # legacy compat
                cls._process_legacy(account, op_json, block_date)
            elif op['id'] == 'com.steemit.community':
                if block_num > 13e6:
                    process_json_community_op(account, op_json, block_date)

    @classmethod
    def _process_legacy(cls, account, op_json, block_date):
        """ Process legacy 'follow' plugin ops (follow/mute/clear, reblog) """
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
            cls._follow(account, op_json, block_date)
        elif cmd == 'reblog':
            cls._reblog(account, op_json, block_date)

    @classmethod
    def _follow(cls, account, op_json, block_date):
        if not isinstance(op_json['what'], list):
            return
        what = first(op_json['what']) or 'clear'
        if what not in ['blog', 'clear', 'ignore']:
            return
        if not all([key in op_json for key in ['follower', 'following']]):
            print("bad follow op: {} {}".format(block_date, op_json))
            return

        follower = op_json['follower']
        following = op_json['following']

        if follower == following:
            return  # can't follow self
        if follower != account:
            return  # impersonation
        if not all(map(Accounts.exists, [follower, following])):
            return  # invalid input

        sql = """
        INSERT INTO hive_follows (follower, following, created_at, state)
        VALUES (:fr, :fg, :at, :state) ON CONFLICT (follower, following) DO UPDATE SET state = :state
        """
        state = {'clear': 0, 'blog': 1, 'ignore': 2}[what]
        query(sql, fr=Accounts.get_id(follower), fg=Accounts.get_id(following),
              at=block_date, state=state)
        Accounts.dirty_follows(follower)
        Accounts.dirty_follows(following)

    @classmethod
    def _reblog(cls, account, op_json, block_date):
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
            print("reblog: post not found: {}/{}".format(author, permlink))
            return

        if 'delete' in op_json and op_json['delete'] == 'delete':
            query("DELETE FROM hive_reblogs WHERE account = :a AND post_id = :pid LIMIT 1", a=blogger, pid=post_id)
            FeedCache.delete(post_id, Accounts.get_id(blogger))
        else:
            sql = "INSERT INTO hive_reblogs (account, post_id, created_at) VALUES (:a, :pid, :date) ON CONFLICT (account, post_id) DO NOTHING"
            query(sql, a=blogger, pid=post_id, date=block_date)
            FeedCache.insert(post_id, Accounts.get_id(blogger), block_date)
