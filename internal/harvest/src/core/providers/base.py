"""
Provider Adapter Base Class
All providers implement this interface.
"""

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Optional


@dataclass
class HarvestResult:
    """Standardized result from any provider harvest"""
    provider: str
    email: str
    credentials: dict  # Provider-specific credentials (tokens, keys, etc.)
    metadata: dict = None  # Optional extra info (quota, tier, etc.)

    def to_dict(self) -> dict:
        return {
            "provider": self.provider,
            "email": self.email,
            "credentials": self.credentials,
            "metadata": self.metadata or {},
        }


class ProviderAdapter(ABC):
    """Abstract base class for provider login adapters"""

    @property
    @abstractmethod
    def name(self) -> str:
        """Provider identifier (e.g. 'antigravity', 'kiro', 'codebuddy')"""
        ...

    @property
    @abstractmethod
    def display_name(self) -> str:
        """Human-readable name (e.g. 'Antigravity (Gemini Code Assist)')"""
        ...

    @property
    @abstractmethod
    def auth_flow(self) -> str:
        """Auth flow type: 'google_oauth' | 'device_code' | 'import_token'"""
        ...

    @property
    def needs_browser(self) -> bool:
        """Whether this provider needs browser automation"""
        return self.auth_flow in ("google_oauth", "device_code")

    @property
    def needs_google_login(self) -> bool:
        """Whether this provider uses Google OAuth (shared login flow)"""
        return self.auth_flow == "google_oauth"

    @abstractmethod
    def build_auth_url(self, state: str) -> str:
        """Build the initial auth URL to navigate to"""
        ...

    @abstractmethod
    async def browser_flow(self, page, account: dict) -> dict:
        """
        Provider-specific browser flow AFTER Google login + consent.
        For google_oauth: receives page already on callback URL, extract code.
        For device_code: receives page, handle full device code flow.
        Returns provider-specific intermediate data (e.g. code, token, cookies).
        """
        ...

    @abstractmethod
    async def post_browser(self, intermediate: dict, account: dict) -> HarvestResult:
        """
        Post-browser processing (HTTP only, no browser needed).
        Exchange codes, fetch user info, onboard, etc.
        Returns standardized HarvestResult.
        """
        ...

    def get_credential_fields(self) -> list[str]:
        """List of credential fields this provider stores"""
        return ["access_token", "refresh_token"]
