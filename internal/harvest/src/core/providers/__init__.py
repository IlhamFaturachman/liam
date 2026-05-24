"""
Provider Registry
Register all providers here. Adding a new provider = add 1 import + 1 line.
"""

from core.providers.base import ProviderAdapter
from core.providers.antigravity import AntigravityProvider
from core.providers.pioneer import PioneerProvider

# Registry of all available providers
_PROVIDERS: dict[str, ProviderAdapter] = {
    "antigravity": AntigravityProvider(),
    "ag": AntigravityProvider(),  # Alias
    "pioneer": PioneerProvider(),
    "pio": PioneerProvider(),  # Alias
    # Future:
    # "kiro": KiroProvider(),
    # "codebuddy": CodeBuddyProvider(),
    # "kilocode": KiloCodeProvider(),
}


def get_provider(name: str) -> ProviderAdapter:
    """Get provider adapter by name"""
    name = name.lower().strip()
    if name not in _PROVIDERS:
        available = [k for k in _PROVIDERS.keys() if k != "ag"]  # Hide aliases
        raise ValueError(f"Unknown provider: '{name}'. Available: {available}")
    return _PROVIDERS[name]


def list_providers() -> list[dict]:
    """List all available providers (excluding aliases)"""
    seen = set()
    result = []
    for key, provider in _PROVIDERS.items():
        if provider.name not in seen:
            seen.add(provider.name)
            result.append({
                "name": provider.name,
                "display_name": provider.display_name,
                "auth_flow": provider.auth_flow,
                "needs_browser": provider.needs_browser,
            })
    return result
