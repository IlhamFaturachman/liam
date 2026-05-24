"""
Pioneer Provider Adapter
Google SSO (via Supabase Auth) -> Dashboard -> /api-keys -> Create -> Harvest

Flow:
1. Navigate to agent.pioneer.ai/login
2. Wait for SPA hydration (buttons to render)
3. Click "Continue with Google"
4. Google login page: enter email + password
5. POST-LOGIN: clear ALL interstitials (Google TOS, workspace welcome, consent)
   -- TOS appears AFTER email+password, NOT before
6. Wait for Pioneer dashboard redirect
7. Navigate to /api-keys
8. Create key -> Reveal -> extract

Uses handles_own_browser_phase=True because Pioneer does NOT redirect
to a callback URL with code= param -- it redirects to its own dashboard.
"""

import asyncio
import os
import re
import httpx

from browser.google_login import google_login
from browser.consent import handle_consent, detect_captcha
from browser.edge_cases import handle_all_edge_cases
from core.providers.base import ProviderAdapter, HarvestResult
from core.errors import BatchLoginError, ErrorCode
from utils.delay import random_delay


PIONEER_DASHBOARD = "https://agent.pioneer.ai"
PIONEER_LOGIN = "https://agent.pioneer.ai/login"
PIONEER_API_KEYS_URL = "https://agent.pioneer.ai/api-keys"
PIONEER_API_BASE = "https://api.pioneer.ai"

DASHBOARD_LOAD_TIMEOUT = 45000
MAX_INTERSTITIALS = 10

_step_counter = 0


def _debug_dump(html: str, name: str):
    """Save raw HTML to results/ for debugging."""
    try:
        os.makedirs("results", exist_ok=True)
        path = f"results/debug_{name}.html"
        with open(path, "w", encoding="utf-8") as f:
            f.write(html)
    except Exception:
        pass


async def _dump_step(page, step: str, safe_email: str, log=None):
    """Dump HTML + screenshot at current step for debugging."""
    global _step_counter
    _step_counter += 1
    prefix = f"{_step_counter:02d}_{step}"
    try:
        html = await page.content()
        _debug_dump(html, f"{prefix}_{safe_email}")
        if log:
            title_match = re.search(r'<title[^>]*>(.*?)</title>', html, re.S)
            title = title_match.group(1).strip()[:60] if title_match else "N/A"
            log(f"  [{prefix}] url={page.url[:80]} title={title}")
    except Exception:
        pass
    try:
        await page.screenshot(
            path=f"results/debug_{prefix}_{safe_email}.png", full_page=True
        )
    except Exception:
        pass


def _is_pioneer_app(url: str) -> bool:
    """Check if URL is on Pioneer domain (any page including login)."""
    return "agent.pioneer.ai" in url


def _is_pioneer_dashboard(url: str) -> bool:
    """Check if URL is Pioneer dashboard (authenticated, not login/auth page)."""
    if "agent.pioneer.ai" not in url:
        return False
    # Must NOT be login or auth page
    from urllib.parse import urlparse
    path = urlparse(url).path
    if path in ("/login", "/auth", "/signup", "/register"):
        return False
    # Must be a Pioneer page, not a Google redirect
    if "accounts.google.com" in url or "consent.google.com" in url:
        return False
    return True


def _check_pioneer_error(url: str) -> str | None:
    """Check if Pioneer URL contains an error. Returns error message or None."""
    if "agent.pioneer.ai" not in url:
        return None
    if "error=" not in url:
        return None
    # Parse error from URL params or hash
    from urllib.parse import urlparse, parse_qs
    parsed = urlparse(url)
    # Error can be in query OR fragment
    params = parse_qs(parsed.query)
    if not params.get("error"):
        params = parse_qs(parsed.fragment)
    error = params.get("error", [""])[0]
    error_code = params.get("error_code", [""])[0]
    error_desc = params.get("error_description", [""])[0]
    if error or error_code or error_desc:
        return f"Pioneer error: {error} / {error_code} / {error_desc}"
    return None


def _is_google_domain(url: str) -> bool:
    """Check if URL is on any Google domain."""
    return any(d in url for d in [
        "accounts.google.com", "consent.google.com",
        "myaccount.google.com", "policies.google.com",
    ])


async def _wait_for_hydration(page, log=None):
    """Wait for Pioneer SPA to hydrate (buttons to render in DOM)."""
    if log:
        log("Waiting for page hydration...")
    for _ in range(15):  # max 15 seconds
        try:
            count = await page.locator("button").count()
            if count >= 2:  # Pioneer login has at least 4 buttons
                return True
        except Exception:
            pass
        await asyncio.sleep(1)
    return False


async def _clear_all_interstitials(page, log=None, safe_email: str = ""):
    """
    Clear ALL interstitials in a loop. Returns count handled.
    This handles: Google TOS, workspace welcome, consent, account picker, etc.
    """
    handled = 0
    for i in range(MAX_INTERSTITIALS):
        if await detect_captcha(page):
            raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA detected")

        edge = await handle_all_edge_cases(page)

        if edge == "verify_challenge":
            raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge")

        if edge != "none":
            handled += 1
            if log:
                log(f"  Cleared: {edge}")
            await random_delay(1000, 2000)
            try:
                await page.wait_for_load_state("networkidle", timeout=10000)
            except Exception:
                pass
            await asyncio.sleep(1)
            continue

        # Also try consent screen explicitly
        if _is_google_domain(page.url):
            try:
                await handle_consent(page)
                handled += 1
                if log:
                    log("  Cleared: consent")
                await random_delay(500, 1000)
                continue
            except Exception:
                pass

        break

    return handled


class PioneerProvider(ProviderAdapter):

    @property
    def name(self) -> str:
        return "pioneer"

    @property
    def display_name(self) -> str:
        return "Pioneer AI ($200 free credit)"

    @property
    def auth_flow(self) -> str:
        return "google_oauth"

    @property
    def handles_own_browser_phase(self) -> bool:
        return True

    def build_auth_url(self, state: str) -> str:
        return PIONEER_LOGIN

    async def full_browser_flow(self, page, account: dict, status_cb=None) -> HarvestResult:
        """
        LINEAR flow — no complex branching, no skipping steps.
        Each step must complete before moving to the next.
        """
        global _step_counter
        _step_counter = 0

        email = account["email"]
        password = account["password"]
        safe_email = email.replace("@", "_at_").replace(".", "_")

        def log(msg):
            if status_cb:
                status_cb(msg)

        # =================================================================
        # STEP 1: Make sure we're on Pioneer login page with buttons visible
        # =================================================================
        log("Step 1: Pioneer login page...")
        await _dump_step(page, "step1_initial", safe_email, log)

        # If we're not on Pioneer, navigate there
        if not _is_pioneer_app(page.url):
            # Might be on Google TOS — clear it first
            await _clear_all_interstitials(page, log, safe_email)
            if not _is_pioneer_app(page.url):
                await page.goto(PIONEER_LOGIN, wait_until="domcontentloaded", timeout=20000)

        # Wait for SPA to hydrate
        hydrated = await _wait_for_hydration(page, log)
        await _dump_step(page, "step1_hydrated", safe_email, log)

        if not hydrated:
            raise BatchLoginError(
                ErrorCode.TIMEOUT,
                f"Pioneer login page did not hydrate (url: {page.url})"
            )

        # =================================================================
        # STEP 2: Click "Continue with Google"
        # =================================================================
        log("Step 2: Clicking Continue with Google...")
        google_btn = await _find_google_button(page)
        if not google_btn:
            await page.screenshot(
                path=f"results/pioneer_no_google_btn_{email}.png", full_page=True
            )
            raise BatchLoginError(
                ErrorCode.UNHANDLED,
                f"No 'Continue with Google' button (url: {page.url})"
            )

        await google_btn.click()

        # Wait for Google login page to appear
        try:
            await page.wait_for_url(
                lambda url: _is_google_domain(url),
                timeout=15000,
            )
        except Exception:
            # Maybe already auto-logged in and went to Pioneer dashboard
            if _is_pioneer_dashboard(page.url):
                log("Auto-authenticated, skipping to dashboard")
                # Jump to step 5
                await _dump_step(page, "step2_auto_auth", safe_email, log)
                return await self._from_dashboard(page, email, safe_email, log)
            raise BatchLoginError(
                ErrorCode.TIMEOUT,
                f"No Google login page after clicking button (url: {page.url})"
            )

        await _dump_step(page, "step2_google_page", safe_email, log)

        # =================================================================
        # STEP 3: Google login — enter email + password
        # Handle any pre-login interstitials (account picker, etc.)
        # =================================================================
        log("Step 3: Google login...")

        # Clear pre-login interstitials (account picker, but NOT TOS — TOS
        # comes AFTER login on these accounts)
        edge = await handle_all_edge_cases(page)
        if edge == "verify_challenge":
            raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge")
        elif edge != "none":
            log(f"  Pre-login: {edge}")
            await random_delay(500, 1000)

        await _dump_step(page, "step3_before_login", safe_email, log)

        # Enter credentials
        log("Entering credentials...")
        await google_login(page, email, password)

        await _dump_step(page, "step3_after_login", safe_email, log)

        # Check for immediate Pioneer error (e.g. DB error on first signup)
        pioneer_err = _check_pioneer_error(page.url)
        if pioneer_err:
            await _dump_step(page, "step3_pioneer_error", safe_email, log)
            raise BatchLoginError(
                ErrorCode.UNHANDLED,
                f"{pioneer_err} (account may not be registered on Pioneer)"
            )

        # =================================================================
        # STEP 4: POST-LOGIN — clear ALL interstitials until we reach Pioneer
        # This is where Google TOS ("Before you continue" / "I agree") appears!
        # Loop aggressively until we're on Pioneer dashboard.
        # =================================================================
        log("Step 4: Post-login interstitial clearing...")

        for attempt in range(MAX_INTERSTITIALS):
            await _dump_step(page, f"step4_loop_{attempt}", safe_email, log)

            # Already on Pioneer dashboard? Done!
            if _is_pioneer_dashboard(page.url):
                log("Reached Pioneer dashboard!")
                break

            # Pioneer returned an error? (e.g. "Database error saving new user")
            pioneer_err = _check_pioneer_error(page.url)
            if pioneer_err:
                await _dump_step(page, "step4_pioneer_error", safe_email, log)
                raise BatchLoginError(
                    ErrorCode.UNHANDLED,
                    f"{pioneer_err} (account may not be registered on Pioneer)"
                )

            # CAPTCHA check
            if await detect_captcha(page):
                raise BatchLoginError(ErrorCode.CAPTCHA_DETECTED, "CAPTCHA after login")

            # Try edge case handlers (TOS, workspace welcome, etc.)
            edge = await handle_all_edge_cases(page)
            if edge == "verify_challenge":
                await page.screenshot(
                    path=f"results/pioneer_verify_{email}.png", full_page=True
                )
                raise BatchLoginError(ErrorCode.VERIFY_CHALLENGE, "Verify challenge")
            elif edge != "none":
                log(f"  Cleared: {edge}")
                await random_delay(1000, 2000)
                try:
                    await page.wait_for_load_state("networkidle", timeout=10000)
                except Exception:
                    pass
                continue

            # Try consent screen
            if _is_google_domain(page.url):
                try:
                    await handle_consent(page)
                    log("  Cleared: consent")
                    await random_delay(500, 1000)
                    continue
                except Exception:
                    pass

            # Nothing to clear — wait a bit and check if we're being redirected
            await asyncio.sleep(2)

            # If still on Google, wait for redirect
            if _is_google_domain(page.url):
                try:
                    await page.wait_for_url(
                        lambda url: not _is_google_domain(url),
                        timeout=10000,
                    )
                except Exception:
                    pass
                continue

            # If we're on Pioneer but not dashboard (e.g. loading), wait
            if _is_pioneer_app(page.url) and not _is_pioneer_dashboard(page.url):
                await asyncio.sleep(3)
                continue

            break

        # =================================================================
        # STEP 5: We should be on Pioneer dashboard now
        # =================================================================
        if not _is_pioneer_dashboard(page.url):
            log(f"Waiting for Pioneer dashboard (currently at {page.url})...")
            try:
                await page.wait_for_url(
                    lambda url: _is_pioneer_dashboard(url),
                    timeout=DASHBOARD_LOAD_TIMEOUT,
                )
            except Exception:
                await _dump_step(page, "step5_no_dashboard", safe_email, log)
                # Last chance: clear interstitials
                await _clear_all_interstitials(page, log, safe_email)
                if not _is_pioneer_dashboard(page.url):
                    await page.screenshot(
                        path=f"results/pioneer_no_dashboard_{email}.png", full_page=True
                    )
                    raise BatchLoginError(
                        ErrorCode.TIMEOUT,
                        f"Never reached Pioneer dashboard (stuck at: {page.url})"
                    )

        await asyncio.sleep(3)
        log("Dashboard loaded!")
        await _dump_step(page, "step5_dashboard", safe_email, log)

        return await self._from_dashboard(page, email, safe_email, log)

    async def _from_dashboard(self, page, email: str, safe_email: str, log) -> HarvestResult:
        """From Pioneer dashboard, navigate to /api-keys and create key."""
        # =================================================================
        # STEP 6: Navigate to /api-keys
        # =================================================================
        api_key = await _create_api_key_via_ui(page, email, safe_email, log)

        # =================================================================
        # STEP 7: Validate
        # =================================================================
        log("Step 7: Validating API key...")
        await _validate_api_key(api_key)

        log("API key validated!")
        return HarvestResult(
            provider="pioneer",
            email=email,
            credentials={"api_key": api_key},
            metadata={"plan": "free", "credit": "$200"},
        )

    # --- ABC required but unused ---

    async def browser_flow(self, page, account: dict) -> dict:
        raise NotImplementedError("Pioneer uses full_browser_flow")

    async def post_browser(self, intermediate: dict, account: dict) -> HarvestResult:
        raise NotImplementedError("Pioneer uses full_browser_flow")

    def get_credential_fields(self) -> list[str]:
        return ["api_key"]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

async def _find_google_button(page) -> object:
    """Find the 'Continue with Google' button on Pioneer login page."""
    for selector in [
        'button:has-text("Continue with Google")',
        'button:has-text("Sign in with Google")',
        'button:has-text("Google")',
        'a:has-text("Continue with Google")',
        'a:has-text("Sign in with Google")',
        '[data-provider="google"]',
    ]:
        try:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=3000):
                return btn
        except Exception:
            continue
    return None


async def _create_api_key_via_ui(page, email: str, safe_email: str, log) -> str:
    """
    Navigate directly to /api-keys, create key, extract it.
    """
    # --- Navigate to /api-keys ---
    log("Step 6: Navigating to /api-keys...")
    try:
        await page.goto(PIONEER_API_KEYS_URL, wait_until="domcontentloaded", timeout=20000)
    except Exception:
        await page.goto(PIONEER_API_KEYS_URL, wait_until="networkidle", timeout=30000)

    await asyncio.sleep(3)

    # Clear any interstitials
    await _clear_all_interstitials(page, log, safe_email)

    # If redirected away, try again
    if not _is_pioneer_app(page.url):
        log("Redirected away, navigating back...")
        await page.goto(PIONEER_API_KEYS_URL, wait_until="networkidle", timeout=20000)
        await asyncio.sleep(3)
        await _clear_all_interstitials(page, log, safe_email)

    # Session lost?
    if "/login" in page.url or "/auth" in page.url:
        await _dump_step(page, "apikeys_session_lost", safe_email, log)
        raise BatchLoginError(
            ErrorCode.UNHANDLED,
            f"Session lost - redirected to login ({page.url})"
        )

    # Wait for SPA hydration on api-keys page
    await _wait_for_hydration(page, log)
    await _dump_step(page, "step6_apikeys_page", safe_email, log)

    # --- Click "Create key" button ---
    log("Looking for Create key button...")
    create_btn = None
    for selector in [
        'button:has-text("Create key")',
        'button:has-text("Create Key")',
        'button:has(svg.lucide-plus):has-text("Create")',
        'button:has(svg.lucide-plus)',
    ]:
        try:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=3000):
                create_btn = btn
                log(f"Found: {selector}")
                break
        except Exception:
            continue

    if not create_btn:
        await page.screenshot(
            path=f"results/pioneer_no_create_btn_{email}.png", full_page=True
        )
        await _dump_step(page, "no_create_btn", safe_email, log)
        raise BatchLoginError(
            ErrorCode.UNHANDLED,
            f"No 'Create key' button on /api-keys for {email}"
        )

    await create_btn.click()
    await asyncio.sleep(1.5)
    await _dump_step(page, "step6_create_modal", safe_email, log)

    # --- Fill name = "local" ---
    log("Filling key name...")
    name_input = None
    for selector in [
        'input[placeholder*="Production"]',
        'input[placeholder*="Development"]',
        'input[placeholder*="e.g."]',
        'form input[type="text"]',
        '.rounded-xl input[type="text"]',
        'input[type="text"]',
    ]:
        try:
            inp = page.locator(selector).first
            if await inp.is_visible(timeout=2000):
                name_input = inp
                break
        except Exception:
            continue

    if not name_input:
        await _dump_step(page, "no_name_input", safe_email, log)
        raise BatchLoginError(
            ErrorCode.UNHANDLED, f"No key name input for {email}"
        )

    await name_input.fill("local")
    await asyncio.sleep(0.5)

    # --- Set expiration to "Never" ---
    log("Setting expiration to Never...")
    expiry_btn = None
    for selector in [
        'button:has(svg.lucide-clock)',
        'button:has-text("7 days")',
        'button:has-text("30 days")',
        'button:has-text("90 days")',
        'button:has-text("Expiration")',
    ]:
        try:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=2000):
                expiry_btn = btn
                break
        except Exception:
            continue

    if expiry_btn:
        await expiry_btn.click()
        await asyncio.sleep(0.5)

        never_option = None
        for selector in [
            'button:text-is("Never")',
            'div:text-is("Never")',
            'li:text-is("Never")',
            'span:text-is("Never")',
            '[role="option"]:has-text("Never")',
            ':text-is("Never")',
        ]:
            try:
                opt = page.locator(selector).first
                if await opt.is_visible(timeout=2000):
                    never_option = opt
                    break
            except Exception:
                continue

        if never_option:
            await never_option.click()
            await asyncio.sleep(0.5)
            log("Set to Never")
        else:
            log("'Never' option not found, using default")
    else:
        log("Expiration dropdown not found, using default")

    # --- Submit ---
    log("Submitting...")
    submit_btn = None
    for selector in [
        'form button[type="submit"]',
        'form button:has-text("Create key")',
        'form button:has-text("Create Key")',
        '.rounded-xl button:has-text("Create key")',
    ]:
        try:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=2000):
                submit_btn = btn
                break
        except Exception:
            continue

    if not submit_btn:
        await _dump_step(page, "no_submit", safe_email, log)
        raise BatchLoginError(
            ErrorCode.UNHANDLED, f"No submit button for {email}"
        )

    await asyncio.sleep(0.5)
    await submit_btn.click()
    await asyncio.sleep(3)
    await _dump_step(page, "step6_reveal_modal", safe_email, log)

    # --- Reveal key ---
    log("Revealing key...")
    for selector in [
        'button:has-text("Reveal")',
        'button:has(svg.lucide-eye):has-text("Reveal")',
        'button:has(svg.lucide-eye)',
    ]:
        try:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=5000):
                await btn.click()
                await asyncio.sleep(1)
                log("Key revealed")
                break
        except Exception:
            continue

    await _dump_step(page, "step6_after_reveal", safe_email, log)

    # --- Extract key ---
    api_key = await _extract_key_from_page(page)

    if not api_key:
        await page.screenshot(
            path=f"results/pioneer_nokey_{email}.png", full_page=True
        )
        await _dump_step(page, "nokey", safe_email, log)
        raise BatchLoginError(
            ErrorCode.UNHANDLED,
            f"Could not extract API key for {email}"
        )

    log(f"Got key: {api_key[:12]}...")

    # Cleanup: click Done
    try:
        done = page.locator('button:has-text("Done")').first
        if await done.is_visible(timeout=1000):
            await done.click()
    except Exception:
        pass

    return api_key


async def _extract_key_from_page(page) -> str | None:
    """Extract a pio_sk_ key from the current page."""
    # Method 1: <code> elements
    try:
        codes = page.locator("code")
        count = await codes.count()
        for i in range(count):
            text = await codes.nth(i).text_content()
            if text and "pio_sk_" in text:
                clean = text.strip()
                if clean.startswith("pio_sk_") and "\u2022" not in clean and "*" not in clean:
                    return clean
    except Exception:
        pass

    # Method 2: input fields
    for selector in ['input[readonly]', 'input[type="text"]', 'textarea']:
        try:
            elements = page.locator(selector)
            count = await elements.count()
            for i in range(count):
                el = elements.nth(i)
                try:
                    text = await el.input_value()
                except Exception:
                    text = await el.text_content()
                if text and text.strip().startswith("pio_sk_"):
                    return text.strip()
        except Exception:
            continue

    # Method 3: regex on full HTML
    try:
        content = await page.content()
        match = re.search(r'pio_sk_[a-zA-Z0-9_-]{20,}', content)
        if match:
            return match.group(0)
    except Exception:
        pass

    return None


async def _validate_api_key(api_key: str):
    """Validate the API key."""
    async with httpx.AsyncClient(timeout=15) as client:
        resp = await client.get(
            f"{PIONEER_API_BASE}/base-models?supports_inference=true",
            headers={"X-API-Key": api_key},
        )
        if resp.status_code != 200:
            raise BatchLoginError(
                ErrorCode.UNHANDLED,
                f"API key validation failed (HTTP {resp.status_code})"
            )
