"""
Parse account input (txt file or pasted text)
"""


def parse_accounts(text: str) -> list[dict]:
    """
    Parse accounts from text input.
    Format: email:password (one per line)
    Returns list of {"email": ..., "password": ...}
    """
    accounts = []
    seen_emails = set()

    for line in text.strip().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue

        # Support email:password or email|password
        separator = ":" if ":" in line else "|" if "|" in line else None
        if not separator:
            continue

        parts = line.split(separator, 1)
        if len(parts) != 2:
            continue

        email = parts[0].strip()
        password = parts[1].strip()

        if not email or not password:
            continue

        # Dedupe by email
        if email.lower() in seen_emails:
            continue
        seen_emails.add(email.lower())

        accounts.append({"email": email, "password": password})

    return accounts


def parse_accounts_file(filepath: str) -> list[dict]:
    """Parse accounts from a .txt file"""
    with open(filepath, "r", encoding="utf-8") as f:
        return parse_accounts(f.read())
