"""System-specific utility methods"""

import sys
import resource

USE_COLOR = hasattr(sys.stdout, 'isatty') and sys.stdout.isatty()

def colorize(string, color='93'):
    if not USE_COLOR:
        return string
    return "\033[%sm%s\033[0m" % (color, string)

def peak_usage_mb():
    mem_denom = (1024 * 1024) if sys.platform == 'darwin' else 1024
    max_mem = int(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss)
    return max_mem / mem_denom
