import json
import logging
import re

from funcy.seqs import first, second, drop
from hive.community.roles import get_user_role, privacy_map
from hive.db.methods import query_one, query, db_last_block
from steem.blockchain import Blockchain
from steem.post import Post
from steem.steemd import Steemd
from steem.utils import parse_time
from steembase.exceptions import PostDoesNotExist
from toolz import update_in, partition_all

log = logging.getLogger(__name__)


# utils
# -----
def construct_identifier(op):
    return '%s/%s' % (op['author'], op['permlink'])


def is_valid_account_name(name):
    return re.match('^[a-z][a-z0-9\-.]{2,15}$', name)


def json_expand(json_op):
    """ For custom_json ops. """
    if type(json_op) == dict and 'json' in json_op:
        return update_in(json_op, ['json'], json.loads)

    return json_op


# core
# ----
def get_account_id(name):
    if is_valid_account_name(name):
        return query_one("SELECT id FROM hive_accounts WHERE name = '%s' LIMIT 1" % name)


def get_post_id_and_depth(author, permlink):
    res = None
    if author:
        res = first(query(
            "SELECT id, depth FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (author, permlink)))
    return res or (None, -1)


def register_accounts(accounts, date):
    for account in set(accounts):
        if not get_account_id(account):
            query("INSERT INTO hive_accounts (name, created_at) VALUES ('%s', '%s')" % (account, date))


def delete_posts(ops):
    for op in ops:
        query("UPDATE hive_posts SET is_deleted = 1 WHERE author = '%s' AND permlink = '%s'" % (
            op['author'], op['permlink']))


def register_posts(ops, date):
    for op in ops:
        is_edit = query_one(
            "SELECT 1 FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (op['author'], op['permlink']))
        if is_edit:
            continue  # ignore edits to posts

        # this method needs to perform auth checking e.g. is op.author authorized to post in op.community?
        try:
            community_or_blog = create_post_as(op)
        except PostDoesNotExist:
            return

        if op['parent_author'] == '':
            parent_id = None
            depth = 0
            category = op['parent_permlink']
        else:
            parent_data = first(query("SELECT id, depth, category FROM hive_posts WHERE author = '%s' "
                                      "AND permlink = '%s'" % (op['parent_author'], op['parent_permlink'])))
            parent_id, parent_depth, category = parent_data
            depth = parent_depth + 1

        query("INSERT INTO hive_posts (parent_id, author, permlink, category, community, depth, created_at) "
              "VALUES (%s, '%s', '%s', '%s', '%s', %d, '%s')" % (
                  parent_id or 'NULL', op['author'], op['permlink'], category, community_or_blog, depth, date))


def process_json_follow_op(account, op_json, block_date):
    """ This method processes any legacy 'follow' plugin ops (follow/mute/clear, reblog) """
    if type(op_json) != list:
        return
    if first(op_json) not in ['follow', 'reblog']:
        return
    if not isinstance(second(op_json), dict):
        return

    cmd, op_json = op_json  # ['follow', {data...}]
    if cmd == 'follow':
        if type(op_json['what']) != list:
            return
        what = first(op_json['what']) or 'clear'
        if what not in ['blog', 'clear', 'ignore']:
            return

        follower = op_json['follower']
        following = op_json['following']

        if follower != account:
            return  # impersonation attempt
        if not all(filter(is_valid_account_name, [follower, following])):
            return

        if what == 'blog':
            query("INSERT IGNORE INTO hive_follows (follower, following, created_at) "
                  "VALUES ('%s', '%s', '%s')" % (follower, following, block_date))
        elif what == 'ignore':
            # TODO: `hive_follows` needs a flag to distinguish between a follow and a mute.
            # otherwise we need to create a `hive_ignores` table to track mutes.
            pass
        elif what == 'clear':
            query("DELETE FROM hive_follows WHERE follower = '%s' AND following = '%s' LIMIT 1" % (follower, following))

    elif cmd == 'reblog':
        blogger = op_json['account']
        author = op_json['author']
        permlink = op_json['permlink']

        if blogger != account:
            return  # impersonation
        if not all(filter(is_valid_account_name, [account, blogger])):
            return

        post_id, depth = get_post_id_and_depth(author, permlink)

        if depth > 0:
            return  # prevent comment reblogs

        if 'delete' in op_json and op_json['delete'] == 'delete':
            query("DELETE FROM hive_reblogs WHERE account = '%s' AND post_id = %d LIMIT 1" % (blogger, post_id))
        else:
            query("INSERT IGNORE INTO hive_reblogs (account, post_id, created_at) "
                  "VALUES ('%s', %d, '%s')" % (blogger, post_id, block_date))


# community methods
# -----------------
def process_json_community_op(account, op_json, date):
    ## TODO
    return


def create_post_as(comment: dict) -> str:
    """ Given a new Steem post/comment, add it to appropriate community.
    
    For a comment to be valid, these conditions apply:
        - Post must be new (edits don't count)
        - Author is allowed to post in this community (membership & privacy)
        - Author is not muted in this community
        
    
    Args:
        comment (dict): Operation with the post to add.
        
    Returns:
        name (str): If all conditions apply, community name we're posting into.
                    Otherwise, authors own name (blog) is returned.
    """
    post = Post(comment)

    if not post.community:
        return post.author

    community = get_community(post.community)
    if not community:
        return post.author

    if is_author_muted(post.author, post.community):
        return post.author

    privacy = community['privacy']
    if privacy_map[privacy] == 'open':
        pass
    elif privacy_map[privacy] == 'restricted':
        # guests cannot create top-level posts in restricted communities
        if post.is_main_post() and get_user_role(post.author, post.community) == 'guest':
            return post.author
    elif privacy_map[privacy] == 'closed':
        # we need at least member permissions to post or comment
        if get_user_role(post.author, post.community) == 'guest':
            return post.author

    return post.community


def get_community(community_name):
    return query_one("SELECT * FROM hive_communities WHERE account = '%s' LIMIT 1" % community_name)


def is_author_muted(author_name: str, community_name: str) -> bool:
    return False


def is_community_valid(name: str) -> bool:
    """ Given a community name, check if its a valid community."""
    return bool(get_community(name))


# run indexer
# -----------
def process_block(block):
    date = parse_time(block['timestamp'])
    block_num = int(block['previous'][:8], base=16) + 1
    txs = block['transactions']

    # NOTE: currently `prev` tracks the previous block number and this is enforced with a FK constraint.
    # soon we will have access to prev block hash and current hash in the API return value, we should use this instead.
    # the FK constraint will then fail if we somehow end up on the wrong side in a fork reorg.
    query("INSERT INTO hive_blocks (num, prev, txs, created_at) "
          "VALUES ('%d', '%d', '%d', '%s')" % (block_num, block_num - 1, len(txs), date))
    if block_num % 1000 == 0:
        log.info("processing block {} at {} with {} txs".format(block_num, date, len(txs)))

    accounts = set()
    comments = []
    json_ops = []
    deleted = []
    for tx in txs:
        for operation in tx['operations']:
            op_type, op = operation

            if op_type == 'pow':
                accounts.add(op['worker_account'])
            elif op_type == 'pow2':
                accounts.add(op['work'][1]['input']['worker_account'])
            elif op_type in ['account_create', 'account_create_with_delegation']:
                accounts.add(op['new_account_name'])
            elif op_type == 'comment':
                comments.append(op)
            elif op_type == 'delete_comment':
                deleted.append(op)
            elif op_type == 'custom_json':
                json_ops.append(op)

    register_accounts(accounts, date)  # if an account does not exist, mark it as created in this block
    register_posts(comments, date)  # if this is a new post, add the entry and validate community param
    delete_posts(deleted)  # mark hive_posts.is_deleted = 1

    for op in map(json_expand, json_ops):
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
        op_json = op['json']

        if op['id'] == 'follow':
            if block_num < 6000000 and type(op_json) != list:
                op_json = ['follow', op_json]  # legacy compat
            process_json_follow_op(account, op_json, date)
        elif op['id'] == 'com.steemit.community':
            process_json_community_op(account, op_json, date)


def process_blocks(blocks):
    query("START TRANSACTION")
    for block in blocks:
        process_block(block)
    query("COMMIT")


def sync_from_file(file_path, chunk_size=250):
    last_block = db_last_block()
    with open(file_path) as f:
        # each line in file represents one block
        # we can skip the blocks we already have
        remaining = drop(last_block, f)
        for batch in partition_all(chunk_size, remaining):
            process_blocks(map(json.loads, batch))


def sync_from_steemd():
    b = Blockchain()
    h = b.stream_from(
        start_block=db_last_block() + 1,
        full_blocks=True,
    )
    for block in h:
        process_blocks([block])


# testing
# -------
def run():
    # fast-load first 10m blocks
    if db_last_block() < int(1e7):
        sync_from_file('/home/user/Downloads/blocks.json.lst')

    sync_from_steemd()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = Steemd().last_irreversible_block_num
    hive_head = db_last_block()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    # setup()
    run()
