"""
Camoufox browser launch helper
"""

import random

from camoufox.async_api import AsyncCamoufox
from utils.delay import random_viewport


# Camoufox can mimic any of these OSes; randomising per-session means batches
# don't all show the same UA fingerprint to Google. We weight Mac slightly
# higher because the Antigravity IDE itself ships on macOS too — flat 50/50
# would tip the population balance vs real users in a way that's easy to flag.
_OS_POOL = ["windows", "windows", "macos", "linux"]


class BrowserSession:
    """Context manager for a Camoufox browser session.

    Hardening notes:
      - block_webrtc=True. WebRTC's STUN handshake leaks the local LAN IP
        even when the page is routed through a SOCKS proxy. Google's
        reCAPTCHA + bot-detect look at this directly: a "US residential"
        proxy IP paired with a 10.x.x.x or 192.168.x.x leak is one of
        the cleanest "this is automation" signals on the public web.
      - humanize=True. Camoufox simulates curved cursor paths between
        clicks instead of teleporting. Combined with type_human_like()
        per-keystroke delays, the trace shape stops matching the
        synthetic-Playwright pattern Google trains against.
      - randomized OS per session. Spreading UAs across the pool means
        a 200-account batch doesn't show as 200 identical UA strings
        from one IP, which is otherwise the loudest tell.
      - geoip=True keeps Accept-Language + timezone consistent with the
        proxy's IP geo, which is what real Chrome does.
    """

    def __init__(self, headless: bool = False, proxy: str = None):
        self.headless = headless
        self.proxy = proxy
        self._context_manager = None
        self.browser = None
        self.page = None

    async def __aenter__(self):
        kwargs = {
            "headless": self.headless,
            "geoip": True,
            "humanize": True,
            "block_webrtc": True,
            "os": random.choice(_OS_POOL),
        }

        if self.proxy:
            kwargs["proxy"] = {"server": self.proxy}

        # AsyncCamoufox is itself an async context manager
        self._context_manager = AsyncCamoufox(**kwargs)
        self.browser = await self._context_manager.__aenter__()

        self.page = await self.browser.new_page()

        # Set random viewport
        viewport = random_viewport()
        await self.page.set_viewport_size(viewport)

        return self.browser, self.page

    async def __aexit__(self, exc_type, exc_val, exc_tb):
        if self.page:
            try:
                await self.page.close()
            except Exception:
                pass
        if self._context_manager:
            try:
                await self._context_manager.__aexit__(exc_type, exc_val, exc_tb)
            except Exception:
                pass
        return False
