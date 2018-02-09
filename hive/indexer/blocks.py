from hive.db.methods import query_row, query

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
        pass
