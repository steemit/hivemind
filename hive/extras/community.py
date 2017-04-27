import json
from typing import Union

from hive.extras.roles import permissions
from steem import Steem
from steem.account import Account
from steembase.operations import CustomJson


class Community:
    """ Community is an extension of steem.commit.Commit class. It allows you to construct
    various community related `custom_json` operations, and commit them onto the blockchain.

    Args:
        community_name (str): Community to operate in.
        account_name (str): Account to perform actions with.
        steem_instance (Steem): steem.Steem instance. If empty, a default Steem instance will be created on the fly.
        All arguments passed to Community will be inherited by this new Steem instance as well. (ie. `no_broadcast`).

    Example:
        You can pass arguments to Steem instance trough Community initializer:

        .. code-block:: python

            community = Community('my_community_name', no_broadcast=True)  # simulation mode

    """

    _id = 'com.steemit.community'
    _roles = permissions.keys()
    _valid_settings = ['title', 'about', 'description', 'language', 'is_nsfw']

    def __init__(self, community_name: str, account_name: str, steem_instance: Steem = None, **kwargs):
        self.steem = steem_instance or Steem(**kwargs)
        self.community = community_name
        self.account = account_name

    def create(self, community_type: str = 'public', admins: Union[str, list] = None):
        """ Create a new community.

        This method will upgrade an existing STEEM account into a new community.

        Args:
            community_type: Can be **public** (default) or **restricted**.
            admins: A single username, or a list of users who will be community Admins.
             If left empty, the community owner will be assigned as a single admin. Can be modified later.
        """
        # validate account and community name
        Account(self.account)
        assert self.community == self.account
        # todo: check if community already exists

        if type(admins) == str:
            admins = [admins]
        if not admins:
            admins = [self.community]

        op = self._op(action='create',
                      type=community_type,
                      admins=admins)
        return self._commit(op)

    def update_settings(self, **settings):
        # sanitize the settings to valid keys
        settings = {k: v for k, v in settings.items() if k in self._valid_settings}
        op = self._op(action='update_settings', settings=settings)
        return self._commit(op)

    def add_users(self, account_names: Union[str, list], role: str):
        """ Add user to the community in the specified role.

        Args:
            account_names (str, list): Steem username(s) of the account we are adding to the community.
            role (str): Role we are adding this user as. Can be admin, moderator or poster.

        """
        return self._add_or_remove_users(account_names, role, 'add')

    def remove_users(self, account_names: Union[str, list], role: str):
        """ Opposite of `add_user`. """
        return self._add_or_remove_users(account_names, role, 'remove')

    def _add_or_remove_users(self, account_names: Union[str, list], role: str, action: str):
        """ Implementation for adding/removing users to communities under various roles. """
        if type(account_names) == str:
            account_names = [account_names]

        if role not in self._roles:
            raise ValueError('Invalid role "%s", needs to be either: %s' % (role, ', '.join(self._roles)))

        action_name = '{0}_{1}s'.format(action, role)
        op = self._op(action=action_name, accounts=account_names)
        return self._commit(op)

    def set_user_title(self, account_name: str, title: str):
        """ Set a title for given user. """
        op = self._op(action='set_user_title', account=account_name, title=title)
        return self._commit(op)

    def mute_user(self, account_name: str):
        op = self._op(action='mute_user', account=account_name)
        return self._commit(op)

    def unmute_user(self, account_name: str):
        op = self._op(action='unmute_user', account=account_name)
        return self._commit(op)

    def mute_post(self, author: str, permlink: str, notes: str):
        op = self._op(action='mute_post', author=author, permlink=permlink, notes=notes)
        return self._commit(op)

    def unmute_post(self, author: str, permlink: str, notes: str):
        op = self._op(action='unmute_post', author=author, permlink=permlink, notes=notes)
        return self._commit(op)

    def pin_post(self, author: str, permlink: str):
        op = self._op(action='pin_post', author=author, permlink=permlink)
        return self._commit(op)

    def unpin_post(self, author: str, permlink: str):
        op = self._op(action='unpin_post', author=author, permlink=permlink)
        return self._commit(op)

    def flag_post(self, author: str, permlink: str, comment: str):
        op = self._op(action='flag_post', author=author, permlink=permlink, comment=comment)
        return self._commit(op)

    def _commit(self, community_op: Union[list, str]):
        """ Construct and commit a community *custom_json* operation to the blockchain. """
        if type(community_op) == str:
            community_op = json.loads(community_op)

        op = CustomJson(
            **{'json': community_op,
               'required_auths': [],
               'required_posting_auths': [self.account],
               'id': Community._id})
        return self.steem.commit.finalizeOp(op, self.account, 'posting')

    def _op(self, action: str, **params):
        """ Generate a standard data structure for community *custom_json* operations. """
        return [action, {
            'community': self.community,
            **params
        }]

    def _check_permissions(self, action, account_name):
        """ Check if this account has the right to perform this action within the community.
        Should be called as helper in most methods.
        """
        pass


if __name__ == '__main__':
    pass
