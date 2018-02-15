from hive.db.adapter import Db
from hive.utils.normalize import parse_amount

from hive.indexer.posts import Posts
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost

DB = Db.instance()

class Payments:
    @classmethod
    def op_transfer(cls, op, tx_idx, num, date):
        record = cls._validated(op, tx_idx, num, date)
        if not record:
            return

        # add payment record
        insert = DB.build_upsert('hive_payments', 'id', record)
        DB.query(insert)

        # read current amount
        sql = "SELECT promoted FROM hive_posts WHERE id = :id"
        curr_amount = DB.query_one(sql, id=record['post_id'])
        new_amount = curr_amount + record['amount']

        # update post record
        sql = "UPDATE hive_posts SET promoted = :val WHERE id = :id"
        DB.query(sql, val=new_amount, id=record['post_id'])

        # notify cached_post of new promoted balance, and trigger update
        CachedPost.update_promoted_amount(record['post_id'], new_amount)
        author, permlink = cls._split_url(op['memo'])
        CachedPost.vote(author, permlink, record['post_id'])

    @classmethod
    def _validated(cls, op, tx_idx, num, date):
        if op['to'] != 'null':
            return # only care about payments to null

        amount, token = parse_amount(op['amount'])
        if token != 'SBD':
            return # only care about SBD payments

        url = op['memo']
        if not cls._validate_url(url):
            print("invalid url: {}".format(url))
            return # invalid url

        author, permlink = cls._split_url(url)
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

    @staticmethod
    def _validate_url(url):
        if not url or url.count('/') != 1 or url[0] != '@':
            return False
        return True

    @staticmethod
    def _split_url(url):
        return url[1:].split('/')
