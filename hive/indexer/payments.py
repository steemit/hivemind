from hive.indexer.posts import Posts
from hive.indexer.accounts import Accounts

def parse_amount(value):
    if isinstance(value, str):
        return str.split(' ')

    elif isinstance(value, list):
        import decimal
        satoshis, precision, nai = value
        amount = decimal.Decimal(satoshis) / (10**precision)
        names = {'@@000000013': 'SBD'}
        assert nai in names, "unrecognized nai: %s" % nai
        return (amount, names[nai])


class Payments:
    @classmethod
    def op_transfer(cls, op, tx_idx, num, date):
        record = cls._validated_record(op, tx_idx, num, date)
        if not record:
            return

        print("apply promotion balance... %s" % repr(record))

    @classmethod
    def _validated_record(cls, op, tx_idx, num, date):
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
