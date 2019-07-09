"""[WIP] Process community ops."""

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
    def get_user_role(cls, community, account):
        """Get user role within a specific community."""
        if account == community:
            return ROLE_OWNER

        return DB.query_one("""SELECT role_id FROM hive_roles
                                WHERE community = :community
                                  AND account = :account
                                LIMIT 1""") or ROLE_GUEST

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

        if type_id == TYPE_JOURNAL:
            if not comment_op['parent_author']:
                return role >= ROLE_MEMBER
        elif type_id == TYPE_COUNCIL:
            return role >= ROLE_MEMBER
        return role >= ROLE_GUEST



def is_permitted(account: str, community: str, action: str, op: dict) -> bool:
    """ Check if an account is allowed to perform an action in a given community."""
    #pylint: disable=too-many-return-statements,too-many-branches
    role = Community.get_user_role(community, account)

    if action == 'setRole':
        if role < ROLE_MOD:
            return False
        assert op['role'] in ROLES.keys()
        new_role = ROLES[op['role']]
        if account == op['account']:
            return new_role < role # demote self
        else:
            other_role = Community.get_user_role(community, op['account'])
            if role <= other_role:
                return False # cannot demote at or above rank
            return True

    if action == 'updateSettings':
        return role >= ROLE_ADMIN
    if action == 'setUserTitle':
        return role >= ROLE_MOD
    if action == 'mutePost':
        return role >= ROLE_MOD
    if action == 'unmutePost':
        return role >= ROLE_MOD
    if action == 'pinPost':
        return role >= ROLE_MOD
    if action == 'unpinPost':
        return role >= ROLE_MOD
    if action == 'flagPost':
        return role >= ROLE_GUEST
    if action == 'subscribe':
        return True
    if action == 'unsubscribe':
        return True

    raise Exception('unhandled action `%s`' % action)

# community methods
# -----------------
def process_json_community_op(account, op_json, date):
    """Validates community op and apply state changes to db."""
    #pylint: disable=line-too-long,unused-variable,too-many-branches
    cmd_name, cmd_op = op_json  # ['flagPost', {community: '', author: '', ...}]

    if (cmd_name not in COMMANDS
            or 'community' not in cmd_op
            or not isinstance(cmd_op['community'], str)
            or not Accounts.exists(cmd_op['community'])
            or not Community.exists(cmd_op['community'])):
        return

    community = cmd_op['community']

    log.warning("community op from %s @ %s -- %s", account, date, op_json)

    # validate permissions
    if not is_permitted(account, community, cmd_name, cmd_op):
        return

    # If command references a post, ensure it's valid
    post_id, depth = Posts.get_id_and_depth(cmd_op.get('author'), cmd_op.get('permlink'))
    if not post_id:
        return

    # If command references an account, ensure it's valid
    account_id = Accounts.get_id(cmd_op.get('account'))



    # Community-level commands
    # ----------------

    if cmd_name == 'updateSettings':
        settings = cmd_op['settings']
        DB.query("UPDATE hive_communities SET settings = :settings WHERE name = :community",
                 community=community, settings=json.dumps(settings))

    if cmd_name == 'subscribe':
        DB.query("INSERT INTO hive_subscriptions (account, community) VALUES (:account, :community)",
                 account=account, community=community)
    if cmd_name == 'unsubscribe':
        DB.query("DELETE FROM hive_subscriptions WHERE account = :account AND community = :community",
                 account=account, community=community)

    # Account-level actions
    # ----------------
    if cmd_name == 'setRole':
        assert account_id
        role_id = ROLES[cmd_op['role']]
        assert role_id
        DB.query("UPDATE hive_roles SET role_id = :role_id WHERE account = :account AND community = :community",
                 role_id=role_id, account=account, community=community)

    if cmd_name == 'setUserTitle':
        assert account_id
        title = cmd_op['title']
        DB.query("UPDATE hive_members SET title = :title WHERE account = :account AND community = :community",
                 title=title, account=account, community=community)


    # MOD POST Actions
    # ----------------
    if cmd_name == 'mutePost':
        assert post_id
        DB.query("UPDATE hive_posts SET is_muted = 1 WHERE id = :id", id=post_id)

    if cmd_name == 'unmutePost':
        assert post_id
        DB.query("UPDATE hive_posts SET is_muted = 0 WHERE id = :id", id=post_id)

    if cmd_name == 'pinPost':
        assert post_id
        DB.query("UPDATE hive_posts SET is_pinned = 1 WHERE id = :id", id=post_id)

    if cmd_name == 'unpinPost':
        assert post_id
        DB.query("UPDATE hive_posts SET is_pinned = 0 WHERE id = :id", id=post_id)

    # GUEST POST Actions
    # ------------------
    if cmd_name == 'flagPost':
        assert post_id
        DB.query("INSERT INTO hive_flags (account, community, author, permlink, comment, created_at) VALUES (:account, :community, :author, :permlink, :comment, :date)")

    # track success (TODO: failures as well?)
    # INSERT INTO hive_modlog (account, community, action, created_at) VALUES  (account, community, json.inspect, block_date)
    return True
