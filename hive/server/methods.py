from hive.db.methods import (
    get_followers,
    get_following,
)


def api_get_followers(db, bottle, app, params):
    _ = bottle, app
    return get_followers(
        account=params.get('account'),
        skip=params.get('skip'),
        limit=params.get('limit'),
        db=db,
    )


def api_get_following(db, bottle, app, params):
    _ = bottle, app
    return get_following(
        account=params.get('account'),
        skip=params.get('skip'),
        limit=params.get('limit'),
        db=db,
    )
