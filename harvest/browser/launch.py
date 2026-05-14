"""
Camoufox browser launch helper
"""

from camoufox.async_api import AsyncCamoufox
from utils.delay import random_viewport


class BrowserSession:
    """Context manager for a Camoufox browser session"""

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
