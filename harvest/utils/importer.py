"""
Auto-import harvest results to LIAM proxy server
"""

import httpx
import json
from typing import Optional


# Provider-specific import endpoints and payload builders.
# Each provider has its own LIAM endpoint that expects a specific body shape.
_IMPORT_ROUTES = {
    "antigravity": {
        "path": "/api/accounts/import/ag",
        "payload": lambda r: {"refresh_token": r.get("credentials", {}).get("refresh_token", "")},
    },
    "pioneer": {
        "path": "/api/accounts/import/pio",
        "payload": lambda r: {"api_key": r.get("credentials", {}).get("api_key", "")},
    },
    # Future providers:
    # "kiro": {"path": "/api/accounts/import/kiro", "payload": lambda r: ...},
}


async def import_to_proxy(
    results: list[dict],
    proxy_url: str = "http://localhost:8080",
    timeout: int = 15,
) -> dict:
    """
    Import harvest results directly to LIAM proxy's provider-specific
    import endpoints.

    Uses provider-specific routes when available (e.g. /api/accounts/import/ag
    for antigravity, /api/accounts/import/pio for pioneer). Falls back to
    generic /api/accounts for unknown providers.
    
    Args:
        results: List of harvest results (from orchestrator.results)
        proxy_url: LIAM proxy server URL
        timeout: Request timeout in seconds
    
    Returns:
        {"imported": N, "failed": N, "errors": [...]}
    """
    imported = 0
    failed = 0
    errors = []

    async with httpx.AsyncClient(timeout=timeout) as client:
        for result in results:
            try:
                provider = result.get("provider", "antigravity")
                route = _IMPORT_ROUTES.get(provider)

                if route:
                    # Use provider-specific import endpoint
                    url = f"{proxy_url}{route['path']}"
                    payload = route["payload"](result)
                else:
                    # Fallback: generic /api/accounts (original behavior)
                    url = f"{proxy_url}/api/accounts"
                    payload = {
                        "provider": provider,
                        "email": result.get("email", ""),
                        "credentials": result.get("credentials", {}),
                    }

                resp = await client.post(
                    url,
                    json=payload,
                    headers={"Content-Type": "application/json"},
                )

                if resp.status_code in (200, 201):
                    imported += 1
                else:
                    failed += 1
                    errors.append({
                        "email": result.get("email"),
                        "provider": provider,
                        "status": resp.status_code,
                        "error": resp.text[:200],
                    })

            except Exception as e:
                failed += 1
                errors.append({
                    "email": result.get("email"),
                    "error": str(e),
                })

    return {
        "imported": imported,
        "failed": failed,
        "errors": errors,
    }


def generate_import_curl(results: list[dict], proxy_url: str = "http://localhost:8080") -> str:
    """
    Generate curl commands for manual import (fallback if proxy not running).
    """
    lines = [
        f"# LIAM Harvest → Proxy Import",
        f"# Generated for {len(results)} accounts",
        f"# Target: {proxy_url}",
        "",
    ]

    for result in results:
        provider = result.get("provider", "antigravity")
        route = _IMPORT_ROUTES.get(provider)

        if route:
            url = f"{proxy_url}{route['path']}"
            payload = route["payload"](result)
        else:
            url = f"{proxy_url}/api/accounts"
            payload = {
                "provider": provider,
                "email": result.get("email", ""),
                "credentials": result.get("credentials", {}),
            }

        json_str = json.dumps(payload).replace("'", "'\\''")
        lines.append(
            f"curl -s -X POST {url} "
            f"-H 'Content-Type: application/json' "
            f"-d '{json_str}'"
        )
        lines.append("")

    return "\n".join(lines)
