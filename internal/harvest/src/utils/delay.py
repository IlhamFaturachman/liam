"""
Human-like random delays for anti-detection
"""

import asyncio
import random


async def random_delay(min_ms: int, max_ms: int):
    """Sleep for a random duration between min_ms and max_ms milliseconds"""
    delay = random.randint(min_ms, max_ms) / 1000.0
    await asyncio.sleep(delay)


async def type_human_like(page, locator, text: str, delay_range: tuple = (50, 150)):
    """Type text with human-like delays between keystrokes"""
    # Playwright Python uses `type` with delay parameter (ms between keystrokes)
    avg_delay = (delay_range[0] + delay_range[1]) // 2
    await locator.type(text, delay=avg_delay)


def random_viewport() -> dict:
    """Generate a slightly randomized viewport size"""
    width = 1280 + random.randint(-100, 200)
    height = 800 + random.randint(-50, 100)
    return {"width": width, "height": height}
