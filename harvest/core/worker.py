"""
Generic Worker - handles the full flow for a single account using provider adapters
"""

import asyncio
import secrets

from browser.launch import BrowserSession
from browser.google_login import google_login, LoginError
from browser.consent import handle_consent, detect_captcha, CaptchaError, ConsentError
from browser.edge_cases import handle_all_edge_cases
from core.providers.base import ProviderAdapter, HarvestResult
from core.errors import BatchLoginError, ErrorCode, classify_exception
from utils.delay import random_delay

# Retry settings
MAX_RETRIES = 2
BACKOFF_BASE = 2
BACKOFF_CAP = 15


async def process_account(
    account: dict,
    provider: ProviderAdapter,
    worker_id: int,
    headless: bool = False,
    proxy: str = None,
    on_status=None,
) -> HarvestResult:
    """
    Full automation flow for a single account using the given provider adapter.
    Handles: browser launch, Google login, consent, provider-specific flow, post-browser.
    """
    email = account["email"]
    last_error = None

    def status_cb(msg: str):
        if on_status:
            on_status(worker_id, email, msg)

    for attempt in range(MAX_RETRIES + 1):
        try:
            if attempt > 0:
                backoff = min(BACKOFF_BASE ** (attempt + 1), BACKOFF_CAP)
                status_cb(f"Retry {attempt}/{MAX_RETRIES} (waiting {backoff}s)...")
                await asyncio.sleep(backoff)

            # Phase 1: Browser flow
            status_cb("Launching browser...")
            intermediate = await _browser_phase(account, provider, headless, proxy, status_cb)

            # Phase 2: Post-browser (HTTP only)
            status_cb("Post-browser processing...")
            result = await provider.post_browser(intermediate, account)

            status_cb("SUCCESS")
            return result

        except BatchLoginError as e:
            last_error = e
            if not e.retryable or attempt >= MAX_RETRIES:
                raise
            status_cb(f"Error: {e.code.value} (retrying...)")

        except (LoginError, CaptchaError, ConsentError) as e:
            classified = classify_exception(e)
            last_error = classified
            if not classified.retryable or attempt >= MAX_RETRIES:
                raise classified
            status_cb(f"Error: {classified.code.value} (retrying...)")

        except Exception as e:
            classified = classify_exception(e)
            last_error = classified
            if not classified.retryable or attempt >= MAX_RETRIES:
                raise classified
            status_cb(f"Error: {classified.code.value} (retrying...)")

    raise last_error or BatchLoginError(ErrorCode.UNHANDLED, "Max retries exceeded")


async def _browser_phase(
    account: dict,
    provider: ProviderAdapter,
    headless: bool,
    proxy: str,
    status_cb,
) -> dict:
    """Browser automation phase: login + consent + provider-specific flow"""
    email = account["email"]
    password = account["password"]

    # Build auth URL
    state = secrets.token_urlsafe(32)
    auth_url = provider.build_auth_url(state)

    async with BrowserSession(headless=headless, proxy=proxy) as (browser, page):
        # Navigate to auth URL
        status_cb("Navigating to OAuth...")
        await page.goto(auth_url, wait_until="networkidle")

        # CAPTCHA check
        if await detect_captcha(page):
            raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA on login page")

        # Handle edge cases (account picker, etc.)
        edge = await handle_all_edge_cases(page)
        if edge == "verify_challenge":
            raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge detected")
        elif edge != "none":
            status_cb(f"Handled: {edge}")

        # Google login (shared for all google_oauth providers)
        if provider.needs_google_login:
            status_cb("Logging in...")
            await google_login(page, email, password)

            # Post-login edge cases
            edge = await handle_all_edge_cases(page)
            if edge == "verify_challenge":
                raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge after login")
            elif edge != "none":
                status_cb(f"Handled: {edge}")

            # CAPTCHA check after login
            if await detect_captcha(page):
                raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA after login")

            # Consent screen
            status_cb("Handling consent...")
            await handle_consent(page)

        # Wait for callback/redirect
        status_cb("Waiting for callback...")
        try:
            await page.wait_for_url(
                lambda url: "code=" in url or "error=" in url,
                timeout=30000,
            )
        except Exception:
            if "code=" not in page.url:
                raise BatchLoginError(ErrorCode.TIMEOUT, "Timeout waiting for callback")

        # Provider-specific browser flow (extract code, cookies, etc.)
        status_cb("Extracting credentials...")
        intermediate = await provider.browser_flow(page, account)

    return intermediate
