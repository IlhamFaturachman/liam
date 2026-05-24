"""
Google OAuth consent screen automation + CAPTCHA detection
"""

from playwright.async_api import Page

from config import BROWSER, TIMEOUTS
from utils.delay import random_delay


async def detect_captcha(page: Page) -> bool:
    """
    Check if Google is showing a CAPTCHA challenge.
    Returns True if CAPTCHA detected.
    """
    captcha_selectors = [
        'iframe[src*="recaptcha"]',
        'iframe[src*="captcha"]',
        '#captcha',
        'img[alt*="CAPTCHA"]',
        'div[data-sitekey]',
        'iframe[title*="challenge"]',
        'iframe[title*="recaptcha"]',
        '#recaptcha',
    ]

    for selector in captcha_selectors:
        try:
            if await page.locator(selector).is_visible(timeout=1500):
                return True
        except Exception:
            continue

    return False


async def handle_consent(page: Page):
    """
    Handle Google OAuth consent screen.
    Clicks "Allow" or equivalent button.
    Raises CaptchaError if CAPTCHA detected.
    Raises ConsentError if consent handling fails.
    """
    # First check for CAPTCHA
    if await detect_captcha(page):
        raise CaptchaError("CAPTCHA detected on consent page")

    await random_delay(*BROWSER["post_login_delay"])

    # Google consent screen has multiple possible layouts/languages
    # Try various button selectors in order of likelihood
    allow_selectors = [
        # English
        'button:has-text("Allow")',
        'button:has-text("Continue")',
        '#submit_approve_access',
        'button[data-idom-class*="grant"]',
        # Indonesian
        'button:has-text("Izinkan")',
        'button:has-text("Lanjutkan")',
        # Other languages
        'button:has-text("Zulassen")',
        'button:has-text("Autoriser")',
        'button:has-text("Permitir")',
        # Workspace "I understand" button (input[type="submit"], not <button>)
        # This is a fallback in case edge_cases didn't catch it
        'input[name="confirm"][value="I understand"]',
        'input#confirm[type="submit"]',
    ]

    # Try to find and click the allow button
    for selector in allow_selectors:
        try:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=3000):
                await random_delay(*BROWSER["pre_click_delay"])
                await btn.click()

                # Wait for redirect after consent
                try:
                    await page.wait_for_url(
                        lambda url: "code=" in url or "error=" in url,
                        timeout=TIMEOUTS["callback"],
                    )
                except Exception:
                    # Might need a second click (some flows have 2 steps)
                    pass

                return
        except Exception:
            continue

    # Check if there's a "Select account" page first
    try:
        # If multiple accounts shown, this shouldn't happen in our flow
        # but handle gracefully
        account_btn = page.locator('[data-identifier]').first
        if await account_btn.is_visible(timeout=2000):
            await account_btn.click()
            await random_delay(1000, 2000)
            # Retry consent after account selection
            return await handle_consent(page)
    except Exception:
        pass

    # Check if we're already redirected (consent was auto-approved)
    current_url = page.url
    if "code=" in current_url or "callback" in current_url:
        return  # Already redirected, no consent needed

    # Check for CAPTCHA one more time
    if await detect_captcha(page):
        raise CaptchaError("CAPTCHA detected after consent attempt")

    # If nothing worked, wait a bit and check if redirect happens
    try:
        await page.wait_for_url(
            lambda url: "code=" in url or "error=" in url,
            timeout=TIMEOUTS["consent"],
        )
        return
    except Exception:
        raise ConsentError("Could not find or click consent button")


class CaptchaError(Exception):
    """CAPTCHA detected during automation"""
    pass


class ConsentError(Exception):
    """Failed to handle consent screen"""
    pass
