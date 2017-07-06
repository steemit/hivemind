from hive.db.methods import query_row
from hive.community.roles import get_user_role, privacy_map, permissions, is_permitted

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


def create_post_as(community, comment: dict) -> str:
    """ Given a new Steem post/comment, add it to appropriate community.
    
    For a comment to be valid, these conditions apply:
        - Post must be new (edits don't count)
        - Author is allowed to post in this community (membership & privacy)
        - Author is not muted in this community
        
    
    Args:
        community (str): Community intended for this post op
        comment (dict): Operation with the post to add.
        
    Returns:
        name (str): If all conditions apply, community name we're posting into.
                    Otherwise, authors own name (blog) is returned.
    """

    if not community:
        raise Exception("no community specified")

    author = comment['author']
    if author == community:
        return author

    community_props = get_community(community)
    if not community_props:
        # TODO: if a community is not found, assume it's completely open.
        # all we need to do at that point is validate that its a valid acct name.
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
    return query_row("SELECT * FROM hive_communities WHERE name = '%s' LIMIT 1" % community_name)

def is_author_muted(author_name: str, community_name: str) -> bool:
    return get_user_role(author_name, community_name) is 'muted'
