"""
Antigravity Provider Adapter
Google OAuth → Token Exchange → loadCodeAssist → onboardUser
"""

import os
import secrets
import httpx
from urllib.parse import urlencode, urlparse, parse_qs
from datetime import datetime, timezone, timedelta

from core.providers.base import ProviderAdapter, HarvestResult


# OAuth Config
# These are the public Antigravity IDE OAuth credentials, used by all
# Antigravity users worldwide. Safe to ship — they identify the app
# (Antigravity IDE), not individual users.
# Override via env vars LIAM_AG_CLIENT_ID / LIAM_AG_CLIENT_SECRET if needed.
AG_CONFIG = {
    "client_id": os.environ.get("LIAM_AG_CLIENT_ID", "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"),
    "client_secret": os.environ.get("LIAM_AG_CLIENT_SECRET", "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"),
    "authorize_url": "https://accounts.google.com/o/oauth2/v2/auth",
    "token_url": "https://oauth2.googleapis.com/token",
    "userinfo_url": "https://www.googleapis.com/oauth2/v1/userinfo",
    "scopes": [
        "https://www.googleapis.com/auth/cloud-platform",
        "https://www.googleapis.com/auth/userinfo.email",
        "https://www.googleapis.com/auth/userinfo.profile",
        "https://www.googleapis.com/auth/cclog",
        "https://www.googleapis.com/auth/experimentsandconfigs",
    ],
    "redirect_uri": "http://localhost:8000/oauth/callback",
    "load_code_assist_url": "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
    "onboard_user_url": "https://cloudcode-pa.googleapis.com/v1internal:onboardUser",
}


class AntigravityProvider(ProviderAdapter):

    @property
    def name(self) -> str:
        return "antigravity"

    @property
    def display_name(self) -> str:
        return "Antigravity (Gemini Code Assist)"

    @property
    def auth_flow(self) -> str:
        return "google_oauth"

    def build_auth_url(self, state: str) -> str:
        params = {
            "client_id": AG_CONFIG["client_id"],
            "response_type": "code",
            "redirect_uri": AG_CONFIG["redirect_uri"],
            "scope": " ".join(AG_CONFIG["scopes"]),
            "state": state,
            "access_type": "offline",
            "prompt": "consent",
        }
        return f"{AG_CONFIG['authorize_url']}?{urlencode(params)}"

    async def browser_flow(self, page, account: dict) -> dict:
        """
        After Google login + consent, page should be on callback URL.
        Extract the authorization code.
        """
        url = page.url
        parsed = urlparse(url)
        params = parse_qs(parsed.query)

        if "error" in params:
            error = params["error"][0]
            desc = params.get("error_description", ["Unknown"])[0]
            raise Exception(f"OAuth error: {error} - {desc}")

        codes = params.get("code", [])
        if not codes:
            raise Exception(f"No code found in callback URL: {url}")

        return {"code": codes[0]}

    async def post_browser(self, intermediate: dict, account: dict) -> HarvestResult:
        """Exchange code → tokens → userinfo → loadCodeAssist → onboard"""
        code = intermediate["code"]

        async with httpx.AsyncClient(timeout=15) as client:
            # 1. Exchange code for tokens
            token_resp = await client.post(
                AG_CONFIG["token_url"],
                data={
                    "grant_type": "authorization_code",
                    "client_id": AG_CONFIG["client_id"],
                    "client_secret": AG_CONFIG["client_secret"],
                    "code": code,
                    "redirect_uri": AG_CONFIG["redirect_uri"],
                },
                headers={"Content-Type": "application/x-www-form-urlencoded"},
            )
            if token_resp.status_code != 200:
                raise Exception(f"Token exchange failed ({token_resp.status_code}): {token_resp.text}")

            tokens = token_resp.json()
            access_token = tokens["access_token"]
            refresh_token = tokens.get("refresh_token", "")
            expires_in = tokens.get("expires_in", 3600)

            # 2. Get user info
            user_resp = await client.get(
                f"{AG_CONFIG['userinfo_url']}?alt=json",
                headers={"Authorization": f"Bearer {access_token}"},
            )
            email = account["email"]
            if user_resp.status_code == 200:
                user_data = user_resp.json()
                email = user_data.get("email", email)

            # 3. Load Code Assist
            assist_resp = await client.post(
                AG_CONFIG["load_code_assist_url"],
                headers={
                    "Authorization": f"Bearer {access_token}",
                    "Content-Type": "application/json",
                    "User-Agent": "google-api-nodejs-client/9.15.1",
                    "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
                },
                json={"metadata": {"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}},
            )
            if assist_resp.status_code != 200:
                raise Exception(f"loadCodeAssist failed ({assist_resp.status_code}): {assist_resp.text}")

            assist_data = assist_resp.json()
            project_id = assist_data.get("cloudaicompanionProject", "")
            if isinstance(project_id, dict):
                project_id = project_id.get("id", "")

            tier_id = "legacy-tier"
            for tier in assist_data.get("allowedTiers", []):
                if tier.get("isDefault") and tier.get("id"):
                    tier_id = tier["id"].strip()
                    break

            if not project_id:
                raise Exception("No projectId in loadCodeAssist response")

            # 4. Onboard user (fire and forget with retries)
            final_project_id = project_id
            for attempt in range(10):
                onboard_resp = await client.post(
                    AG_CONFIG["onboard_user_url"],
                    headers={
                        "Authorization": f"Bearer {access_token}",
                        "Content-Type": "application/json",
                        "User-Agent": "google-api-nodejs-client/9.15.1",
                        "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
                    },
                    json={"tierId": tier_id, "metadata": {"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}},
                )
                if onboard_resp.status_code == 200:
                    onboard_data = onboard_resp.json()
                    if onboard_data.get("done"):
                        resp_project = onboard_data.get("response", {}).get("cloudaicompanionProject")
                        if resp_project:
                            if isinstance(resp_project, str):
                                final_project_id = resp_project.strip()
                            elif isinstance(resp_project, dict) and resp_project.get("id"):
                                final_project_id = resp_project["id"].strip()
                        break
                import asyncio
                await asyncio.sleep(5)

        # Compute expires_at
        expires_at = (datetime.now(timezone.utc) + timedelta(seconds=expires_in)).isoformat(timespec="milliseconds").replace("+00:00", "Z")

        return HarvestResult(
            provider="antigravity",
            email=email,
            credentials={
                "access_token": access_token,
                "refresh_token": refresh_token,
                "expires_at": expires_at,
                "project_id": final_project_id,
                "tier_id": tier_id,
                "scope": tokens.get("scope", " ".join(AG_CONFIG["scopes"])),
            },
            metadata={"tier_id": tier_id},
        )

    def get_credential_fields(self) -> list[str]:
        return ["access_token", "refresh_token", "expires_at", "project_id", "tier_id", "scope"]
