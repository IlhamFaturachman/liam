"""
Google login page automation
"""

from playwright.async_api import Page

from config import BROWSER, TIMEOUTS
from utils.delay import random_delay, type_human_like


async def google_login(page: Page, email: str, password: str):
    """
    Automate Google login (email + password steps).
    Raises LoginError on failure.
    """
    # --- Email Step ---
    email_input = page.locator('input[type="email"]')
    await email_input.wait_for(state="visible", timeout=TIMEOUTS["login"])

    # Human-like delay before typing
    await random_delay(*BROWSER["pre_click_delay"])

    # Type email
    await email_input.click()
    await type_human_like(page, email_input, email, BROWSER["typing_delay"])

    await random_delay(500, 1000)

    # Click Next
    next_btn = page.locator("#identifierNext")
    await next_btn.click()

    # --- Password Step ---
    password_input = page.locator('input[type="password"][name="Passwd"]')
    await password_input.wait_for(state="visible", timeout=TIMEOUTS["login"])

    # Human-like delay
    await random_delay(*BROWSER["post_login_delay"])

    # Type password
    await password_input.click()
    await type_human_like(page, password_input, password, BROWSER["typing_delay"])

    await random_delay(500, 1000)

    # Click Next
    password_next = page.locator("#passwordNext")
    await password_next.click()

    # Wait for navigation (consent, error, or redirect)
    try:
        await page.wait_for_load_state("networkidle", timeout=TIMEOUTS["login"])
    except Exception:
        pass  # May timeout if page is still loading, continue to error checks

    # --- Error Detection ---
    await _check_login_errors(page, email)


async def _check_login_errors(page: Page, email: str):
    """Check for common Google login errors"""

    # Wrong password
    try:
        wrong_password = page.locator(
            'span:has-text("Wrong password"), '
            'span:has-text("The email and password"), '
            'span:has-text("Sandi salah")'
        )
        if await wrong_password.first.is_visible(timeout=2000):
            raise LoginError("WRONG_PASSWORD", f"Invalid password for {email}")
    except LoginError:
        raise
    except Exception:
        pass

    # Account not found
    try:
        not_found = page.locator(
            'span:has-text("Couldn\'t find your Google Account"), '
            'span:has-text("Tidak dapat menemukan")'
        )
        if await not_found.first.is_visible(timeout=1000):
            raise LoginError("ACCOUNT_NOT_FOUND", f"Account not found: {email}")
    except LoginError:
        raise
    except Exception:
        pass

    # Account locked / unusual activity
    try:
        locked = page.locator(
            'span:has-text("unusual activity"), '
            'span:has-text("aktivitas tidak biasa"), '
            'span:has-text("This account has been suspended")'
        )
        if await locked.first.is_visible(timeout=1000):
            raise LoginError("ACCOUNT_LOCKED", f"Account locked/suspended: {email}")
    except LoginError:
        raise
    except Exception:
        pass


class LoginError(Exception):
    """Google login error with error code"""

    def __init__(self, code: str, message: str):
        self.code = code
        self.message = message
        super().__init__(message)
