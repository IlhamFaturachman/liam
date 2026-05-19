"""
LIAM Harvest - Multi-Provider Batch Login Tool
FastAPI application with WebSocket live progress
"""

import asyncio
import json
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI, WebSocket, WebSocketDisconnect, UploadFile, File
from fastapi.responses import HTMLResponse, JSONResponse, FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel
from typing import Optional

from core.orchestrator import orchestrator
from core.providers import get_provider, list_providers
from utils.parser import parse_accounts
from utils.importer import import_to_proxy, generate_import_curl


class ConnectionManager:
    def __init__(self):
        self.active_connections: list[WebSocket] = []

    async def connect(self, websocket: WebSocket):
        await websocket.accept()
        self.active_connections.append(websocket)

    def disconnect(self, websocket: WebSocket):
        if websocket in self.active_connections:
            self.active_connections.remove(websocket)

    async def broadcast(self, data: dict):
        disconnected = []
        for conn in self.active_connections:
            try:
                await conn.send_json(data)
            except Exception:
                disconnected.append(conn)
        for conn in disconnected:
            self.active_connections.remove(conn)


manager = ConnectionManager()


async def on_update(status: dict):
    await manager.broadcast(status)


@asynccontextmanager
async def lifespan(app: FastAPI):
    orchestrator.set_update_callback(on_update)
    os.makedirs("results", exist_ok=True)
    yield


app = FastAPI(title="LIAM Harvest", lifespan=lifespan)
app.mount("/static", StaticFiles(directory="static"), name="static")


# --- Models ---

class StartRequest(BaseModel):
    provider: str = "antigravity"
    concurrency: Optional[int] = None
    headless: Optional[bool] = None


class AccountsInput(BaseModel):
    text: str


class ProxiesInput(BaseModel):
    text: str


# --- Routes ---

@app.get("/", response_class=HTMLResponse)
async def index():
    with open("static/index.html", "r", encoding="utf-8") as f:
        return HTMLResponse(content=f.read())


@app.get("/api/providers")
async def get_providers():
    """List available providers"""
    return list_providers()


@app.post("/api/accounts")
async def load_accounts(data: AccountsInput):
    accounts = parse_accounts(data.text)
    if not accounts:
        return JSONResponse(status_code=400, content={"error": "No valid accounts found"})
    orchestrator.load_accounts(accounts)
    return {"count": len(accounts), "accounts": [a["email"] for a in accounts]}


@app.post("/api/accounts/upload")
async def upload_accounts(file: UploadFile = File(...)):
    content = await file.read()
    text = content.decode("utf-8")
    accounts = parse_accounts(text)
    if not accounts:
        return JSONResponse(status_code=400, content={"error": "No valid accounts found"})
    orchestrator.load_accounts(accounts)
    return {"count": len(accounts), "accounts": [a["email"] for a in accounts]}


@app.post("/api/proxies")
async def load_proxies(data: ProxiesInput):
    proxies = [l.strip() for l in data.text.strip().splitlines() if l.strip() and not l.startswith("#")]
    if not proxies:
        return JSONResponse(status_code=400, content={"error": "No valid proxies found"})
    orchestrator.set_proxies(proxies)
    return {"count": len(proxies)}


@app.post("/api/start")
async def start_batch(req: StartRequest):
    if not orchestrator.accounts:
        return JSONResponse(status_code=400, content={"error": "No accounts loaded"})
    try:
        provider = get_provider(req.provider)
    except ValueError as e:
        return JSONResponse(status_code=400, content={"error": str(e)})

    asyncio.create_task(
        orchestrator.start(provider=provider, concurrency=req.concurrency, headless=req.headless)
    )
    return {"status": "started", "provider": provider.name}


@app.post("/api/pause")
async def pause_batch():
    orchestrator.pause()
    return {"status": "paused"}


@app.post("/api/resume")
async def resume_batch():
    orchestrator.resume()
    return {"status": "resumed"}


@app.post("/api/stop")
async def stop_batch():
    orchestrator.stop()
    return {"status": "stopped"}


@app.post("/api/retry")
async def retry_failed():
    if not orchestrator.failed:
        return JSONResponse(status_code=400, content={"error": "No failed accounts"})
    retryable = [f for f in orchestrator.failed if f.get("retryable", False)]
    if not retryable:
        return JSONResponse(status_code=400, content={"error": "No retryable failures"})
    retry_accounts = [{"email": f["email"], "password": f["password"]} for f in retryable]
    orchestrator.load_accounts(retry_accounts)
    provider = get_provider(orchestrator.provider.name if orchestrator.provider else "antigravity")
    asyncio.create_task(orchestrator.start(provider=provider))
    return {"status": "retrying", "count": len(retry_accounts)}


@app.get("/api/status")
async def get_status():
    return orchestrator.get_status()


@app.get("/api/results")
async def get_results():
    return {"results": orchestrator.results, "failed": orchestrator.failed}


@app.get("/api/export/json")
async def export_json():
    if not orchestrator.results:
        return JSONResponse(status_code=400, content={"error": "No results"})
    filepath = "results/success.json"
    with open(filepath, "w") as f:
        json.dump(orchestrator.results, f, indent=2, ensure_ascii=False)
    return FileResponse(filepath, media_type="application/json", filename="success.json")


class ImportRequest(BaseModel):
    proxy_url: str = "http://localhost:8080"


@app.post("/api/import")
async def import_results(req: ImportRequest):
    """Import harvest results directly to LIAM proxy server"""
    if not orchestrator.results:
        return JSONResponse(status_code=400, content={"error": "No results to import"})

    result = await import_to_proxy(orchestrator.results, proxy_url=req.proxy_url)
    return result


@app.get("/api/import/curl")
async def get_import_curl():
    """Get curl commands for manual import"""
    if not orchestrator.results:
        return JSONResponse(status_code=400, content={"error": "No results"})
    proxy_url = "http://localhost:8080"
    curl_script = generate_import_curl(orchestrator.results, proxy_url)
    return {"script": curl_script, "count": len(orchestrator.results)}


@app.get("/oauth/callback")
async def oauth_callback():
    return HTMLResponse(content="""
    <html><body style="background:#1a1a2e;color:#fff;display:flex;align-items:center;justify-content:center;height:100vh;font-family:sans-serif;">
    <div style="text-align:center;"><h1>Authorization Complete</h1><p>You can close this tab.</p></div>
    </body></html>""")


@app.websocket("/ws")
async def websocket_endpoint(websocket: WebSocket):
    await manager.connect(websocket)
    try:
        await websocket.send_json(orchestrator.get_status())
        while True:
            data = await websocket.receive_text()
            if data == "status":
                await websocket.send_json(orchestrator.get_status())
    except WebSocketDisconnect:
        manager.disconnect(websocket)


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("main:app", host="0.0.0.0", port=8000, reload=False)
