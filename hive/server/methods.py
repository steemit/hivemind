from hive.db.methods import (
    get_followers,
    get_following,
    following_count,
    follower_count,
)


# follow plugin
# -------------
def api_get_followers(db, bottle, app, params):
    _ = db, bottle, app
    return get_followers(
        account=params.get('account'),
        skip=params.get('skip'),
        limit=params.get('limit'),
    )


def api_get_following(db, bottle, app, params):
    _ = db, bottle, app
    return get_following(
        account=params.get('account'),
        skip=params.get('skip'),
        limit=params.get('limit'),
    )


def api_get_follow_count(db, bottle, app, params):
    _ = db, bottle, app
    return following_count(params.get('account'))


def api_get_follower_count(db, bottle, app, params):
    _ = db, bottle, app
    return follower_count(params.get('account'))
