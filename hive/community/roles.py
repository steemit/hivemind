"""[WIP] Role-based auth engine for communities."""

from collections import OrderedDict

from hive.db.methods import query_one, query_row

PRIVACY_MAP = {0: 'open', 1: 'restricted', 2: 'closed'}

# each group inherits permissions from groups below it
# hence we use OrderedDict to ensure order
# TODO: implement support for non-json-op actions: post, comment
PERMISSIONS = OrderedDict([
    ('muted', []),
    ('guest', []),
    ('member', ['flag_post']),
    ('moderator', [
        'update_settings',
        'add_posters', 'remove_posters',
        'mute_user', 'unmute_user',
        'mute_post', 'unmute_post',
        'pin_post', 'unpin_post',
        'set_user_title',
    ]),
    ('admin', [
        'add_admins', 'remove_admins',
        'add_mods', 'remove_mods',
    ]),
    ('owner', ['create']),
])


def role_permissions(account_role: str):
    """ Fetch a list of community permissions role is entitled to."""
    acc_perm = []
    for role, role_perm in PERMISSIONS.items():
        acc_perm.extend(role_perm)
        if role == account_role:
            break
    else:
        raise KeyError('User role %s is not defined in permissions table.' % account_role)

    return acc_perm


def is_permitted(account: str, community: str, action: str) -> bool:
    """ Check if an account is allowed to perform an action in a given community."""
    if action not in PERMISSIONS.keys():
        raise ValueError('Action %s is not valid.' % action)

    account_role = get_user_role(account, community)
    return action in role_permissions(account_role)


def get_user_role(account: str, community: str) -> str:
    """Get user role within a specific community."""
    if account == community:
        return 'owner'

    roles = query_one(
        "SELECT is_admin, is_mod, is_approved, is_muted "
        "FROM hive_members"
        "WHERE community = '%s' AND account = '%s' LIMIT 1" % (community, account)
    )

    # todo muted precedes member role?
    # return highest role first
    if roles['is_admin']:
        return 'admin'
    elif roles['is_mod']:
        return 'moderator'
    elif roles['is_muted']:
        return 'muted'
    elif roles['is_approved']:
        return 'member'

    return 'guest'


def get_community_privacy(community: str) -> str:
    """Load community privacy level"""
    type_id = query_one('SELECT type_id from hive_communities WHERE name = "%s"' % community)
    return PRIVACY_MAP.get(type_id)


def is_community_post_valid(community, comment_op: dict) -> str:
    """ Given a new Steem post/comment, check if valid as per community rules

    For a comment to be valid, these conditions apply:
        - Post must be new (edits don't count)
        - Author is allowed to post in this community (membership & privacy)
        - Author is not muted in this community


    Args:
        community (str): Community intended for this post op
        comment_op (dict): Raw post operation

    Returns:
        is_valid (bool): If all checks pass, true
    """

    if not community:
        raise Exception("no community specified")

    author = comment_op['author']
    if author == community:
        return True

    sql = "SELECT * FROM hive_communities WHERE name = :name LIMIT 1"
    community_props = query_row(sql, name=community)
    if not community_props:
        # if this is not a defined community, it's free to post in.
        return True

    if get_user_role(author, community) == 'muted':
        return False

    privacy = PRIVACY_MAP[community_props['privacy']]
    if privacy == 'open':
        pass
    elif privacy == 'restricted':
        # guests cannot create top-level posts in restricted communities
        if comment_op['parent_author'] == "" and get_user_role(author, community) == 'guest':
            return False
    elif privacy == 'closed':
        # we need at least member permissions to post or comment
        if get_user_role(author, community) == 'guest':
            return False

    return True
