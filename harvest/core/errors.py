"""
Error codes and classification system
Structured error handling with retryable vs non-retryable classification.
Inspired by enowxai-dupl error system.
"""

from enum import Enum
from typing import Optional


class ErrorCategory(str, Enum):
    """Error categories"""
    AUTH = "auth"              # Login/credential errors
    NETWORK = "network"       # Network/transport errors
    BROWSER = "browser"       # Browser automation errors
    PROVIDER = "provider"     # Provider API errors (Google, AG)
    INPUT = "input"           # Input validation errors
    SYSTEM = "system"         # Internal system errors


class ErrorCode(str, Enum):
    """Specific error codes"""
    # Auth (non-retryable)
    WRONG_PASSWORD = "AUTH_WRONG_PASSWORD"
    ACCOUNT_NOT_FOUND = "AUTH_ACCOUNT_NOT_FOUND"
    ACCOUNT_LOCKED = "AUTH_ACCOUNT_LOCKED"
    ACCOUNT_SUSPENDED = "AUTH_ACCOUNT_SUSPENDED"
    TWO_FA_REQUIRED = "AUTH_2FA_REQUIRED"
    VERIFY_CHALLENGE = "AUTH_VERIFY_CHALLENGE"
    CONSENT_DENIED = "AUTH_CONSENT_DENIED"

    # Network (retryable)
    TIMEOUT = "NETWORK_TIMEOUT"
    CONNECTION_ERROR = "NETWORK_CONNECTION_ERROR"
    DNS_ERROR = "NETWORK_DNS_ERROR"
    PROXY_ERROR = "NETWORK_PROXY_ERROR"
    HTTP_429 = "NETWORK_RATE_LIMITED"
    HTTP_5XX = "NETWORK_SERVER_ERROR"

    # Browser (retryable)
    BROWSER_LAUNCH_FAILED = "BROWSER_LAUNCH_FAILED"
    NAVIGATION_FAILED = "BROWSER_NAVIGATION_FAILED"
    ELEMENT_NOT_FOUND = "BROWSER_ELEMENT_NOT_FOUND"
    PAGE_CRASH = "BROWSER_PAGE_CRASH"

    # Provider (mixed)
    CAPTCHA_DETECTED = "PROVIDER_CAPTCHA"
    TOKEN_EXCHANGE_FAILED = "PROVIDER_TOKEN_EXCHANGE_FAILED"
    LOAD_CODE_ASSIST_FAILED = "PROVIDER_LOAD_CODE_ASSIST_FAILED"
    ONBOARD_FAILED = "PROVIDER_ONBOARD_FAILED"
    QUOTA_FETCH_FAILED = "PROVIDER_QUOTA_FETCH_FAILED"
    PROJECT_NOT_FOUND = "PROVIDER_PROJECT_NOT_FOUND"

    # Input (non-retryable)
    INVALID_FORMAT = "INPUT_INVALID_FORMAT"
    MISSING_FIELD = "INPUT_MISSING_FIELD"

    # System (retryable)
    UNHANDLED = "SYSTEM_UNHANDLED"
    DB_ERROR = "SYSTEM_DB_ERROR"


# Classification: which errors are worth retrying
RETRYABLE_ERRORS = {
    ErrorCode.TIMEOUT,
    ErrorCode.CONNECTION_ERROR,
    ErrorCode.DNS_ERROR,
    ErrorCode.PROXY_ERROR,
    ErrorCode.HTTP_429,
    ErrorCode.HTTP_5XX,
    ErrorCode.BROWSER_LAUNCH_FAILED,
    ErrorCode.NAVIGATION_FAILED,
    ErrorCode.ELEMENT_NOT_FOUND,
    ErrorCode.PAGE_CRASH,
    ErrorCode.CAPTCHA_DETECTED,  # Retryable (might not get captcha next time)
    ErrorCode.TOKEN_EXCHANGE_FAILED,
    ErrorCode.LOAD_CODE_ASSIST_FAILED,
    ErrorCode.ONBOARD_FAILED,
    ErrorCode.UNHANDLED,
}

NON_RETRYABLE_ERRORS = {
    ErrorCode.WRONG_PASSWORD,
    ErrorCode.ACCOUNT_NOT_FOUND,
    ErrorCode.ACCOUNT_LOCKED,
    ErrorCode.ACCOUNT_SUSPENDED,
    ErrorCode.TWO_FA_REQUIRED,
    ErrorCode.VERIFY_CHALLENGE,
    ErrorCode.CONSENT_DENIED,
    ErrorCode.PROJECT_NOT_FOUND,
    ErrorCode.INVALID_FORMAT,
    ErrorCode.MISSING_FIELD,
}


class BatchLoginError(Exception):
    """Structured error with code, category, and retryable flag"""

    def __init__(
        self,
        code: ErrorCode,
        message: str,
        category: Optional[ErrorCategory] = None,
        details: Optional[dict] = None,
    ):
        self.code = code
        self.message = message
        self.category = category or self._infer_category(code)
        self.retryable = code in RETRYABLE_ERRORS
        self.details = details or {}
        super().__init__(f"[{code.value}] {message}")

    @staticmethod
    def _infer_category(code: ErrorCode) -> ErrorCategory:
        prefix = code.value.split("_")[0]
        mapping = {
            "AUTH": ErrorCategory.AUTH,
            "NETWORK": ErrorCategory.NETWORK,
            "BROWSER": ErrorCategory.BROWSER,
            "PROVIDER": ErrorCategory.PROVIDER,
            "INPUT": ErrorCategory.INPUT,
            "SYSTEM": ErrorCategory.SYSTEM,
        }
        return mapping.get(prefix, ErrorCategory.SYSTEM)

    def to_dict(self) -> dict:
        return {
            "code": self.code.value,
            "category": self.category.value,
            "message": self.message,
            "retryable": self.retryable,
            "details": self.details,
        }


def classify_exception(e: Exception) -> BatchLoginError:
    """Convert generic exceptions to BatchLoginError"""
    from browser.google_login import LoginError
    from browser.consent import CaptchaError, ConsentError
    from core.oauth import OAuthError

    if isinstance(e, BatchLoginError):
        return e

    if isinstance(e, LoginError):
        code_map = {
            "WRONG_PASSWORD": ErrorCode.WRONG_PASSWORD,
            "ACCOUNT_NOT_FOUND": ErrorCode.ACCOUNT_NOT_FOUND,
            "ACCOUNT_LOCKED": ErrorCode.ACCOUNT_LOCKED,
        }
        code = code_map.get(e.code, ErrorCode.UNHANDLED)
        return BatchLoginError(code, e.message)

    if isinstance(e, CaptchaError):
        return BatchLoginError(ErrorCode.CAPTCHA_DETECTED, str(e))

    if isinstance(e, ConsentError):
        return BatchLoginError(ErrorCode.CONSENT_DENIED, str(e))

    if isinstance(e, OAuthError):
        msg = str(e).lower()
        if "token exchange" in msg:
            return BatchLoginError(ErrorCode.TOKEN_EXCHANGE_FAILED, str(e))
        if "loadcodeassist" in msg:
            return BatchLoginError(ErrorCode.LOAD_CODE_ASSIST_FAILED, str(e))
        if "onboard" in msg:
            return BatchLoginError(ErrorCode.ONBOARD_FAILED, str(e))
        if "project" in msg:
            return BatchLoginError(ErrorCode.PROJECT_NOT_FOUND, str(e))
        return BatchLoginError(ErrorCode.UNHANDLED, str(e))

    if isinstance(e, TimeoutError):
        return BatchLoginError(ErrorCode.TIMEOUT, str(e))

    if isinstance(e, ConnectionError):
        return BatchLoginError(ErrorCode.CONNECTION_ERROR, str(e))

    return BatchLoginError(ErrorCode.UNHANDLED, f"Unexpected: {type(e).__name__}: {e}")
