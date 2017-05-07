# -*- coding: utf-8 -*-
import logging
from functools import singledispatch

import funcy

logger = logging.getLogger(__name__)


@funcy.log_calls(logger.debug, errors=True)
@singledispatch
def parse_params(params):
    if not params:
        return
    else:
        raise ValueError('params must be dict or list')
