from collections import OrderedDict
from typing import List

# each group inherits permissions from groups below it
# hence we use OrderedDict to ensure order
from hive.db.methods import query_one

privacy_types = ('open', 'restricted', 'closed')
privacy_map = dict(enumerate(privacy_types))

permissions = OrderedDict([
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


def role_permissions(account_role: str) -> List[str]:
    """ Fetch a list of community permissions role is entitled to."""
    acc_perm = []
    for role, role_perm in permissions.items():
        acc_perm.extend(role_perm)
        if role == account_role:
            break
    else:
        raise KeyError('User role %s is not defined in permissions table.' % account_role)

    return acc_perm


def is_permitted(account: str, community: str, action: str) -> bool:
    """ Check if an account is allowed to perform an action in a given community."""
    if action not in permissions.keys():
        raise ValueError('Action %s is not valid.' % action)

    account_role = get_user_role(account, community)
    return action in role_permissions(account_role)


def get_user_role(account: str, community: str) -> str:
    if account == community:
        return 'owner'

    roles = query_one("SELECT is_admin, is_mod, is_approved, is_muted "
                      "FROM hive_members"
                      "WHERE community = '%s' AND account = '%s' LIMIT 1" % (community, account))

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
