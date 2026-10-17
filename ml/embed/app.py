"""Vellatry's embedding service.

Two jobs, both inference. It turns short marketing texts (a topic, a search query, a
prompt) into vectors, so the Go side can tell which of them mean the same thing; and it
scores a question against passages a notebook has already shortlisted, so the ones that
actually answer it come first. It holds no state, knows nothing about tenants, and is
reachable only on Fly's private network.

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
from fastembed.rerank.cross_encoder import TextCrossEncoder
from pydantic import BaseModel, Field

# BAAI/bge-small-en-v1.5: 384 dimensions, MIT licence, a few milliseconds per text on a
# CPU. fastembed runs it through ONNX, so the image needs no PyTorch.
MODEL = os.getenv("EMBED_MODEL", "BAAI/bge-small-en-v1.5")
DIM = 384

# Xenova/ms-marco-MiniLM-L-6-v2: a cross-encoder, Apache 2.0, 80 MB. Chosen by measuring
# every permissively licensed reranker fastembed offers against the corpus in eval/ (see
# the README for the numbers): it was best on all three measures, the smallest and the
# fastest, and one of the models it beat scored worse than no reranker at all.
#
# It reads the question and a passage together, which is why it places a passage an
# embedding ranks fourth, and why it is too slow to run over a whole notebook: the Go
# side shortlists first and sends only those.
RERANK_MODEL = os.getenv("RERANK_MODEL", "Xenova/ms-marco-MiniLM-L-6-v2")

# Limits, so one caller cannot ask for an unbounded amount of work.
MAX_TEXTS = 256
MAX_CHARS = 2000
MAX_PASSAGES = 64

_model: TextEmbedding | None = None
_reranker: TextCrossEncoder | None = None


def model() -> TextEmbedding:
    global _model
    if _model is None:
        # The weights are baked into the image, so this only loads them into memory.
        _model = TextEmbedding(model_name=MODEL)
    return _model


def reranker() -> TextCrossEncoder:
    global _reranker
    if _reranker is None:
        _reranker = TextCrossEncoder(model_name=RERANK_MODEL)
    return _reranker


@asynccontextmanager
async def lifespan(_: FastAPI):
    # Load and warm both models before the first request, not during it.
    list(model().embed(["warm"]))
    list(reranker().rerank("warm", ["warm"]))
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
    return {"ok": True, "model": MODEL, "dim": DIM, "rerank_model": RERANK_MODEL}


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest) -> EmbedResponse:
    texts = [t.strip()[:MAX_CHARS] for t in req.texts]
    if any(not t for t in texts):
        raise HTTPException(status_code=400, detail="every text must have content")
    vectors = [v.tolist() for v in model().embed(texts)]
    # fastembed returns unit vectors, which is what the Go side's cosine assumes.
    return EmbedResponse(model=MODEL, dim=DIM, vectors=vectors)


class RerankRequest(BaseModel):
    query: str = Field(min_length=1)
    passages: List[str] = Field(min_length=1, max_length=MAX_PASSAGES)


class RerankResponse(BaseModel):
    model: str
    scores: List[float]


@app.post("/rerank", response_model=RerankResponse)
def rerank(req: RerankRequest) -> RerankResponse:
    """Score each passage against the query, in the order they were sent.

    The scores are the cross-encoder's own logits: comparable with each other for this
    one query and meaningless on their own, so the caller orders by them and shows
    nobody a number.
    """
    passages = [p.strip()[:MAX_CHARS] for p in req.passages]
    if any(not p for p in passages):
        raise HTTPException(status_code=400, detail="every passage must have content")
    scores = [float(s) for s in reranker().rerank(req.query.strip()[:MAX_CHARS], passages)]
    return RerankResponse(model=RERANK_MODEL, scores=scores)
