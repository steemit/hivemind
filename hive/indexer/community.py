"""[WIP] Process community ops."""

import logging

from funcy.seqs import flatten
from hive.db.methods import query_one
from hive.community.roles import PERMISSIONS, is_permitted
from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts

log = logging.getLogger(__name__)

# community methods
# -----------------
def process_json_community_op(account, op_json, date):
    """Validates community op and apply state changes to db."""
    #pylint: disable=line-too-long,unused-variable
    cmd_name, cmd_op = op_json  # ['flagPost', {community: '', author: '', ...}]

    commands = list(flatten(PERMISSIONS.values()))
    if cmd_name not in commands:
        return

    log.warning("community op from %s @ %s -- %s", account, date, op_json)

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
    post_id, depth = Posts.get_id_and_depth(cmd_op.get('author'), cmd_op.get('permlink'))
    if not post_id:
        return

    # If command references an account, ensure it's valid
    account_id = Accounts.get_id(cmd_op.get('account'))

    # If command references a list of accounts, ensure they are valid
    account_ids = list(map(Accounts.get_id, cmd_op.get('accounts')))

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

def is_community(name):
    """Check if named community exists."""
    return bool(query_one("SELECT 1 FROM hive_communities WHERE name = :name", name=name))
