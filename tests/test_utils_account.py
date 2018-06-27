import json

from hive.utils.account import safe_profile_metadata

def test_valid_account():
    raw_profile = dict(
        name='Leonardo Da Vinci',
        about='Renaissance man, vegetarian, inventor of the helicopter in 1512 and painter of the Mona Lisa.',
        location='Florence',
        website='http://www.davincilife.com/',
        cover_image='https://steemitimages.com/0x0/https://pbs.twimg.com/profile_banners/816255358066946050/1483447009/1500x500',
        profile_image='https://www.parhlo.com/wp-content/uploads/2016/01/tmp617041537745813506.jpg',
    )
    account = {'name': 'foo', 'json_metadata': json.dumps(dict(profile=raw_profile))}

    safe_profile = safe_profile_metadata(account)
    for key, safe_value in safe_profile.items():
        assert raw_profile[key] == safe_value

def test_invalid_account():
    raw_profile = dict(
        name='NameIsTooBigByOneChar',
        location='Florence\x00',
        website='davincilife.com/',
        cover_image='example.com/avatar.jpg',
        profile_image='https://example.com/valid-url-but-longer-than-1024-chars' + 'x' * 1024,
    )
    account = {'name': 'foo', 'json_metadata': json.dumps(dict(profile=raw_profile))}

    safe_profile = safe_profile_metadata(account)
    assert safe_profile['name'] == 'NameIsTooBigByOne...'
    assert safe_profile['about'] == ''
    assert safe_profile['location'] == ''
    assert safe_profile['website'] == 'http://davincilife.com/' # TODO: should normalize to https?
    assert safe_profile['cover_image'] == ''
    assert safe_profile['profile_image'] == ''
