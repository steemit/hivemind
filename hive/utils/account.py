"""Methods for normalizing/sanitizing steemd account metadata."""

import json
import re

from hive.utils.normalize import trunc

def safe_profile_metadata(account):
    """Given an account, return sanitized profile data."""
    prof = {}
    try:
        prof = json.loads(account['json_metadata'])['profile']
        if not isinstance(prof, dict):
            prof = {}
    except Exception:
        pass

    name = str(prof['name']) if 'name' in prof else None
    about = str(prof['about']) if 'about' in prof else None
    location = str(prof['location']) if 'location' in prof else None
    website = str(prof['website']) if 'website' in prof else None
    profile_image = str(prof['profile_image']) if 'profile_image' in prof else None
    cover_image = str(prof['cover_image']) if 'cover_image' in prof else None

    name = _char_police(name)
    about = _char_police(about)
    location = _char_police(location)

    name = trunc(name, 20)
    about = trunc(about, 160)
    location = trunc(location, 30)

    if name and name[0:1] == '@':
        name = None
    if website and len(website) > 100:
        website = None
    if website and not re.match('^https?://', website):
        website = 'http://' + website

    if profile_image and not re.match('^https?://', profile_image):
        profile_image = None
    if cover_image and not re.match('^https?://', cover_image):
        cover_image = None
    if profile_image and len(profile_image) > 1024:
        profile_image = None
    if cover_image and len(cover_image) > 1024:
        cover_image = None

    return dict(
        name=name or '',
        about=about or '',
        location=location or '',
        website=website or '',
        profile_image=profile_image or '',
        cover_image=cover_image or '',
    )

def _char_police(string):
    """If a string has bad chars, ignore it.

    Unclear how a NUL would get in profile data,
    but Postgres does not allow them in strings.
    """
    if not string:
        return None
    if string.find('\x00') > -1:
        print("bad string: {}".format(string))
        return None
    return string
