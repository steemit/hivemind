import json
import logging
import glob
import time

from funcy.seqs import first, second, drop, flatten
from hive.community.roles import get_user_role, privacy_map, permissions, is_permitted
from hive.db.methods import query_one, query, db_last_block
from steem.blockchain import Blockchain
from steem.steemd import Steemd
from steem.utils import parse_time, is_valid_account_name, json_expand
from toolz import partition_all

log = logging.getLogger(__name__)


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
        community_or_blog = create_post_as(op) or op['author']

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

        if what == 'clear':
            query("DELETE FROM hive_follows WHERE follower = '%s' "
                  "AND following = '%s' LIMIT 1" % (follower, following))
        else:
            fields = {'follower': follower, 'following': following,
                    'created_at': created_at, 'is_muted': int(what == 'ignore')}
            query("INSERT INTO hive_follows (follower, following, created_at, is_muted) "
                    "VALUES (:follower, :following, :created_at, :is_muted) "
                    "ON DUPLICATE KEY UPDATE is_muted = :is_muted", fields)

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
    cmd_name, cmd_op = op_json  # ['flagPost', {community: '', author: '', ...}]

    commands = list(flatten(permissions.values()))
    if cmd_name not in commands:
        return

    community = cmd_op['community']
    community_exists = is_community(community)

    # special case: community creation. TODO: does this require ACTIVE auth? or POSTING will suffice?
    if cmd_name == 'create' and not community_exists:
        if account != community:  # only the OWNER may create
            return

        ctype = cmd_op['type']  # restricted, open-comment, public
        # INSERT INTO hive_communities (account, name, about, description, lang, is_nsfw, is_private, created_at)
        # VALUES ('%s', '%s', '%s', '%s', '%s', %d, %d, '%s')" % [account, name, about, description, lang, is_nsfw ? 1 : 0, is_private ? 1 : 0, block_date]
        # INSERT ADMINS---

    # validate permissions
    if not community_exists or not is_permitted(account, community, cmd_name):
        return

    # If command references a post, ensure it's valid
    post_id, depth = get_post_id_and_depth(cmd_op.get('author'), cmd_op.get('permlink'))
    if not post_id:
        return

    # If command references an account, ensure it's valid
    account_id = get_account_id(cmd_op.get('account'))

    # If command references a list of accounts, ensure they are valid
    account_ids = list(map(get_account_id, cmd_op.get('accounts')))

    # ADMIN Actions
    # -------------
    if cmd_name == 'add_admins':
        assert account_ids
        # UPDATE hive_members SET is_admin = 1 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'remove_admins':
        assert account_ids
        # todo: validate at least one admin remains!!!
        # UPDATE hive_members SET is_admin = 0 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'add_mods':
        assert account_ids
        # UPDATE hive_members SET is_mod = 1 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'remove_mods':
        assert account_ids
        # UPDATE hive_members SET is_mod = 0 WHERE account IN (%s) AND community = '%s'

    # MOD USER Actions
    # ----------------
    if cmd_name == 'update_settings':
        # name, about, description, lang, is_nsfw
        # settings {bg_color, bg_color2, text_color}
        # UPDATE hive_communities SET .... WHERE community = '%s'
        assert account_id

    if cmd_name == 'add_posters':
        assert account_ids
        # UPDATE hive_members SET is_approved = 1 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'remove_posters':
        assert account_ids
        # UPDATE hive_members SET is_approved = 0 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'mute_user':
        assert account_id
        # UPDATE hive_members SET is_muted = 1 WHERE account = '%s' AND community = '%s'

    if cmd_name == 'unmute_user':
        assert account_id
        # UPDATE hive_members SET is_muted = 0 WHERE account = '%s' AND community = '%s'

    if cmd_name == 'set_user_title':
        assert account_id
        # UPDATE hive_members SET title = '%s' WHERE account = '%s' AND community = '%s'

    # MOD POST Actions
    # ----------------
    if cmd_name == 'mute_post':
        assert post_id
        # assert all([account_id, post_id])
        # UPDATE hive_posts SET is_muted = 1 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    if cmd_name == 'unmute_post':
        assert post_id
        # UPDATE hive_posts SET is_muted = 0 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    if cmd_name == 'pin_post':
        assert post_id
        # UPDATE hive_posts SET is_pinned = 1 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    if cmd_name == 'unpin_post':
        assert post_id
        # UPDATE hive_posts SET is_pinned = 0 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    # GUEST POST Actions
    # ------------------
    if cmd_name == 'flag_post':
        assert post_id
        # INSERT INTO hive_flags (account, community, author, permlink, comment, created_at) VALUES ()

    # track success (TODO: failures as well?)
    # INSERT INTO hive_modlog (account, community, action, created_at) VALUES  (account, community, json.inspect, block_date)
    return True


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

    if comment['json_metadata'] == "":
        return None

    md = None
    try:
        md = json.loads(comment['json_metadata'])
    except:
        return None

    if md is not dict or 'community' not in md:
        return None

    author = comment['author']
    community = md['community']
    community_props = get_community(community)

    if not community_props:
        return None

    if is_author_muted(author, community):
        return None

    privacy = privacy_map[community_props['privacy']]
    if privacy == 'open':
        pass
    elif privacy == 'restricted':
        # guests cannot create top-level posts in restricted communities
        if comment['parent_author'] == "" and get_user_role(author, community) == 'guest':
            return None
    elif privacy == 'closed':
        # we need at least member permissions to post or comment
        if get_user_role(author, community) == 'guest':
            return None

    return community


def get_community(community_name):
    # sqlalchemy:
    # q = select([hive_communities]).where(hive_communities.c.account == community_name).limit(1)
    # conn.execute(q).fetchall()
    return first(query("SELECT * FROM hive_communities WHERE name = '%s' LIMIT 1" % community_name))


def is_author_muted(author_name: str, community_name: str) -> bool:
    return get_user_role(author_name, community_name) is 'muted'


def is_community(name: str) -> bool:
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
    if block_num % 100000 == 0:
        log.warning("processing block {} at {} with {} txs".format(block_num, date, len(txs)))

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


def sync_from_checkpoints():
    last_block = db_last_block()

    fn = lambda f: [int(f.split('/')[1].split('.')[0]), f]
    files = map(fn, glob.glob("checkpoints/*.json.lst"))
    files = sorted(files, key = lambda f: f[0])

    last_read = 0
    for (num, path) in files:
        if last_block < num:
            print("Last block: {} -- load {}".format(last_block, path))
            skip_lines = last_block - last_read
            sync_from_file(path, skip_lines)
            last_block = num
        last_read = num


def sync_from_file(file_path, skip_lines, chunk_size=250):
    with open(file_path) as f:
        # each line in file represents one block
        # we can skip the blocks we already have
        remaining = drop(skip_lines, f)
        for batch in partition_all(chunk_size, remaining):
            process_blocks(map(json.loads, batch))


def sync_from_steemd():
    s = Steemd()
    st = time.time()

    start = db_last_block() + 1
    lbound = start
    ubound = s.get_dynamic_global_properties()['head_block_number']

    while lbound < ubound:
        to = min(lbound + 250, ubound)
        #blocks = s.get_blocks_range(lbound, to) # not ordered
        blocks = [s.get_block(n) for n in range(lbound, to + 1)]
        lbound = to + 1
        process_blocks(blocks)

        rate = (lbound - start) / (time.time() - st)
        print("Loaded blocks {} to {} ({}/s) {}m remaining".format(
            start, to, round(rate, 1), round((ubound-lbound) / rate / 60, 2)))


def listen_steemd():
    b = Blockchain()
    h = b.stream_from(
        start_block=db_last_block() + 1,
        full_blocks=True,
    )
    for block in h:
        print("Process block {}".format(block))
        process_blocks([buffer])


# testing
# -------
def run():
    # fast-load checkpoint files
    sync_from_checkpoints()
    # fast-load from steemd
    sync_from_steemd()
    # follow head blocks
    listen_steemd()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = Steemd().last_irreversible_block_num
    hive_head = db_last_block()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    # setup()
    run()
