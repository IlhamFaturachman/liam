"""
Orchestrator - manages batch login queue with concurrent workers
Generic: works with any provider adapter.
"""

import asyncio
import json
import os
import time
from datetime import datetime, timezone
from enum import Enum
from typing import Callable, Optional

from core.worker import process_account
from core.providers.base import ProviderAdapter, HarvestResult
from core.errors import BatchLoginError, classify_exception
from utils.delay import random_delay


# Settings
DEFAULT_CONCURRENCY = 4
WORKER_START_DELAY = (2000, 5000)
MAX_RETRY_ATTEMPTS = 1


class BatchState(str, Enum):
    IDLE = "idle"
    RUNNING = "running"
    PAUSED = "paused"
    RETRYING = "retrying"
    DONE = "done"


class AccountStatus(str, Enum):
    PENDING = "pending"
    RUNNING = "running"
    SUCCESS = "success"
    FAILED = "failed"
    RETRYING = "retrying"


class Orchestrator:
    """Manages batch login queue with concurrent workers"""

    def __init__(self):
        self.state: BatchState = BatchState.IDLE
        self.accounts: list[dict] = []
        self.results: list[dict] = []
        self.failed: list[dict] = []
        self.account_statuses: dict[str, dict] = {}
        self.concurrency: int = DEFAULT_CONCURRENCY
        self.headless: bool = False
        self.provider: Optional[ProviderAdapter] = None
        self.proxies: list[str] = []
        self._pause_event: asyncio.Event = asyncio.Event()
        self._pause_event.set()
        self._stop_flag: bool = False
        self._active_workers: int = 0
        self._total_processed: int = 0
        self._start_time: float = 0
        self._on_update: Optional[Callable] = None
        self._proxy_index: int = 0

    def set_update_callback(self, callback: Callable):
        self._on_update = callback

    def set_proxies(self, proxies: list[str]):
        self.proxies = [p.strip() for p in proxies if p.strip()]
        self._proxy_index = 0

    def _get_next_proxy(self) -> Optional[str]:
        if not self.proxies:
            return None
        proxy = self.proxies[self._proxy_index % len(self.proxies)]
        self._proxy_index += 1
        return proxy

    def load_accounts(self, accounts: list[dict]):
        self.accounts = accounts
        self.results = []
        self.failed = []
        self.account_statuses = {}
        self._total_processed = 0
        self._stop_flag = False
        for acc in accounts:
            self.account_statuses[acc["email"]] = {
                "status": AccountStatus.PENDING,
                "detail": "",
                "time": 0,
            }

    async def start(self, provider: ProviderAdapter, concurrency: int = None, headless: bool = None):
        if self.state == BatchState.RUNNING:
            return

        self.provider = provider
        if concurrency is not None:
            self.concurrency = concurrency
        if headless is not None:
            self.headless = headless

        self.state = BatchState.RUNNING
        self._stop_flag = False
        self._pause_event.set()
        self._start_time = time.time()
        await self._notify_update()

        # Queue pending accounts
        queue = asyncio.Queue()
        for acc in self.accounts:
            if self.account_statuses[acc["email"]]["status"] in (AccountStatus.PENDING, AccountStatus.RETRYING):
                await queue.put(acc)

        # Spawn workers
        workers = []
        for i in range(min(self.concurrency, queue.qsize())):
            await random_delay(*WORKER_START_DELAY)
            workers.append(asyncio.create_task(self._worker(i, queue)))

        await asyncio.gather(*workers)

        # Retry retryable failures
        retryable = [f for f in self.failed if f.get("retryable", False)]
        if retryable and MAX_RETRY_ATTEMPTS > 0 and not self._stop_flag:
            await self._retry_failed(retryable)

        self.state = BatchState.DONE
        await self._notify_update()
        self._save_results()

    async def _worker(self, worker_id: int, queue: asyncio.Queue):
        while not queue.empty() and not self._stop_flag:
            await self._pause_event.wait()
            try:
                account = queue.get_nowait()
            except asyncio.QueueEmpty:
                break

            email = account["email"]
            start_time = time.time()
            self._active_workers += 1
            self.account_statuses[email] = {"status": AccountStatus.RUNNING, "detail": "Starting...", "time": 0}
            await self._notify_update()

            proxy = self._get_next_proxy()

            try:
                result = await process_account(
                    account=account,
                    provider=self.provider,
                    worker_id=worker_id,
                    headless=self.headless,
                    proxy=proxy,
                    on_status=self._on_worker_status,
                )

                elapsed = round(time.time() - start_time, 1)
                self.results.append({**result.to_dict(), "time": elapsed})
                self.account_statuses[email] = {
                    "status": AccountStatus.SUCCESS,
                    "detail": f"Done in {elapsed}s",
                    "time": elapsed,
                }

            except BatchLoginError as e:
                elapsed = round(time.time() - start_time, 1)
                self.failed.append({
                    "email": email, "password": account["password"],
                    "error": e.message, "error_code": e.code.value,
                    "retryable": e.retryable,
                    "timestamp": datetime.now(timezone.utc).isoformat(),
                })
                self.account_statuses[email] = {
                    "status": AccountStatus.FAILED,
                    "detail": f"{e.code.value}: {e.message[:40]}",
                    "time": elapsed,
                }

            except Exception as e:
                elapsed = round(time.time() - start_time, 1)
                classified = classify_exception(e)
                self.failed.append({
                    "email": email, "password": account["password"],
                    "error": str(e), "error_code": classified.code.value,
                    "retryable": classified.retryable,
                    "timestamp": datetime.now(timezone.utc).isoformat(),
                })
                self.account_statuses[email] = {
                    "status": AccountStatus.FAILED,
                    "detail": str(e)[:50],
                    "time": elapsed,
                }

            finally:
                self._active_workers -= 1
                self._total_processed += 1
                await self._notify_update()

    async def _retry_failed(self, retryable: list[dict]):
        self.state = BatchState.RETRYING
        await self._notify_update()

        retryable_emails = {a["email"] for a in retryable}
        self.failed = [f for f in self.failed if f["email"] not in retryable_emails]

        retry_accounts = [{"email": a["email"], "password": a["password"]} for a in retryable]
        for acc in retry_accounts:
            self.account_statuses[acc["email"]] = {"status": AccountStatus.RETRYING, "detail": "Retrying...", "time": 0}

        queue = asyncio.Queue()
        for acc in retry_accounts:
            await queue.put(acc)

        workers = []
        for i in range(min(self.concurrency, queue.qsize())):
            await random_delay(*WORKER_START_DELAY)
            workers.append(asyncio.create_task(self._worker(i, queue)))
        await asyncio.gather(*workers)

    def pause(self):
        if self.state == BatchState.RUNNING:
            self.state = BatchState.PAUSED
            self._pause_event.clear()

    def resume(self):
        if self.state == BatchState.PAUSED:
            self.state = BatchState.RUNNING
            self._pause_event.set()

    def stop(self):
        self._stop_flag = True
        self._pause_event.set()
        self.state = BatchState.DONE

    def _on_worker_status(self, worker_id: int, email: str, status: str):
        if email in self.account_statuses:
            self.account_statuses[email]["detail"] = status
        if self._on_update:
            asyncio.ensure_future(self._notify_update())

    async def _notify_update(self):
        if self._on_update:
            await self._on_update(self.get_status())

    def get_status(self) -> dict:
        total = len(self.accounts)
        success = len(self.results)
        failed = len(self.failed)
        elapsed = round(time.time() - self._start_time, 1) if self._start_time else 0
        return {
            "state": self.state.value,
            "total": total,
            "processed": self._total_processed,
            "success": success,
            "failed": failed,
            "active_workers": self._active_workers,
            "concurrency": self.concurrency,
            "headless": self.headless,
            "provider": self.provider.name if self.provider else None,
            "elapsed": elapsed,
            "accounts": self.account_statuses,
        }

    def _save_results(self):
        os.makedirs("results", exist_ok=True)
        if self.results:
            with open("results/success.json", "w") as f:
                json.dump(self.results, f, indent=2, ensure_ascii=False)
        if self.failed:
            with open("results/failed.json", "w") as f:
                json.dump(self.failed, f, indent=2, ensure_ascii=False)


orchestrator = Orchestrator()
