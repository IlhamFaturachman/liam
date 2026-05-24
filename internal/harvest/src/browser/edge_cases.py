"""
Google OAuth edge case handlers
Handles: account picker, TOS, region selection, verify challenges, email verification
Learned from enowxai-dupl patterns.
"""

from playwright.async_api import Page
from utils.delay import random_delay


async def handle_account_picker(page: Page) -> bool:
    """
    Handle "Choose an account" page.
    If multiple accounts are shown, click "Use another account" to go to fresh login.
    Returns True if handled, False if not on this page.
    """
    try:
        # Detect account picker page
        use_another = page.locator(
            'div:has-text("Use another account"), '
            'div:has-text("Gunakan akun lain"), '
            'li[data-identifier="accountIdentifier"]'
        ).first

        if await use_another.is_visible(timeout=2000):
            await random_delay(500, 1000)
            # Click "Use another account"
            another_btn = page.locator(
                '[data-identifier="accountIdentifier"]:has-text("Use another"), '
                'li:has-text("Use another account"), '
                'li:has-text("Gunakan akun lain")'
            ).first

            if await another_btn.is_visible(timeout=2000):
                await another_btn.click()
                await page.wait_for_load_state("networkidle", timeout=10000)
                return True

            # Fallback: click the last item (usually "Use another account")
            items = page.locator('[data-authuser]')
            count = await items.count()
            if count > 0:
                last_item = items.nth(count - 1)
                await last_item.click()
                await page.wait_for_load_state("networkidle", timeout=10000)
                return True

        return False
    except Exception:
        return False


async def handle_workspace_welcome(page: Page) -> bool:
    """
    Handle Google Workspace "Welcome to your new account" consent page.
    This page appears for new workspace accounts and uses an input[type="submit"]
    button — NOT a regular <button>. The button text varies by locale
    ("I understand", "Saya mengerti", etc.) so we detect by DOM structure,
    not by text.

    The page may appear 1-2 times (different consent steps with different
    hidden fields). The form POSTs to 'speedbump/gaplustos'.

    Returns True if handled, False if not on this page.
    """
    try:
        # Strategy: detect the page by its unique DOM elements, not button text.
        # The workspace welcome page always has:
        #   - A form with action containing "gaplustos" or "speedbump"
        #   - An input#confirm or input[name="confirm"] (submit button)
        # These selectors are language-agnostic.

        # Priority 1: the confirm input (always present, any language)
        confirm_selectors = [
            'input#confirm[name="confirm"]',
            'input[name="confirm"][type="submit"]',
            'input#confirm[type="submit"]',
        ]

        for selector in confirm_selectors:
            el = page.locator(selector).first
            try:
                if await el.is_visible(timeout=3000):
                    await random_delay(800, 2000)
                    await el.click()
                    await page.wait_for_load_state("networkidle", timeout=15000)
                    return True
            except Exception:
                continue

        # Priority 2: detect by URL pattern (gaplustos/speedbump) or form action
        url = page.url
        is_speedbump = "speedbump" in url or "gaplustos" in url
        if not is_speedbump:
            # Check if page has a form posting to speedbump/gaplustos
            try:
                is_speedbump = await page.evaluate("""
                    () => {
                        const forms = document.querySelectorAll('form');
                        for (const f of forms) {
                            const action = f.getAttribute('action') || '';
                            if (action.includes('gaplustos') || action.includes('speedbump'))
                                return true;
                        }
                        return false;
                    }
                """)
            except Exception:
                pass

        if is_speedbump:
            # On the speedbump page — find ANY submit input or prominent button
            submit_btn = page.locator(
                'input[type="submit"], '
                'button[type="submit"]'
            ).first
            try:
                if await submit_btn.is_visible(timeout=3000):
                    await random_delay(800, 2000)
                    await submit_btn.click()
                    await page.wait_for_load_state("networkidle", timeout=15000)
                    return True
            except Exception:
                pass

        return False
    except Exception:
        return False


async def handle_google_tos(page: Page) -> bool:
    """
    Handle Google Terms of Service / Privacy Policy / Cookie Consent page.
    This includes multiple variants:
    - "Before you continue" cookie consent (consent.google.com)
    - "Terms of Service" acceptance page
    - "Privacy and Terms" page

    The button text varies by locale ("I agree", "Saya setuju", "Ich stimme zu",
    "J'accepte", "Acepto", etc.) so we detect by DOM structure and visual style
    too, not just text.

    The accept button is always the prominent blue button.
    May appear 1-2+ times in sequence.

    Returns True if handled, False if not on this page.
    """
    try:
        # Strategy 1: known ID — Google TOS always has #agreeButton when present
        agree_by_id = page.locator('#agreeButton')
        try:
            if await agree_by_id.is_visible(timeout=2000):
                await random_delay(500, 1200)
                await agree_by_id.click()
                await page.wait_for_load_state("networkidle", timeout=10000)
                return True
        except Exception:
            pass

        # Strategy 2: detect TOS/consent page by URL pattern
        url = page.url
        is_tos_page = any(p in url for p in [
            "/terms", "/privacy", "/consent",
            "consent.google.com", "myaccount.google.com/termsofservice",
            "policies.google.com",
        ])

        # Strategy 3: detect by page content — look for common TOS/consent
        # indicators in the page text. This is more reliable than URL alone
        # because Google sometimes serves consent pages on accounts.google.com.
        if not is_tos_page:
            try:
                is_tos_page = await page.evaluate("""
                    () => {
                        const body = document.body ? document.body.innerText : '';
                        // Google TOS / cookie consent indicators (multi-language)
                        const indicators = [
                            'Terms of Service', 'Privacy Policy',
                            'Persyaratan Layanan', 'Kebijakan Privasi',
                            'Privacy and Terms', 'Terms and Privacy',
                            'Before you continue', 'Sebelum melanjutkan',
                            'Antes de continuar', 'Avant de continuer',
                            'Bevor du fortfährst',
                        ];
                        const hasIndicator = indicators.some(t => body.includes(t));
                        // Also check for the consent form structure
                        const hasConsentForm = !!document.querySelector(
                            'form[action*="consent"], form[action*="signin/v2/consent"], ' +
                            'div[data-consent-bumper], #consent-bump'
                        );
                        return hasIndicator || hasConsentForm;
                    }
                """)
            except Exception:
                pass

        if not is_tos_page:
            # Quick-check: try common button texts (fast path for EN/ID)
            quick_selectors = [
                'button:has-text("I agree")',
                'button:has-text("Accept all")',
                'button:has-text("Saya setuju")',
                'button:has-text("Terima semua")',
            ]
            for sel in quick_selectors:
                try:
                    if await page.locator(sel).first.is_visible(timeout=1000):
                        is_tos_page = True
                        break
                except Exception:
                    continue

        if not is_tos_page:
            return False

        # We're on a TOS/consent page — find the accept/agree button.
        # Try known text patterns first (covers ~90% of locales)
        text_selectors = [
            'button:has-text("I agree")',
            'button:has-text("Saya setuju")',
            'button:has-text("Accept all")',
            'button:has-text("Terima semua")',
            'button:has-text("Agree")',
            'button:has-text("Setuju")',
            'button:has-text("Acepto")',
            "button:has-text(\"J'accepte\")",
            'button:has-text("Ich stimme zu")',
            'button:has-text("Akkoord")',
            'button:has-text("Concordo")',
            'button:has-text("Aceitar")',
            'button:has-text("Aceptar todo")',
            'button:has-text("Tout accepter")',
            'button:has-text("Alle akzeptieren")',
            'button:has-text("Aceitar tudo")',
            'button:has-text("Accetta tutto")',
            'button:has-text("Kabul ediyorum")',
            'button:has-text("Zgadzam")',
            'button:has-text("Souhlasím")',
            'button:has-text("Hyväksyn")',
            'button:has-text("Godkjenn")',
            'button:has-text("Godkänn")',
            'button:has-text("Accepter")',
            'button:has-text("Aanvaarden")',
            'button:has-text("Přijmout")',
            'input[type="submit"][value*="agree"]',
            'input[type="submit"][value*="I agree"]',
            'input[type="submit"][value*="Agree"]',
        ]
        for sel in text_selectors:
            try:
                btn = page.locator(sel).first
                if await btn.is_visible(timeout=800):
                    await random_delay(500, 1200)
                    await btn.click()
                    await page.wait_for_load_state("networkidle", timeout=10000)
                    return True
            except Exception:
                continue

        # Fallback: language-agnostic — find the blue/prominent button via JS
        # Google TOS/consent button is always blue (primary action color)
        try:
            clicked = await page.evaluate("""
                () => {
                    const buttons = document.querySelectorAll('button, input[type="submit"]');
                    for (const btn of buttons) {
                        if (!btn.offsetParent) continue; // skip hidden
                        const style = window.getComputedStyle(btn);
                        const bg = style.backgroundColor;
                        // Google blue: rgb(26, 115, 232) or similar blue shades
                        const match = bg.match(/rgb\\((\\d+),\\s*(\\d+),\\s*(\\d+)/);
                        if (match) {
                            const [, r, g, b] = match.map(Number);
                            // Blue-ish: blue channel dominant
                            if (b > 180 && b > r * 1.5 && b > g * 1.2) {
                                btn.click();
                                return true;
                            }
                        }
                    }
                    return false;
                }
            """)
            if clicked:
                await page.wait_for_load_state("networkidle", timeout=10000)
                return True
        except Exception:
            pass

        # Last resort: click the last visible button (usually the accept one)
        try:
            buttons = page.locator('button[type="submit"], form button')
            count = await buttons.count()
            if count > 0:
                last_btn = buttons.nth(count - 1)
                if await last_btn.is_visible(timeout=1000):
                    await random_delay(500, 1200)
                    await last_btn.click()
                    await page.wait_for_load_state("networkidle", timeout=10000)
                    return True
        except Exception:
            pass

        return False
    except Exception:
        return False

        # We're on a TOS page — find the accept/agree button.
        # Strategy: find the prominent blue button. Google TOS uses a specific
        # pattern: the agree button is always a <button> with blue background
        # (rgb(26,115,232) or similar) or the last/rightmost button in the
        # button group at the bottom.

        # Try known text patterns first (covers ~90% of locales)
        text_selectors = [
            'button:has-text("I agree")',
            'button:has-text("Saya setuju")',
            'button:has-text("Accept all")',
            'button:has-text("Terima semua")',
            'button:has-text("Agree")',
            'button:has-text("Setuju")',
            'button:has-text("Acepto")',
            'button:has-text("J\'accepte")',
            'button:has-text("Ich stimme zu")',
            'button:has-text("Akkoord")',
            'button:has-text("Concordo")',
            'button:has-text("Aceitar")',
            'button:has-text("Aceptar todo")',
            'button:has-text("Tout accepter")',
            'button:has-text("Alle akzeptieren")',
            'button:has-text("Aceitar tudo")',
            'button:has-text("Accetta tutto")',
            'input[type="submit"][value="I agree"]',
        ]
        for sel in text_selectors:
            try:
                btn = page.locator(sel).first
                if await btn.is_visible(timeout=800):
                    await random_delay(500, 1200)
                    await btn.click()
                    await page.wait_for_load_state("networkidle", timeout=10000)
                    return True
            except Exception:
                continue

        # Fallback: language-agnostic — find the blue/prominent button via JS
        # Google TOS button is always the one with a blue-ish background
        try:
            clicked = await page.evaluate("""
                () => {
                    const buttons = document.querySelectorAll('button, input[type="submit"]');
                    for (const btn of buttons) {
                        const style = window.getComputedStyle(btn);
                        const bg = style.backgroundColor;
                        // Google blue: rgb(26, 115, 232) or similar blue shades
                        // Also check for high-contrast primary buttons
                        const match = bg.match(/rgb\\((\\d+),\\s*(\\d+),\\s*(\\d+)/);
                        if (match) {
                            const [, r, g, b] = match.map(Number);
                            // Blue-ish: blue channel dominant, not gray/white/black
                            if (b > 180 && b > r * 1.5 && b > g * 1.2) {
                                btn.click();
                                return true;
                            }
                        }
                    }
                    return false;
                }
            """)
            if clicked:
                await page.wait_for_load_state("networkidle", timeout=10000)
                return True
        except Exception:
            pass

        # Last resort: click the last visible button (usually the accept one)
        try:
            buttons = page.locator('button[type="submit"], form button')
            count = await buttons.count()
            if count > 0:
                last_btn = buttons.nth(count - 1)
                if await last_btn.is_visible(timeout=1000):
                    await random_delay(500, 1200)
                    await last_btn.click()
                    await page.wait_for_load_state("networkidle", timeout=10000)
                    return True
        except Exception:
            pass

        return False
    except Exception:
        return False


async def handle_region_selection(page: Page) -> bool:
    """
    Handle region/country selection page that sometimes appears after login.
    Returns True if handled, False if not on this page.
    """
    try:
        # Detect region selection
        region_indicators = [
            'select[name="country"]',
            'div:has-text("Select your country")',
            'div:has-text("Pilih negara")',
            'div:has-text("Choose your region")',
        ]

        for selector in region_indicators:
            el = page.locator(selector).first
            if await el.is_visible(timeout=2000):
                # Try to find and click continue/next without changing region
                continue_selectors = [
                    'button:has-text("Continue")',
                    'button:has-text("Lanjutkan")',
                    'button:has-text("Next")',
                    'button:has-text("Confirm")',
                    'button[type="submit"]',
                ]
                for cont_sel in continue_selectors:
                    cont_btn = page.locator(cont_sel).first
                    if await cont_btn.is_visible(timeout=1000):
                        await random_delay(500, 1000)
                        await cont_btn.click()
                        await page.wait_for_load_state("networkidle", timeout=10000)
                        return True

        return False
    except Exception:
        return False


async def handle_verify_challenge(page: Page) -> bool:
    """
    Detect "Verify it's you" challenges (phone, recovery email, etc.)
    These are NOT captchas but identity verification challenges.
    Returns True if challenge detected (account should be skipped).
    """
    try:
        challenge_indicators = [
            'span:has-text("Verify it\'s you")',
            'span:has-text("Verifikasi bahwa ini Anda")',
            'span:has-text("Confirm your recovery")',
            'span:has-text("Konfirmasi email pemulihan")',
            'span:has-text("Enter a phone number")',
            'span:has-text("Masukkan nomor telepon")',
            'span:has-text("Get a verification code")',
            '#idvPreregisteredPhonePin',
            'input[name="phoneNumberId"]',
            'div[data-challengetype]',
            'span:has-text("Too many failed attempts")',
        ]

        for selector in challenge_indicators:
            el = page.locator(selector).first
            try:
                if await el.is_visible(timeout=1500):
                    return True
            except Exception:
                continue

        return False
    except Exception:
        return False


async def handle_save_password_prompt(page: Page) -> bool:
    """
    Handle Chrome/browser "Save password?" prompt or Google's "Stay signed in?" page.
    Returns True if handled, False if not on this page.
    """
    try:
        prompts = [
            'button:has-text("Not now")',
            'button:has-text("Tidak sekarang")',
            'button:has-text("No thanks")',
            'button:has-text("Tidak, terima kasih")',
            '#save-password-no',
        ]

        for selector in prompts:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=2000):
                await random_delay(300, 800)
                await btn.click()
                return True

        return False
    except Exception:
        return False


async def handle_unverified_app_warning(page: Page) -> bool:
    """
    Handle Google OAuth "Unverified App" or "Make sure you downloaded this app" speed bump.
    Clicks the 'Sign in' / 'Continue' button on this specific warning screen.
    Returns True if handled, False if not on this page.
    """
    try:
        # Detect the unverified app warning screen
        warning_indicators = [
            'h1:has-text("Make sure that you downloaded this app from Google")',
            'h1:has-text("Google hasn\'t verified this app")',
            'h1:has-text("Aplikasi ini belum diverifikasi")',
        ]

        is_warning_page = False
        for selector in warning_indicators:
            try:
                if await page.locator(selector).first.is_visible(timeout=3000):
                    is_warning_page = True
                    break
            except Exception:
                continue

        if not is_warning_page:
            return False

        # Find and click the 'Sign in' / 'Continue' / 'Advanced' button
        # Google often hides the continue button behind an "Advanced" link first on unverified apps
        try:
            advanced_btn = page.locator('button:has-text("Advanced"), button:has-text("Lanjutan")').first
            if await advanced_btn.is_visible(timeout=2000):
                await random_delay(500, 1000)
                await advanced_btn.click()
                await random_delay(500, 1000)
        except Exception:
            pass

        continue_selectors = [
            '#submit_approve_access button',
            '#submit_approve_access',
            'button:has-text("Sign in")',
            'button:has-text("Continue")',
            'button:has-text("Lanjutkan")',
            'div[role="button"]:has-text("Sign in")',
            'div[role="button"]:has-text("Continue")',
            'a:has-text("Go to Google Antigravity (unsafe)")',
        ]

        for sel in continue_selectors:
            try:
                btn = page.locator(sel).first
                if await btn.is_visible(timeout=2000):
                    await random_delay(800, 1500)
                    # Use force=True in case there are overlapping shadow elements or overlays
                    await btn.click(force=True)
                    await page.wait_for_load_state("networkidle", timeout=10000)
                    return True
            except Exception:
                continue

        return False
    except Exception:
        return False


async def handle_all_edge_cases(page: Page) -> str:
    """
    Run all edge case handlers in sequence.
    Returns:
        "account_picker" - handled account picker
        "workspace_welcome" - handled workspace welcome consent
        "tos" - handled TOS
        "region" - handled region selection
        "save_password" - handled save password prompt
        "verify_challenge" - challenge detected (should skip account)
        "none" - no edge case detected
    """
    # Check verify challenge first (non-recoverable)
    if await handle_verify_challenge(page):
        return "verify_challenge"

    # Account picker
    if await handle_account_picker(page):
        return "account_picker"

    # Workspace welcome ("Welcome to your new account" with "I understand")
    # Must run BEFORE generic TOS — this is a different page type
    if await handle_workspace_welcome(page):
        return "workspace_welcome"

    # Unverified App / Speed bump ("Make sure you downloaded this app...")
    if await handle_unverified_app_warning(page):
        return "unverified_app_warning"

    # TOS
    if await handle_google_tos(page):
        return "tos"

    # Region selection
    if await handle_region_selection(page):
        return "region"

    # Save password prompt
    if await handle_save_password_prompt(page):
        return "save_password"

    return "none"
