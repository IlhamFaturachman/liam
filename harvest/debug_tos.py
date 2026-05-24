"""
Debug script: login to Pioneer with one account, dump page info at each step.
Run: cd ~/.liam/harvest && venv/bin/python3 debug_tos.py
"""

import asyncio
import sys
import os
import json

sys.path.insert(0, os.path.expanduser("~/.liam/harvest"))

from browser.launch import BrowserSession
from browser.google_login import google_login
from browser.edge_cases import handle_all_edge_cases
from browser.consent import detect_captcha
from utils.delay import random_delay


PIONEER_LOGIN = "https://agent.pioneer.ai/login"
EMAIL = "Yazzie5272@guzeil.com"
PASSWORD = "qwertyui"


async def dump_page(page, label):
    """Dump page state for debugging."""
    print(f"\n{'='*60}")
    print(f"[{label}] URL: {page.url}")

    try:
        await page.screenshot(path=f"results/debug_{label}.png", full_page=True)
        print(f"  Screenshot saved: results/debug_{label}.png")
    except Exception as e:
        print(f"  Screenshot failed: {e}")

    try:
        analysis = await page.evaluate("""
            () => {
                const result = {};
                result.url = window.location.href;
                result.title = document.title;

                // All visible buttons
                const buttons = document.querySelectorAll('button, input[type="submit"]');
                result.buttons = [];
                for (const btn of buttons) {
                    if (btn.offsetParent === null && !btn.offsetHeight) continue;
                    const style = window.getComputedStyle(btn);
                    result.buttons.push({
                        tag: btn.tagName,
                        type: btn.getAttribute('type') || '',
                        text: (btn.innerText || btn.value || '').trim().substring(0, 80),
                        id: btn.id || '',
                        name: btn.getAttribute('name') || '',
                        bg: style.backgroundColor,
                        color: style.color,
                        disabled: btn.disabled,
                    });
                }

                // Forms
                const forms = document.querySelectorAll('form');
                result.forms = [];
                for (const f of forms) {
                    result.forms.push({
                        action: f.getAttribute('action') || '',
                        method: f.method,
                    });
                }

                // Body text preview
                result.bodyText = (document.body ? document.body.innerText : '').substring(0, 1500);

                // Specific checks
                result.hasAgreeButton = !!document.querySelector('#agreeButton');
                result.hasConfirmInput = !!document.querySelector('input#confirm, input[name="confirm"]');

                return result;
            }
        """)
        print(f"  Title: {analysis.get('title')}")
        print(f"  #agreeButton: {analysis.get('hasAgreeButton')}")
        print(f"  #confirm: {analysis.get('hasConfirmInput')}")
        print(f"  Forms: {json.dumps(analysis.get('forms', []), indent=4)}")
        print(f"  Visible buttons:")
        for b in analysis.get('buttons', []):
            print(f"    [{b['tag']}] text='{b['text']}' type={b['type']} id={b['id']} bg={b['bg']} disabled={b['disabled']}")

        body = analysis.get('bodyText', '')
        # Show first 800 chars of body
        print(f"  Body text (first 800 chars):")
        for line in body[:800].split('\n'):
            line = line.strip()
            if line:
                print(f"    {line}")

        # Save full analysis
        with open(f"results/debug_{label}.json", "w") as f:
            json.dump(analysis, f, indent=2, ensure_ascii=False)

    except Exception as e:
        print(f"  JS analysis failed: {e}")


async def main():
    print(f"[*] Debug TOS flow for {EMAIL}")
    print(f"[*] Launching Camoufox (headed mode)...")

    async with BrowserSession(headless=False, proxy=None) as (browser, page):
        # Step 1: Pioneer login page
        print("[*] Navigating to Pioneer login...")
        try:
            await page.goto(PIONEER_LOGIN, wait_until="networkidle", timeout=30000)
        except Exception as e:
            print(f"[!] goto failed: {e}, trying domcontentloaded...")
            await page.goto(PIONEER_LOGIN, wait_until="domcontentloaded", timeout=30000)

        await asyncio.sleep(2)
        await dump_page(page, "01_pioneer_login")

        # Step 2: Click Google button
        print("\n[*] Looking for Google sign-in button...")
        google_clicked = False
        for sel in [
            'button:has-text("Continue with Google")',
            'button:has-text("Sign in with Google")',
            'button:has-text("Google")',
            'a:has-text("Continue with Google")',
            'a:has-text("Google")',
        ]:
            try:
                btn = page.locator(sel).first
                if await btn.is_visible(timeout=3000):
                    print(f"  Found: {sel}")
                    await btn.click()
                    google_clicked = True
                    break
            except Exception:
                continue

        if not google_clicked:
            print("[!] No Google button found!")
            await dump_page(page, "01b_no_google_btn")
            return

        await asyncio.sleep(3)

        # Step 3: Google login
        if "accounts.google.com" in page.url:
            await dump_page(page, "02_google_login")

            # Handle pre-login edge cases
            edge = await handle_all_edge_cases(page)
            if edge != "none":
                print(f"  Pre-login edge case: {edge}")
                await asyncio.sleep(2)

            print("[*] Entering Google credentials...")
            await google_login(page, EMAIL, PASSWORD)
            await asyncio.sleep(3)
        else:
            print(f"[*] Not on Google, URL: {page.url}")

        # Step 4: Post-login — this is where TOS shows up
        print("\n[*] Post-login state:")
        await dump_page(page, "03_post_login")

        # Loop to handle interstitials
        MAX_LOOPS = 10
        for i in range(MAX_LOOPS):
            url = page.url
            print(f"\n[Loop {i}] URL: {url}")

            if "agent.pioneer.ai" in url and "/login" not in url:
                print("[*] On Pioneer dashboard. Done!")
                await dump_page(page, f"04_dashboard_{i}")
                break

            # Dump current page
            await dump_page(page, f"04_interstitial_{i}")

            # Try edge case handler
            edge = await handle_all_edge_cases(page)
            print(f"  handle_all_edge_cases returned: '{edge}'")

            if edge != "none":
                print(f"  [OK] Handled: {edge}")
                await asyncio.sleep(3)
                continue

            # Edge cases didn't fire — maybe it's a page we don't recognize
            print("  [!] No edge case detected. Checking if this IS a TOS page...")

            # Manual check: is there ANY clickable button?
            try:
                btn_count = await page.evaluate("""
                    () => {
                        const btns = document.querySelectorAll('button, input[type="submit"]');
                        let visible = 0;
                        for (const b of btns) {
                            if (b.offsetParent !== null || b.offsetHeight > 0) visible++;
                        }
                        return visible;
                    }
                """)
                print(f"  Visible buttons on page: {btn_count}")
            except Exception:
                pass

            await asyncio.sleep(3)

        # Final state
        await dump_page(page, "99_final")
        print(f"\n[FINAL] URL: {page.url}")

        # Keep browser open for manual inspection
        print("\n[*] Keeping browser open 120s for manual inspection...")
        print("[*] Check results/debug_*.png and results/debug_*.json")
        await asyncio.sleep(120)


if __name__ == "__main__":
    asyncio.run(main())
