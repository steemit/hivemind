import time

from hive.db.methods import query_row, query_col, query_one, query, is_trx_active
from hive.indexer.steem_client import get_adapter

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.cached_post import CachedPost
from hive.indexer.custom_op import CustomOp

class Blocks:

    # Fetch last block
    @classmethod
    def last(cls):
        sql = """SELECT num, created_at date, hash
                 FROM hive_blocks ORDER BY num DESC LIMIT 1"""
        return dict(query_row(sql))

    @classmethod
    def head_num(cls):
        sql = "SELECT num FROM hive_blocks ORDER BY num DESC LIMIT 1"
        return query_one(sql) or 0

    @classmethod
    def head_date(cls):
        sql = "SELECT created_at FROM hive_blocks ORDER BY num DESC LIMIT 1"
        return str(query_one(sql) or '')

    # Fetch specific block
    @classmethod
    def get(cls, num):
        sql = """SELECT num, created_at date, hash
                 FROM hive_blocks WHERE num = :num LIMIT 1"""
        return dict(query_row(sql, num=num))

    # Process a single block. always wrap in a transaction!
    @classmethod
    def process(cls, block, is_initial_sync=False):
        assert is_trx_active(), "Block.process must be in a trx"
        num = cls._push(block)
        date = block['timestamp']

        account_names = set()
        comment_ops = []
        json_ops = []
        delete_ops = []
        voted_authors = set()
        for tx in block['transactions']:
            for operation in tx['operations']:
                op_type, op = operation

                if op_type == 'pow':
                    account_names.add(op['worker_account'])
                elif op_type == 'pow2':
                    account_names.add(op['work'][1]['input']['worker_account'])
                elif op_type == 'account_create':
                    account_names.add(op['new_account_name'])
                elif op_type == 'account_create_with_delegation':
                    account_names.add(op['new_account_name'])
                elif op_type == 'comment':
                    comment_ops.append(op)
                elif op_type == 'delete_comment':
                    delete_ops.append(op)
                elif op_type == 'custom_json':
                    json_ops.append(op)
                elif op_type == 'vote':
                    if not is_initial_sync:
                        CachedPost.vote(op['author'], op['permlink'])
                        voted_authors.add(op['author'])

        Accounts.register(account_names, date)     # register any new names
        Accounts.dirty(voted_authors)              # update rep of voted authors
        Posts.comment_ops(comment_ops, date)       # handle inserts, edits
        Posts.delete_ops(delete_ops)               # handle post deletion
        CustomOp.process_ops(json_ops, num, date)  # follow/reblog/community ops
        return num

    # batch-process blocks, wrap in a transaction
    @classmethod
    def process_multi(cls, blocks, is_initial_sync=False):
        query("START TRANSACTION")
        for block in blocks:
            cls.process(block, is_initial_sync)
        query("COMMIT")

    @classmethod
    def verify_head(cls):
        hive_head = cls.head_num()
        if not hive_head:
            return

        # move backwards from head until hive/steem agree
        to_pop = []
        cursor = hive_head
        steemd = get_adapter()
        while True:
            assert hive_head - cursor < 25, "fork too deep"
            hive_block = cls.get(cursor)
            steem_hash = steemd.get_block(cursor)['block_id']
            match = hive_block['hash'] == steem_hash
            print("[INIT] fork check. block %d: %s vs %s --- %s"
                  % (hive_block['num'], hive_block['hash'],
                     steem_hash, 'ok' if match else 'invalid'))
            if match:
                break
            to_pop.append(hive_block)
            cursor -= 1

        if hive_head == cursor:
            return # no fork!

        print("[FORK] depth is %d; popping blocks %d - %d"
              % (hive_head - cursor, cursor + 1, hive_head))

        # we should not attempt to recover from fork until it's safe
        fork_limit = get_adapter().last_irreversible()
        assert cursor < fork_limit, "not proceeding until head is irreversible"

        cls._pop(to_pop)

    @classmethod
    def _push(cls, block):
        num = int(block['block_id'][:8], base=16)
        txs = block['transactions']
        query("INSERT INTO hive_blocks (num, hash, prev, txs, ops, created_at) "
              "VALUES (:num, :hash, :prev, :txs, :ops, :date)", **{
                  'num': num,
                  'hash': block['block_id'],
                  'prev': block['previous'],
                  'txs': len(txs),
                  'ops': sum([len(tx['operations']) for tx in txs]),
                  'date': block['timestamp']})
        return num

    # Pop head blocks -- used for navigating head to a point prior to a fork.
    # Without an undo database, there is a limit to how fully we can recover.
    #
    # If consistency is critical, run hive with TRAIL_BLOCKS=-1 to only index
    # up to last irreversible. Otherwise use TRAIL_BLOCKS=2 to stay closer
    # while avoiding the vast majority of microforks.
    #
    # As-is, there are a few caveats with the following strategy:
    #  - follow counts can get out of sync (hive needs to force-recount)
    #  - follow state could get out of sync (user-recoverable)
    #
    # For 1.5, also need to handle:
    # - hive_communities
    # - hive_members
    # - hive_flags
    # - hive_modlog
    @classmethod
    def _pop(cls, blocks):
        query("START TRANSACTION")

        for block in blocks:
            num = block['num']
            date = block['date']
            print("[FORK] popping block %d @ %s" % (num, date))
            assert num == cls.head_num(), "can only pop head block"

            # get all affected post_ids in this block
            sql = "SELECT id FROM hive_posts WHERE created_at >= :date"
            post_ids = tuple(query_col(sql, date=date))

            # remove all recent records
            query("DELETE FROM hive_posts_cache WHERE post_id IN :ids", ids=post_ids)
            query("DELETE FROM hive_feed_cache  WHERE created_at >= :date", date=date)
            query("DELETE FROM hive_reblogs     WHERE created_at >= :date", date=date)
            query("DELETE FROM hive_follows     WHERE created_at >= :date", date=date) #*
            query("DELETE FROM hive_post_tags   WHERE post_id IN :ids", ids=post_ids)
            query("DELETE FROM hive_posts       WHERE id IN :ids", ids=post_ids)
            query("DELETE FROM hive_blocks      WHERE num = :num", num=num)

        query("COMMIT")
        print("[FORK] recovery complete")
        # TODO: manually re-process here the blocks which were just popped.
