"""
Auto-import harvest results to LIAM proxy server
"""

import httpx
import json
from typing import Optional


async def import_to_proxy(
    results: list[dict],
    proxy_url: str = "http://localhost:8080",
    timeout: int = 10,
) -> dict:
    """
    Import harvest results directly to LIAM proxy's /api/accounts endpoint.
    
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
                # Build account payload matching LIAM proxy's expected format
                payload = {
                    "provider": result.get("provider", "antigravity"),
                    "email": result.get("email", ""),
                    "credentials": result.get("credentials", {}),
                }

                resp = await client.post(
                    f"{proxy_url}/api/accounts",
                    json=payload,
                    headers={"Content-Type": "application/json"},
                )

                if resp.status_code in (200, 201):
                    imported += 1
                else:
                    failed += 1
                    errors.append({
                        "email": result.get("email"),
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
        payload = {
            "provider": result.get("provider", "antigravity"),
            "email": result.get("email", ""),
            "credentials": result.get("credentials", {}),
        }
        json_str = json.dumps(payload).replace("'", "'\\''")
        lines.append(
            f"curl -s -X POST {proxy_url}/api/accounts "
            f"-H 'Content-Type: application/json' "
            f"-d '{json_str}'"
        )
        lines.append("")

    return "\n".join(lines)
