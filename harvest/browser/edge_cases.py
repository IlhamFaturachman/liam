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


async def handle_google_tos(page: Page) -> bool:
    """
    Handle Google Terms of Service / Privacy Policy acceptance page.
    Returns True if handled, False if not on this page.
    """
    try:
        # Detect TOS page
        tos_indicators = [
            'button:has-text("I agree")',
            'button:has-text("Saya setuju")',
            'button:has-text("Accept all")',
            'button:has-text("Terima semua")',
            'span:has-text("Terms of Service")',
            'span:has-text("Privacy and Terms")',
        ]

        for selector in tos_indicators:
            btn = page.locator(selector).first
            if await btn.is_visible(timeout=2000):
                # Find and click the accept/agree button
                accept_selectors = [
                    'button:has-text("I agree")',
                    'button:has-text("Saya setuju")',
                    'button:has-text("Accept all")',
                    'button:has-text("Terima semua")',
                    'button:has-text("Agree")',
                    '#agreeButton',
                ]
                for accept_sel in accept_selectors:
                    accept_btn = page.locator(accept_sel).first
                    if await accept_btn.is_visible(timeout=1000):
                        await random_delay(500, 1200)
                        await accept_btn.click()
                        await page.wait_for_load_state("networkidle", timeout=10000)
                        return True

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


async def handle_all_edge_cases(page: Page) -> str:
    """
    Run all edge case handlers in sequence.
    Returns:
        "account_picker" - handled account picker
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
