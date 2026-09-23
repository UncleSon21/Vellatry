"""Vellatry's embedding service.

One job: turn short marketing texts (a topic, a search query, a prompt) into vectors, so
the Go side can tell which of them mean the same thing. It holds no state, knows nothing
about tenants, and is reachable only on Fly's private network.

The model is pinned. Vectors from two different models are not comparable, so every
response names the model, and the Go side stores that name beside the vector and refuses
to compare across models.
"""

from __future__ import annotations

import os
from contextlib import asynccontextmanager
from typing import List

from fastapi import FastAPI, HTTPException
from fastembed import TextEmbedding
from pydantic import BaseModel, Field

# BAAI/bge-small-en-v1.5: 384 dimensions, MIT licence, a few milliseconds per text on a
# CPU. fastembed runs it through ONNX, so the image needs no PyTorch.
MODEL = os.getenv("EMBED_MODEL", "BAAI/bge-small-en-v1.5")
DIM = 384

# Limits, so one caller cannot ask for an unbounded amount of work.
MAX_TEXTS = 256
MAX_CHARS = 2000

_model: TextEmbedding | None = None


def model() -> TextEmbedding:
    global _model
    if _model is None:
        # The weights are baked into the image, so this only loads them into memory.
        _model = TextEmbedding(model_name=MODEL)
    return _model


@asynccontextmanager
async def lifespan(_: FastAPI):
    # Load and warm the model before the first request, not during it.
    list(model().embed(["warm"]))
    yield


app = FastAPI(title="Vellatry embeddings", docs_url=None, redoc_url=None, lifespan=lifespan)


class EmbedRequest(BaseModel):
    texts: List[str] = Field(min_length=1, max_length=MAX_TEXTS)


class EmbedResponse(BaseModel):
    model: str
    dim: int
    vectors: List[List[float]]


@app.get("/healthz")
def healthz() -> dict:
    return {"ok": True, "model": MODEL, "dim": DIM}


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest) -> EmbedResponse:
    texts = [t.strip()[:MAX_CHARS] for t in req.texts]
    if any(not t for t in texts):
        raise HTTPException(status_code=400, detail="every text must have content")
    vectors = [v.tolist() for v in model().embed(texts)]
    # fastembed returns unit vectors, which is what the Go side's cosine assumes.
    return EmbedResponse(model=MODEL, dim=DIM, vectors=vectors)
