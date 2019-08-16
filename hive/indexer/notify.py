"""Handle notifications"""

from enum import IntEnum
import logging
#from hive.db.adapter import Db
#pylint: disable=too-many-lines,line-too-long

log = logging.getLogger(__name__)
#DB = Db.instance()

class NotifyType(IntEnum):
    """Labels for notify `type_id` field."""
    new_community = 1   # <src> created <community>
    set_role = 2        # <src> set <dst> <payload>
    set_props = 3       # <src> set properties <payload>
    set_label = 4       # <src> label <dst> <payload>
    mute_post = 5       # <src> mute <post> <payload>
    unmute_post = 6     # <src> unmute <post> <payload>
    pin_post = 7        # <src> pin <post>
    unpin_post = 8      # <src> unpin <post>
    flag_post = 9       # <src> flag <post>
    error = 10          # error: <payload>

    resteem = 11        # <src> resteemed <post>
    mention = 12        # <post> mentioned <dst>
    follow = 13         # <src> followed <dst>

    vote_post = 14      # <src> voted on <post>
    vote_comment = 15   # <src> voted on <post>
    reply_post = 16     # <src> replied to <post>  (?) child post or parent post
    reply_comment = 17  # <src> replied to <post>

    update_account = 18 # <dst> updated account
    receive = 19        # <src> sent <dst> <payload>
    send = 20           # <dst> sent <src> <payload>

#   reward = 21         # <post> rewarded <payload>
#   power_up = 22       # <dst> power up <payload>
#   power_down = 23     # <dst> power down <payload>
#   message = 99        # <src>: <payload>


#                                      agg-cols
# case 1: src     comm payload                           update_settings, new_community
# case 2: src(dst)comm payload         dst,comm          set_role, set_title
# case 3: src     comm payload post                      mute, pin, flag
# case 4: src dst              post    dst,post          resteem, mention
# case 5: src dst                      dst               follow
# case 6: src dst      payload post    dst               vote, reply   (optional: payload?)
# case 7: src dst      payload         dst               send, receive
# case 8:     dst      payload                           error, update_account
# case 9:     dst      payload post    dst               reward

class Notify:
    """Handles writing notifications/messages."""
    # pylint: disable=too-many-instance-attributes,too-many-arguments
    DEFAULT_SCORE = 35

    def __init__(self, type_id, when=None, src_id=None, dst_id=None, community_id=None,
                 post_id=None, payload=None, score=None, **kwargs):
        """Create a notification."""

        assert type_id, 'op is blank :('
        if isinstance(type_id, str):
            enum = NotifyType[type_id]
        elif isinstance(type_id, int):
            enum = NotifyType(type_id)
        else:
            raise Exception("unknown type %s" % repr(type_id))

        self.enum = enum
        self.score = score or self.DEFAULT_SCORE
        self.when = when
        self.src_id = src_id
        self.dst_id = dst_id
        self.post_id = post_id
        self.community_id = community_id
        self.payload = payload
        self._id = kwargs.get('id')

    @classmethod
    def from_dict(cls, row):
        """Instantiate from db row."""
        return Notify(**dict(row))

    def to_dict(self):
        """Generate a db row."""
        return dict(
            type_id=self.enum.value,
            score=self.score,
            when=self.when,
            src_id=self.src_id,
            dst_id=self.dst_id,
            post_id=self.post_id,
            community_id=self.community_id,
            payload=self.payload,
            id=self._id)

    def write(self):
        """Store this notification."""
        assert not self._id, 'notify has id %d' % self._id
        # TODO: write to db
        if self.enum == NotifyType.error:
            log.warning("notify --> %s", vars(self))
        else:
            log.info("notify --> %s", vars(self))
