"""Hive API: Notifications"""
import logging

from hive.server.common.helpers import return_error_info
from hive.indexer.notify import NotifyType
from hive.server.hive_api.common import get_account_id, valid_limit

log = logging.getLogger(__name__)

STRINGS = {
    NotifyType.new_community:  '<src> created <community>',
    NotifyType.set_role:       '<src> set <dst> <payload>',
    NotifyType.set_props:      '<src> set properties <payload>',
    NotifyType.set_label:      '<src> label <dst> <payload>',
    NotifyType.mute_post:      '<src> mute <post> <payload>',
    NotifyType.unmute_post:    '<src> unmute <post> <payload>',
    NotifyType.pin_post:       '<src> pin <post>',
    NotifyType.unpin_post:     '<src> unpin <post>',
    NotifyType.flag_post:      '<src> flag <post>',
    NotifyType.error:          '<dst> error: <payload>',

    NotifyType.resteem:        '<src> resteemed <post>',
    NotifyType.mention:        '<post> mentioned <dst>',
    NotifyType.follow:         '<src> followed <dst>',

    NotifyType.vote_post:      '<src> voted on <post>',
    NotifyType.vote_comment:   '<src> voted on <post>',
    NotifyType.reply_post:     '<src> replied to <post>', # `dst` requires parent post?
    NotifyType.reply_comment:  '<src> replied to <post>',

    NotifyType.update_account: '<dst> updated account',
    NotifyType.receive:        '<src> sent <dst> <payload>',
    NotifyType.send:           '<dst> sent <src> <payload>',

    #NotifyType.reward:         '<post> rewarded <payload>',
    #NotifyType.power_up:       '<dst> power up <payload>',
    #NotifyType.power_down:     '<dst> power down <payload>',
    #NotifyType.message:        '<src>: <payload>',
}

@return_error_info
async def account_notifications(context, account, min_score=0, limit=100):
    """Load notifications for named account."""
    db = context['db']
    limit = valid_limit(limit, 100)
    account_id = await get_account_id(db, account)
    sql = """SELECT hn.id, hn.type_id, hn.score, "when",
                    src.name src, dst.name dst,
                    hp.author, hp.permlink, hc.name community,
                    hc.title community_title, payload
               FROM hive_notifs hn
          LEFT JOIN hive_accounts src ON hn.src_id = src.id
          LEFT JOIN hive_accounts dst ON hn.dst_id = dst.id
          LEFT JOIN hive_posts hp ON hn.post_id = hp.id
          LEFT JOIN hive_communities hc ON hn.community_id = hc.id
          WHERE dst_id = :dst_id
            AND score >= :min_score
            AND hn.id > 23
       ORDER BY hn.id DESC
          LIMIT :limit"""
    rows = await db.query_all(sql, min_score=min_score, dst_id=account_id, limit=limit)
    return [_render(row) for row in rows]

def _render(row):
    """Convert object to string rep."""
    # src dst payload community post
    enum = NotifyType(row['type_id'])
    out = {'type': enum.name, 'score': row['score']}
    msg = STRINGS[enum.value]
    if '<src>' in msg:
        msg = msg.replace('<src>', '@' + row['src'])
    if '<dst>' in msg:
        msg = msg.replace('<dst>', '@' + (row['dst'] or '?'))
    if '<community>' in msg:
        msg = msg.replace('<community>', row['community'])
    if '<post>' in msg:
        msg = msg.replace('<post>', '@' + row['author'] + '/' + row['permlink'])
    if '<payload>' in msg:
        msg = msg.replace('<payload>', row['payload'])
    out['msg'] = msg
    return out
