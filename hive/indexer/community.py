"""[WIP] Process community ops."""

#pylint: disable=too-many-lines

import logging
import re
import ujson as json

from hive.db.adapter import Db
from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts

log = logging.getLogger(__name__)

DB = Db.instance()

ROLES = {'owner': 8, 'admin': 6, 'mod': 4, 'member': 2, 'guest': 0, 'muted': -2}
ROLE_OWNER = ROLES['owner']
ROLE_ADMIN = ROLES['admin']
ROLE_MOD = ROLES['mod']
ROLE_MEMBER = ROLES['member']
ROLE_GUEST = ROLES['guest']
ROLE_MUTED = ROLES['muted']

TYPE_TOPIC = 1
TYPE_JOURNAL = 2
TYPE_COUNCIL = 3

COMMANDS = [
    # community
    'updateSettings', 'subscribe', 'unsubscribe',
    # community+account
    'setRole', 'setUserTitle',
    # community+account+permlink
    'mutePost', 'unmutePost', 'pinPost', 'unpinPost', 'flagPost',
]

class Community:
    """Handles hive community registration and operations."""

    @classmethod
    def register(cls, names, block_date):
        """Block processing: hooks into new account registration.

        `Accounts` calls this method with any newly registered names.
        This method checks for any valid community names and inserts them.
        """

        for name in names:
            if not re.match(r'^hive-[123]\d{4,6}$', name):
                continue
            type_id = int(name[5])

            sql = """INSERT INTO hive_communities (name, title, settings,
                                                   type_id, created_at)
                          VALUES (:name, '', '{}', :type_id, :date)"""
            DB.query(sql, name=name, type_id=type_id, date=block_date)
            sql = """INSERT INTO hive_roles (community, account, role_id, created_at)
                         VALUES (:community, :account, :role_id, :date)"""
            DB.query(sql, community=name, account=name, role_id=ROLE_OWNER, date=block_date)

    @classmethod
    def exists(cls, name):
        """Check if a given community name exists."""
        sql = "SELECT 1 FROM hive_communities WHERE name = :name"
        return bool(DB.query_one(sql, name=name))

    @classmethod
    def get_id(cls, name):
        """Given a community name, get its internal id."""
        sql = "SELECT id FROM hive_communities WHERE name = :name"
        return DB.query_one(sql, name=name)

    @classmethod
    def get_user_role(cls, community, account):
        """Get user role within a specific community."""
        return DB.query_one("""SELECT role_id FROM hive_roles
                                WHERE community = :community
                                  AND account = :account
                                LIMIT 1""",
                            community=community,
                            account=account) or ROLE_GUEST

    @classmethod
    def is_post_valid(cls, community, comment_op: dict):
        """ Given a new post/comment, check if valid as per community rules

        For a comment to be valid, these conditions apply:
            - Author is not muted in this community
            - For council post/comment, author must be a member
            - For journal post, author must be a member
        """

        role = cls.get_user_role(community, comment_op['author'])
        type_id = int(community[5])

        # TODO: (1.5) check that beneficiaries are valid

        if type_id == TYPE_JOURNAL:
            if not comment_op['parent_author']:
                return role >= ROLE_MEMBER
        elif type_id == TYPE_COUNCIL:
            return role >= ROLE_MEMBER
        return role >= ROLE_GUEST

def read_key_str(op, key):
    """Reads a key from a dict, ensuring non-blank str if present."""
    if key in op:
        assert isinstance(op[key], str), 'key `%s` was not str' % key
        assert op[key], 'key `%s` was blank' % key
        return op[key]
    return None

# community methods
# -----------------
def process_json_community_op(actor, op_json, date):
    """Validates community op and apply state changes to db."""
    #pylint: disable=line-too-long,unused-variable,too-many-branches,too-many-locals,too-many-statements

    action, op = op_json  # ['flagPost', {community: '', author: '', ...}]
    actor_id = Accounts.get_id(actor)

    # validate operation
    assert action in COMMANDS, 'invalid op: `%s`' % action

    # validate community
    community = read_key_str(op, 'community')
    assert community, 'must name a community'
    assert Accounts.exists(community), 'invalid name `%s`' % community
    community_id = Community.get_id(community)
    assert community_id, 'community `%s` does not exist' % community

    # get actor's role
    actor_role = Community.get_user_role(community, actor)

    # if present: validate account
    account = read_key_str(op, 'account')
    account_id = None
    if account:
        assert Accounts.exists(account), 'account `%s` not found' % account
        account_id = Accounts.get_id(account)

    # if present: validate permlink
    permlink = read_key_str(op, 'permlink')
    post_id = None
    depth = None
    if permlink:
        assert account, 'permlink requires named account'
        post_id, depth = Posts.get_id_and_depth(account, permlink)
        assert post_id, 'invalid post: %s/%s' % (account, permlink)
        # TODO: assert post belongs to community

    role = read_key_str(op, 'role')
    new_role = None
    if role:
        assert role in ROLES, 'invalid role'
        new_role = ROLES[role]

    # validate permissions
    if action == 'setRole':
        assert actor_role >= ROLE_MOD, 'only mods and up can alter roles'
        assert actor_role > new_role, 'cannot promote to or above own rank'

        if actor != account:
            account_role = Community.get_user_role(community, account)
            assert account_role < actor_role, 'cannot modify higher-role user'
            assert account_role != new_role, 'user is already `%s`' % op['role']

    if action == 'updateSettings':
        assert actor_role >= ROLE_ADMIN, 'only mods can update settings'
    if action == 'setUserTitle':
        assert actor_role >= ROLE_MOD, 'only mods can set user titles'
    if action == 'mutePost':
        assert actor_role >= ROLE_MOD, 'only mods can mute posts'
    if action == 'unmutePost':
        assert actor_role >= ROLE_MOD, 'only mods can unmute posts'
    if action == 'pinPost':
        assert actor_role >= ROLE_MOD, 'only mods can pin posts'
    if action == 'unpinPost':
        assert actor_role >= ROLE_MOD, 'only mods can unpin posts'
    if action == 'flagPost':
        assert actor_role > ROLE_MUTED, 'muted users cannot flag posts'
    if action == 'subscribe':
        pass
    if action == 'unsubscribe':
        pass




    log.warning("valid community op from %s @ %s -- %s", actor, date, op_json)

    # Community-level commands
    # ----------------

    if action == 'updateSettings':
        settings = op['settings']
        DB.query("UPDATE hive_communities SET settings = :settings WHERE name = :community",
                 community=community, settings=json.dumps(settings))

    if action == 'subscribe':
        DB.query("INSERT INTO hive_subscriptions (account, community) VALUES (:account, :community)",
                 account=actor, community=community)

    if action == 'unsubscribe':
        DB.query("DELETE FROM hive_subscriptions WHERE account = :account AND community = :community",
                 account=actor, community=community)

    # Account-level actions
    # ----------------
    if action == 'setRole':
        assert account_id
        role = read_key_str(op, 'role')
        assert role, 'no role specified'
        role_id = ROLES[role]
        assert role_id, 'invalid role `%s`' % role
        DB.query("UPDATE hive_roles SET role_id = :role_id WHERE account = :account AND community = :community",
                 role_id=role_id, account=account, community=community)

    if action == 'setUserTitle':
        assert account_id
        title = read_key_str(op, 'title')
        DB.query("UPDATE hive_roles SET title = :title WHERE account = :account AND community = :community",
                 title=title, account=account, community=community)


    # MOD POST Actions
    # ----------------
    if action == 'mutePost':
        assert post_id, 'no post specified'
        DB.query("UPDATE hive_posts SET is_muted = 1 WHERE id = :id", id=post_id)

    if action == 'unmutePost':
        assert post_id, 'no post specified'
        DB.query("UPDATE hive_posts SET is_muted = 0 WHERE id = :id", id=post_id)

    if action == 'pinPost':
        assert post_id, 'no post specified'
        DB.query("UPDATE hive_posts SET is_pinned = 1 WHERE id = :id", id=post_id)

    if action == 'unpinPost':
        assert post_id, 'no post specified'
        DB.query("UPDATE hive_posts SET is_pinned = 0 WHERE id = :id", id=post_id)

    # GUEST POST Actions
    # ------------------
    if action == 'flagPost':
        assert post_id, 'no post specified'
        DB.query("INSERT INTO hive_flags (account, community, author, permlink, comment, created_at) VALUES (:account, :community, :author, :permlink, :comment, :date)")

    # track success (TODO: failures as well?)
    # INSERT INTO hive_modlog (account, community, action, created_at) VALUES  (account, community, json.inspect, block_date)
    return True
