"""
Generic Worker - handles the full flow for a single account using provider adapters
"""

import asyncio
import os
import re
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

# Max interstitial pages to handle per loop
MAX_INTERSTITIALS = 8

# Debug step counter (reset per account)
_step = 0


def _debug_dump(html: str, name: str):
    """Save HTML to results/ for debugging."""
    try:
        os.makedirs("results", exist_ok=True)
        with open(f"results/debug_{name}.html", "w", encoding="utf-8") as f:
            f.write(html)
    except Exception:
        pass


async def _dump(page, step: str, email: str, status_cb=None):
    """Dump HTML + screenshot at current step."""
    global _step
    _step += 1
    safe = email.replace("@", "_at_").replace(".", "_")
    prefix = f"{_step:02d}_{step}"
    try:
        html = await page.content()
        _debug_dump(html, f"{prefix}_{safe}")
        title_m = re.search(r'<title[^>]*>(.*?)</title>', html, re.S)
        title = title_m.group(1).strip()[:50] if title_m else "N/A"
        if status_cb:
            status_cb(f"  [{prefix}] url={page.url[:70]} title={title}")
    except Exception:
        pass
    try:
        await page.screenshot(path=f"results/debug_{prefix}_{safe}.png", full_page=True)
    except Exception:
        pass


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

            if provider.handles_own_browser_phase:
                # Provider handles the entire browser flow itself
                # (e.g. Pioneer: login page → Google SSO → dashboard → create API key)
                status_cb("Launching browser...")
                result = await _custom_browser_phase(account, provider, headless, proxy, status_cb)
            else:
                # Standard flow: Google OAuth → consent → callback → extract code
                # (e.g. Antigravity: build auth URL → Google login → code= redirect)
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
    """Browser automation phase: login + consent + provider-specific flow.
    
    Debug dumps HTML+screenshot at EVERY step so we can see exactly what
    Camoufox sees at each point.
    """
    global _step
    _step = 0

    email = account["email"]
    password = account["password"]

    # Build auth URL
    state = secrets.token_urlsafe(32)
    auth_url = provider.build_auth_url(state)

    async with BrowserSession(headless=headless, proxy=proxy) as (browser, page):
        # =====================================================================
        # INTERCEPT: Route handler to catch localhost callback redirect.
        # Google redirects to localhost:8000/oauth/callback?code=XXX after
        # consent. Browser can't resolve localhost in Camoufox, so we
        # intercept the request BEFORE it hits the network, capture the URL,
        # and abort the navigation (we only need the code= param).
        # =====================================================================
        captured_callback_url = {"url": None}

        async def intercept_callback(route):
            url = route.request.url
            if "code=" in url or "error=" in url:
                captured_callback_url["url"] = url
                # Respond with a simple page instead of letting browser fail
                await route.fulfill(
                    status=200,
                    content_type="text/html",
                    body="<html><body><h1>OK - Code captured</h1></body></html>",
                )
            else:
                await route.continue_()

        # Intercept ALL localhost requests (any port)
        await page.route("**/localhost:*/**", intercept_callback)
        await page.route("**/127.0.0.1:*/**", intercept_callback)

        # =====================================================================
        # STEP 1: Navigate to OAuth URL
        # =====================================================================
        status_cb("Step 1: Navigating to OAuth...")
        await page.goto(auth_url, wait_until="networkidle")
        await _dump(page, "oauth_page", email, status_cb)

        # CAPTCHA check
        if await detect_captcha(page):
            raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA on login page")

        # Clear any initial interstitials (TOS, account picker, etc.)
        # Loop because TOS can appear multiple times
        for _i in range(MAX_INTERSTITIALS):
            edge = await handle_all_edge_cases(page)
            if edge == "verify_challenge":
                raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge detected")
            elif edge != "none":
                status_cb(f"  Cleared: {edge}")
                await random_delay(1000, 2000)
                try:
                    await page.wait_for_load_state("networkidle", timeout=10000)
                except Exception:
                    pass
                continue
            break

        await _dump(page, "after_initial_clear", email, status_cb)

        # =====================================================================
        # STEP 2: Google login
        # =====================================================================
        if provider.needs_google_login:
            status_cb("Step 2: Google login...")
            await google_login(page, email, password)
            await _dump(page, "after_google_login", email, status_cb)

            # =================================================================
            # STEP 3: Post-login interstitial clearing
            # TOS ("Before you continue" / "I agree") appears HERE after login.
            # Loop aggressively until we reach callback or consent screen.
            # =================================================================
            status_cb("Step 3: Post-login interstitials...")

            for i in range(MAX_INTERSTITIALS):
                # Already got callback?
                if _is_callback_url(page.url):
                    status_cb("Auto-approved, got callback!")
                    break

                await _dump(page, f"post_login_{i}", email, status_cb)

                # CAPTCHA
                if await detect_captcha(page):
                    raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA after login")

                # Edge cases (TOS, workspace welcome, etc.)
                edge = await handle_all_edge_cases(page)
                if edge == "verify_challenge":
                    await page.screenshot(path=f"results/verify_{email}.png", full_page=True)
                    raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge after login")
                elif edge != "none":
                    status_cb(f"  Cleared: {edge}")
                    await random_delay(1000, 2000)
                    try:
                        await page.wait_for_load_state("networkidle", timeout=10000)
                    except Exception:
                        pass
                    continue

                # Check if we're on consent screen (has Allow/Continue button)
                # If so, break to handle consent below
                if "accounts.google.com" in page.url:
                    # Might be consent screen — check for Allow button
                    try:
                        allow = page.locator(
                            'button:has-text("Allow"), '
                            'button:has-text("Continue"), '
                            'button:has-text("Izinkan"), '
                            'button:has-text("Lanjutkan"), '
                            '#submit_approve_access'
                        ).first
                        if await allow.is_visible(timeout=2000):
                            status_cb("  Found consent screen")
                            break
                    except Exception:
                        pass

                # Nothing to clear, wait a bit for redirect
                await asyncio.sleep(2)
                break

            # =================================================================
            # STEP 4: Handle OAuth consent screen
            # =================================================================
            if not _is_callback_url(page.url):
                status_cb("Step 4: Handling consent...")
                await _dump(page, "before_consent", email, status_cb)
                try:
                    await handle_consent(page)
                except (CaptchaError, ConsentError):
                    raise
                except Exception as e:
                    # Consent might have auto-approved during TOS clearing
                    if not _is_callback_url(page.url):
                        await _dump(page, "consent_failed", email, status_cb)
                        raise
                await _dump(page, "after_consent", email, status_cb)

        # =====================================================================
        # STEP 5: Wait for callback (intercepted by route handler)
        # =====================================================================
        status_cb("Step 5: Waiting for callback...")

        # Check if already captured by route interceptor
        if not captured_callback_url["url"] and not _is_callback_url(page.url):
            # Wait for either: URL contains code=, or interceptor captured it
            for _wait in range(30):  # max 30 seconds
                if captured_callback_url["url"]:
                    break
                if _is_callback_url(page.url):
                    captured_callback_url["url"] = page.url
                    break
                await asyncio.sleep(1)

        # Use intercepted URL if available, otherwise fall back to page URL
        callback_url = captured_callback_url["url"] or page.url

        if "code=" not in callback_url:
            await _dump(page, "callback_timeout", email, status_cb)
            await page.screenshot(path=f"results/timeout_{email}.png", full_page=True)
            raise BatchLoginError(
                ErrorCode.TIMEOUT,
                f"Timeout waiting for callback (stuck at: {page.url})"
            )

        status_cb(f"Got callback: ...{callback_url[callback_url.index('code='):callback_url.index('code=')+20]}...")
        await _dump(page, "got_callback", email, status_cb)

        # =====================================================================
        # STEP 6: Extract credentials — use the captured callback URL
        # Override page.url with captured URL for provider.browser_flow()
        # =====================================================================
        status_cb("Step 6: Extracting credentials...")

        # Monkey-patch a simple object so browser_flow can read the callback URL
        class _FakePage:
            def __init__(self, url):
                self.url = url
        
        fake_page = _FakePage(callback_url)
        intermediate = await provider.browser_flow(fake_page, account)

    return intermediate


def _is_callback_url(url: str) -> bool:
    """Check if the URL is a callback redirect (contains code= or error=)"""
    return "code=" in url or "error=" in url


async def _custom_browser_phase(
    account: dict,
    provider: ProviderAdapter,
    headless: bool,
    proxy: str,
    status_cb,
) -> HarvestResult:
    """
    Custom browser phase for providers that handle everything themselves.
    Launches browser, navigates to auth URL, then delegates to
    provider.full_browser_flow() which returns a complete HarvestResult.
    """
    auth_url = provider.build_auth_url(secrets.token_urlsafe(32))

    async with BrowserSession(headless=headless, proxy=proxy) as (browser, page):
        status_cb("Navigating to login...")
        await page.goto(auth_url, wait_until="networkidle")

        # CAPTCHA check on initial page
        if await detect_captcha(page):
            raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA on login page")

        # Handle edge cases in a loop — Google TOS can appear multiple times
        # (e.g. "Before you continue" page before even reaching the provider's
        # login page, especially with Camoufox geoip randomization).
        MAX_INITIAL_INTERSTITIALS = 5
        for _attempt in range(MAX_INITIAL_INTERSTITIALS):
            edge = await handle_all_edge_cases(page)
            if edge == "verify_challenge":
                raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge detected")
            elif edge != "none":
                status_cb(f"Handled: {edge}")
                await asyncio.sleep(1)
                try:
                    await page.wait_for_load_state("networkidle", timeout=10000)
                except Exception:
                    pass
                continue
            break

        # Delegate everything to the provider
        result = await provider.full_browser_flow(page, account, status_cb)

    return result
