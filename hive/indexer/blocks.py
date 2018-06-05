"""Blocks processor."""

from hive.db.methods import query_row, query_col, query_one, query
from hive.steem.steem_client import SteemClient

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.cached_post import CachedPost
from hive.indexer.custom_op import CustomOp
from hive.indexer.payments import Payments
from hive.indexer.follow import Follow

class Blocks:
    """Processes blocks, dispatches work, manages `hive_blocks` table."""

    @classmethod
    def head_num(cls):
        """Get hive's head block number."""
        sql = "SELECT num FROM hive_blocks ORDER BY num DESC LIMIT 1"
        return query_one(sql) or 0

    @classmethod
    def head_date(cls):
        """Get hive's head block date."""
        sql = "SELECT created_at FROM hive_blocks ORDER BY num DESC LIMIT 1"
        return str(query_one(sql) or '')

    @classmethod
    def process(cls, block):
        """Process a single block. Always wrap in a transaction!"""
        #assert is_trx_active(), "Block.process must be in a trx"
        return cls._process(block, is_initial_sync=False)

    @classmethod
    def process_multi(cls, blocks, is_initial_sync=False):
        """Batch-process blocks; wrapped in a transaction."""
        query("START TRANSACTION")

        last_num = 0
        try:
            for block in blocks:
                last_num = cls._process(block, is_initial_sync)
        except Exception as e:
            print("[FATAL] could not process block %d" % (last_num + 1))
            raise e

        # Follows flushing needs to be atomic because recounts are
        # expensive. So is tracking follows at all; hence we track
        # deltas in memory and update follow/er counts in bulk.
        Follow.flush(trx=False)

        query("COMMIT")

    @classmethod
    def _process(cls, block, is_initial_sync=False):
        """Process a single block. Assumes a trx is open."""
        # pylint: disable=too-many-boolean-expressions,too-many-branches
        num = cls._push(block)
        date = block['timestamp']

        account_names = set()
        comment_ops = []
        json_ops = []
        delete_ops = []
        voted_authors = set()
        for tx_idx, tx in enumerate(block['transactions']):
            for operation in tx['operations']:
                if isinstance(operation, dict):
                    op_type = operation['type'].split('_operation')[0]
                    op = operation['value']
                else: # pre-appbase-style. remove after deploy. #APPBASE
                    op_type, op = operation

                # account ops
                if op_type == 'pow':
                    account_names.add(op['worker_account'])
                elif op_type == 'pow2':
                    # old style. remove after #APPBASE
                    #account_names.add(op['work'][1]['input']['worker_account'])
                    account_names.add(op['work']['value']['input']['worker_account'])
                elif op_type == 'account_create':
                    account_names.add(op['new_account_name'])
                elif op_type == 'account_create_with_delegation':
                    account_names.add(op['new_account_name'])

                # post ops
                elif op_type == 'comment':
                    comment_ops.append(op)
                elif op_type == 'delete_comment':
                    delete_ops.append(op)
                elif op_type == 'vote':
                    if not is_initial_sync:
                        CachedPost.vote(op['author'], op['permlink'])
                        voted_authors.add(op['author']) # TODO: move to cachedpost

                # misc ops
                elif op_type == 'transfer':
                    Payments.op_transfer(op, tx_idx, num, date)
                elif op_type == 'custom_json':
                    json_ops.append(op)

        Accounts.register(account_names, date)     # register any new names
        Accounts.dirty(voted_authors)              # update rep of voted authors
        Posts.comment_ops(comment_ops, date)       # handle inserts, edits
        Posts.delete_ops(delete_ops)               # handle post deletion
        CustomOp.process_ops(json_ops, num, date)  # follow/reblog/community ops

        if (num > 20000000
                and not account_names
                and not voted_authors
                and not comment_ops
                and not delete_ops
                and not json_ops):
            trxs = len(block['transactions'])
            print("[WARNING] block %d is no-op with %d trxs" % (num, trxs))
            if trxs:
                print("Trxs: {}".format(block['transactions']))

        return num

    @classmethod
    def verify_head(cls):
        """Perform a fork recovery check on startup."""
        hive_head = cls.head_num()
        if not hive_head:
            return

        # move backwards from head until hive/steem agree
        to_pop = []
        cursor = hive_head
        steemd = SteemClient.instance()
        while True:
            assert hive_head - cursor < 25, "fork too deep"
            hive_block = cls._get(cursor)
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
        fork_limit = steemd.last_irreversible()
        assert cursor < fork_limit, "not proceeding until head is irreversible"

        cls._pop(to_pop)

    @classmethod
    def _get(cls, num):
        """Fetch a specific block."""
        sql = """SELECT num, created_at date, hash
                 FROM hive_blocks WHERE num = :num LIMIT 1"""
        return dict(query_row(sql, num=num))

    @classmethod
    def _push(cls, block):
        """Insert a row in `hive_blocks`."""
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
    def _pop(cls, blocks):
        """Pop head blocks to navigate head to a point prior to fork.

        Without an undo database, there is a limit to how fully we can recover.

        If consistency is critical, run hive with TRAIL_BLOCKS=-1 to only index
        up to last irreversible. Otherwise use TRAIL_BLOCKS=2 to stay closer
        while avoiding the vast majority of microforks.

        As-is, there are a few caveats with the following strategy:

         - follow counts can get out of sync (hive needs to force-recount)
         - follow state could get out of sync (user-recoverable)

        For 1.5, also need to handle:

         - hive_communities
         - hive_members
         - hive_flags
         - hive_modlog
        """
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
            query("DELETE FROM hive_payments    WHERE block_num = :num", num=num)
            query("DELETE FROM hive_blocks      WHERE num = :num", num=num)

        query("COMMIT")
        print("[FORK] recovery complete")
        # TODO: manually re-process here the blocks which were just popped.
