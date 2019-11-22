"""Handle notifications"""

from enum import IntEnum
import logging
from hive.db.adapter import Db
#pylint: disable=too-many-lines,line-too-long

log = logging.getLogger(__name__)
DB = Db.instance()

class NotifyType(IntEnum):
    """Labels for notify `type_id` field."""
    # active
    new_community = 1
    set_role = 2
    set_props = 3
    set_label = 4
    mute_post = 5
    unmute_post = 6
    pin_post = 7
    unpin_post = 8
    flag_post = 9
    error = 10
    subscribe = 11

    reply = 12
    reply_comment = 13
    reblog = 14
    follow = 15
    mention = 16
    vote = 17

    # inactive
    #vote_comment = 16

    #update_account = 19
    #receive = 20
    #send = 21

    #reward = 22
    #power_up = 23
    #power_down = 24
    #message = 25

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
            created_at=self.when,
            src_id=self.src_id,
            dst_id=self.dst_id,
            post_id=self.post_id,
            community_id=self.community_id,
            payload=self.payload,
            id=self._id)

    def write(self):
        """Store this notification."""
        assert not self._id, 'notify has id %d' % self._id
        ignore = ('reply', 'reply_comment', 'reblog', 'follow', 'mention', 'vote')
        if self.enum.name not in ignore:
            log.warning("[NOTIFY] %s - src %s dst %s pid %s%s cid %s (%d/100)",
                        self.enum.name, self.src_id, self.dst_id, self.post_id,
                        ' (%s)' % self.payload if self.payload else '',
                        self.community_id, self.score)
        sql = """INSERT INTO hive_notifs (type_id, score, created_at, src_id,
                                          dst_id, post_id, community_id,
                                          payload)
                      VALUES (:type_id, :score, :created_at, :src_id, :dst_id,
                              :post_id, :community_id, :payload)"""
        DB.query(sql, **self.to_dict())
