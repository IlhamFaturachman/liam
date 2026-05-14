"""
LIAM Harvest - Configuration
"""

# Browser automation settings
BROWSER = {
    "headless": False,
    "concurrency": 4,
    "typing_delay": (50, 150),
    "pre_click_delay": (800, 2000),
    "post_login_delay": (1000, 3000),
    "worker_start_delay": (2000, 5000),
}

# Timeouts (ms)
TIMEOUTS = {
    "login": 15000,
    "consent": 10000,
    "callback": 30000,
    "token_exchange": 10000,
    "load_code_assist": 15000,
    "onboard": 60000,
}

# Retry settings
RETRY = {
    "max_retry_attempts": 1,
    "onboard_max_retries": 10,
    "onboard_retry_delay": 5,
    "exchange_retries": 2,
}

# Server
SERVER = {
    "host": "0.0.0.0",
    "port": 8000,
}
