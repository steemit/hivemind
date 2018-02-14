from hive.indexer.posts import Posts
from hive.indexer.accounts import Accounts
from hive.db.adapter import Db
from hive.utils.normalize import parse_amount

DB = Db.instance()

class Payments:
    @classmethod
    def op_transfer(cls, op, tx_idx, num, date):
        record = cls._validated(op, tx_idx, num, date)
        if not record:
            return

        print("apply promotion balance of %f to %s" % (record['amount'], op['memo']))

        # add payment record
        insert = DB.build_upsert('hive_payments', 'id', record)
        DB.query(insert)

        # update post record
        sql = "UPDATE hive_posts SET promoted = promoted + :add WHERE id = :id"
        DB.query(sql, add=record['amount'], id=record['post_id'])

    @classmethod
    def _validated(cls, op, tx_idx, num, date):
        if op['to'] != 'null':
            return # only care about payments to null

        amount, token = parse_amount(op['amount'])
        if token != 'SBD':
            return # only care about SBD payments

        url = op['memo']
        if not url or url.count('/') != 1 or url[0] != '@':
            print("invalid payment memo: {}".format(url))
            return

        author, permlink = url[1:].split('/')
        if not Accounts.exists(author):
            return

        post_id = Posts.get_id(author, permlink)
        if not post_id:
            print("post does not exist: %s" % url)
            return

        return {'id': None,
                'block_num': num,
                'tx_idx': tx_idx,
                'post_id': post_id,
                'from_account': op['from'],
                'to_account': op['to'],
                'amount': amount,
                'token': token}
