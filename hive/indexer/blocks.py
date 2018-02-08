from hive.db.methods import query_row, query
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
        return query_row(sql)

    # Fetch specific block
    @classmethod
    def get(cls, num):
        sql = """SELECT num, created_at date, hash
                 FROM hive_blocks WHERE num = :num LIMIT 1"""
        return query_row(sql, num=num)

    # Process a single block. always wrap in a transaction!
    @classmethod
    def process(cls, block, is_initial_sync=False):
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

        Accounts.register(account_names, date) # register potentially new names
        Accounts.dirty(voted_authors) # update rep of voted authors
        Posts.comment_ops(comment_ops, date) # ignores edits; inserts, validates
        Posts.delete_ops(delete_ops)  # unallocates hive_posts record, delete cache
        CustomOp.process_ops(json_ops, num, date) # follow, reblog, community ops
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
        hive_block = cls.last()
        if not hive_block:
            return
        hive_head = hive_block['num']

        cursor = hive_head
        steemd = get_adapter()
        while True:
            assert hive_head - cursor < 25, "fork too deep"
            steem_hash = steemd.get_block(cursor)['block_id']
            match = hive_block['hash'] == steem_hash
            print("[FORK] block %d: %s vs %s --- %s"
                  % (hive_block['num'], hive_block['hash'],
                     steem_hash, 'ok' if match else 'invalid'))
            if match:
                break
            cursor -= 1
            hive_block = cls.get(cursor)

        print("[FORK] resolving.. pop blocks %d - %d" % (cursor + 1, hive_head))
        print("not implemented")
        exit()

        raise Exception("Not able to resolve fork after %d" % lbound)

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

    @classmethod
    def _pop(cls):
        block = cls.last()
        print("[HIVE] popping block: {}".format(block))
        raise Exception("pop_blocks untested")
        for block in blocks:
            # delete records from:
            # - hive_feed_cache
            # - hive_posts
            # - hive_post_tags?
            # - hive_posts_cache
            # - hive_accounts
            # - hive_reblogs
            # - hive_communities
            # - hive_members
            # - hive_flags
            # - hive_modlog
            # is it safer to not delete and overwrite?
            sql = "DELETE FROM hive_blocks WHERE num = %d" % block['num']
            query(sql)
